package discovery

import (
	"net/http"
	"strings"
)

// framingDenied reports whether a page's own response headers refuse to let another
// site show it inside a frame, which is what the web app's hover preview does.
//
// Two headers say it. X-Frame-Options is the older one and speaks only of DENY and
// SAMEORIGIN, both of which exclude us. Content-Security-Policy's frame-ancestors
// replaces it and takes a source list, and anything short of a wildcard is read here
// as a refusal: the only list that would let the app through is one that names it,
// and no blog names it. Being wrong that way costs a preview we decline to open;
// being wrong the other way costs a frame the browser blocks and a console error,
// which is the thing this exists to avoid.
//
// A report-only policy is not enforced by the browser, so it is not read here.
func framingDenied(header http.Header) bool {
	for _, value := range header.Values("X-Frame-Options") {
		// A header carrying more than one value is malformed, and browsers refuse
		// framing rather than pick one, so each part is read on its own terms.
		for _, part := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(part)) {
			case "deny", "sameorigin":
				return true
			}
		}
	}

	// Every enforcing policy applies at once, so one refusal among several is a refusal.
	for _, policy := range header.Values("Content-Security-Policy") {
		for _, directive := range strings.Split(policy, ";") {
			name, sources, _ := strings.Cut(strings.TrimSpace(directive), " ")
			if !strings.EqualFold(name, "frame-ancestors") {
				continue
			}
			if !framableByAnyone(strings.Fields(sources)) {
				return true
			}
		}
	}

	return false
}

// framableByAnyone reports whether a frame-ancestors source list lets an arbitrary
// site frame the page. Only a wildcard and a bare https: scheme do; 'none', 'self'
// and a list of hosts all describe somewhere this app is not. An empty list is
// 'none' by omission.
func framableByAnyone(sources []string) bool {
	for _, source := range sources {
		if source == "*" || strings.EqualFold(source, "https:") {
			return true
		}
	}
	return false
}
