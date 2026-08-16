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
	hosts   map[string][]string // host -> disallowed path prefixes
}

func newRobots(f *fetcher) *robots {
	return &robots{fetcher: f, hosts: make(map[string][]string)}
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
	for _, prefix := range rules {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}

func (r *robots) rulesFor(ctx context.Context, u *url.URL) ([]string, error) {
	host := u.Scheme + "://" + u.Host

	r.mu.Lock()
	cached, ok := r.hosts[host]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	rules, err := r.fetch(ctx, host+"/robots.txt")
	if err != nil {
		rules = nil
	}

	r.mu.Lock()
	r.hosts[host] = rules
	r.mu.Unlock()

	return rules, err
}

func (r *robots) fetch(ctx context.Context, robotsURL string) ([]string, error) {
	body, err := r.fetcher.get(ctx, robotsURL, maxRobotsBytes)
	if err != nil {
		return nil, err
	}
	return parseRobots(string(body))
}

// parseRobots collects Disallow rules from the groups that apply to us: our own
// token or the wildcard. Other agents' groups are ignored.
func parseRobots(body string) ([]string, error) {
	var rules []string
	applies := false

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
			agent := strings.ToLower(value)
			applies = agent == "*" || agent == userAgentToken
		case "disallow":
			if applies && value != "" {
				rules = append(rules, value)
			}
		}
	}

	return rules, scanner.Err()
}
