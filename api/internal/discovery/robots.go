package discovery

import (
	"bufio"
	"context"
	"net/url"
	"strings"
	"sync"
)

// robots caches one robots.txt decision set per host. A discovery run touches many
// pages on the same host, and re-fetching robots.txt for each would be both slow and
// rude.
type robots struct {
	fetcher *fetcher
	mu      sync.Mutex
	hosts   map[string]robotRules
}

// robotRules is what one robots.txt tells us: the rules of the group that applies
// to us, and where the site says its sitemaps are.
type robotRules struct {
	rules    []robotRule
	sitemaps []string
}

// robotRule is one Allow or Disallow line. Precedence under RFC 9309 §2.2.2 goes
// to the longest matching pattern, so the pattern is kept rather than only its
// effect.
type robotRule struct {
	pattern string
	allow   bool
}

func newRobots(f *fetcher) *robots {
	return &robots{fetcher: f, hosts: make(map[string]robotRules)}
}

// allowed reports whether the crawler may fetch u. Unreachable or unparseable
// robots.txt is treated as permissive, which matches common crawler behaviour.
func (r *robots) allowed(ctx context.Context, u *url.URL) bool {
	rules, err := r.rulesFor(ctx, u)
	if err != nil {
		return true
	}

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	// Patterns are written against the path and query together, which is what makes
	// a rule like "Disallow: /*?" mean anything.
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	// The longest matching pattern wins and Allow takes a tie. Without this, a site
	// that closes a whole tree and reopens one branch of it loses the branch, and a
	// site that closes one branch of an open tree keeps it.
	var best robotRule
	for _, rule := range rules.rules {
		if len(rule.pattern) < len(best.pattern) || !matchPath(rule.pattern, path) {
			continue
		}
		if len(rule.pattern) > len(best.pattern) || rule.allow {
			best = rule
		}
	}

	return best.pattern == "" || best.allow
}

// matchPath reports whether a robots.txt pattern matches a path. '*' stands for
// any run of characters and a trailing '$' anchors the end; anything else is a
// prefix match.
//
// The literal-prefix comparison this replaces silently ignored every wildcard
// rule — "/*/private" was compared as though a real path might begin with those
// characters, so it never matched and the crawler fetched exactly what the site
// had asked it not to.
func matchPath(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}

	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	rest := path[len(parts[0]):]

	for i, part := range parts[1:] {
		last := i == len(parts)-2
		if part == "" {
			// A trailing '*' absorbs whatever is left, anchored or not.
			if last {
				return true
			}
			continue
		}
		// Anchoring asks about the end of the path, so the final literal has to be
		// found there rather than at its first occurrence.
		if last && anchored {
			return strings.HasSuffix(rest, part)
		}
		at := strings.Index(rest, part)
		if at < 0 {
			return false
		}
		rest = rest[at+len(part):]
	}

	if anchored {
		return rest == ""
	}
	return true
}

// sitemapsFor returns the sitemap URLs the host advertises, if any.
func (r *robots) sitemapsFor(ctx context.Context, u *url.URL) []string {
	rules, err := r.rulesFor(ctx, u)
	if err != nil {
		return nil
	}
	return rules.sitemaps
}

func (r *robots) rulesFor(ctx context.Context, u *url.URL) (robotRules, error) {
	host := u.Scheme + "://" + u.Host

	r.mu.Lock()
	cached, ok := r.hosts[host]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	rules, err := r.fetch(ctx, host+"/robots.txt")
	if err != nil {
		rules = robotRules{}
	}

	r.mu.Lock()
	r.hosts[host] = rules
	r.mu.Unlock()

	return rules, err
}

func (r *robots) fetch(ctx context.Context, robotsURL string) (robotRules, error) {
	body, err := r.fetcher.get(ctx, robotsURL, maxRobotsBytes)
	if err != nil {
		return robotRules{}, err
	}
	return parseRobots(string(body))
}

// robotGroup is one user-agent block. Consecutive User-agent lines share the rules
// that follow them, so a group holds every agent it was addressed to.
type robotGroup struct {
	agents []string
	rules  []robotRule
}

// parseRobots reads every group, then keeps the rules of the one that governs us.
// Sitemap lines are host-wide rather than per-group, so they are always kept.
func parseRobots(body string) (robotRules, error) {
	var (
		out    robotRules
		groups []robotGroup
		// A User-agent line following a rule line opens a new group; one following
		// another User-agent line joins the group already open.
		inRules bool
	)

	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			if inRules || len(groups) == 0 {
				groups = append(groups, robotGroup{})
				inRules = false
			}
			group := &groups[len(groups)-1]
			group.agents = append(group.agents, strings.ToLower(value))

		case "allow", "disallow":
			if len(groups) == 0 {
				// A rule before any User-agent line is addressed to nobody.
				continue
			}
			inRules = true
			if value == "" {
				// "Disallow:" with no path is how a group says it blocks nothing.
				continue
			}
			group := &groups[len(groups)-1]
			group.rules = append(group.rules, robotRule{pattern: value, allow: key == "allow"})

		case "sitemap":
			// Cut splits at the first colon, so value already holds the whole URL.
			if value != "" {
				out.sitemaps = append(out.sitemaps, value)
			}
		}
	}

	out.rules = rulesForAgent(groups)
	return out, scanner.Err()
}

// rulesForAgent picks the group that governs us: one naming this crawler if the
// file has one, and the wildcard group otherwise. A file that addresses us by name
// is talking to us specifically, so its group replaces the wildcard rather than
// adding to it. Groups repeating the same agent are merged, which is common in
// hand-maintained files.
func rulesForAgent(groups []robotGroup) []robotRule {
	var named, wildcard []robotRule
	// Tracked separately, because an empty group addressed to us is a decision —
	// it says we may go anywhere — and not an absence of one.
	addressed := false

	for _, group := range groups {
		for _, agent := range group.agents {
			switch agent {
			case userAgentToken:
				addressed = true
				named = append(named, group.rules...)
			case "*":
				wildcard = append(wildcard, group.rules...)
			}
		}
	}

	if addressed {
		return named
	}
	return wildcard
}
