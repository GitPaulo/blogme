package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimiterSpendsBurstThenRefuses(t *testing.T) {
	l := newLimiter(60, 3) // one token a second, three in hand
	now := time.Now()

	for i := range 3 {
		if ok, _ := l.allow("a", now); !ok {
			t.Fatalf("request %d was refused while the burst should still cover it", i+1)
		}
	}

	ok, wait := l.allow("a", now)
	if ok {
		t.Fatal("the fourth request should exhaust the burst")
	}
	if wait <= 0 || wait > time.Second {
		t.Errorf("wait = %v, want a positive delay no longer than the refill of one token", wait)
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	l := newLimiter(60, 1)
	now := time.Now()

	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("the first request should be allowed")
	}
	if ok, _ := l.allow("a", now); ok {
		t.Fatal("the second request at the same instant should be refused")
	}
	if ok, _ := l.allow("a", now.Add(time.Second)); !ok {
		t.Error("a second later the bucket should have refilled")
	}
}

func TestLimiterKeepsCallersApart(t *testing.T) {
	l := newLimiter(60, 1)
	now := time.Now()

	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("first caller should be allowed")
	}
	if ok, _ := l.allow("b", now); !ok {
		t.Error("one caller's spending must not count against another's")
	}
}

func TestLimiterSweepsIdleCallers(t *testing.T) {
	l := newLimiter(60, 1)
	now := time.Now()

	l.allow("a", now)
	// Long enough that the bucket has refilled and the sweep interval has passed,
	// so keeping the entry buys nothing.
	l.allow("b", now.Add(sweepInterval+2*time.Minute))

	if _, still := l.buckets["a"]; still {
		t.Error("an idle caller should be swept rather than held for the life of the instance")
	}
}

// A nil limiter is how a disabled limit is expressed, so it has to be callable.
func TestNilLimiterAllowsEverything(t *testing.T) {
	var l *limiter
	if ok, _ := l.allow("a", time.Now()); !ok {
		t.Error("a disabled limit must not refuse anything")
	}
	if newLimiter(0, 10) != nil || newLimiter(60, 0) != nil {
		t.Error("a non-positive rate or burst should produce a disabled limiter")
	}
}

func TestClientKeyPrefersTheAddressThePlatformAppends(t *testing.T) {
	for _, tc := range []struct {
		name      string
		forwarded string
		remote    string
		want      string
	}{
		{"no header falls back to the connection", "", "203.0.113.9:5555", "203.0.113.9"},
		{"single hop", "203.0.113.9", "10.0.0.1:5555", "203.0.113.9"},
		{"platform appends the real caller last", "1.2.3.4, 203.0.113.9", "10.0.0.1:5555", "203.0.113.9"},
		{"port is stripped", "203.0.113.9:41234", "10.0.0.1:5555", "203.0.113.9"},
		{"spoofed leading entry is ignored", "evil, 203.0.113.9", "10.0.0.1:5555", "203.0.113.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/search?q=go", nil)
			r.RemoteAddr = tc.remote
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := clientKey(r); got != tc.want {
				t.Errorf("clientKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchThrottlesAndSaysHowLongToWait(t *testing.T) {
	h := newTestHandlers(t, `{"@odata.count":0,"value":[]}`)
	// Tight enough to trip on the second request, so the test does not depend on
	// the shipped defaults staying where they are.
	h.limits = Limits{PerMinute: 60, Burst: 1}
	h.perClient = newLimiter(60, 1)

	if code := get(t, h, "/api/search?q=go").Code; code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", code)
	}

	rec := get(t, h, "/api/search?q=go")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", rec.Code)
	}
	for _, header := range []string{"Retry-After", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset"} {
		if rec.Header().Get(header) == "" {
			t.Errorf("a throttled response should carry %s", header)
		}
	}
}

// Running out of reranking must not take search down with it: the query is
// answered with keyword ranking instead, which is what the index client already
// does when the reranker itself is unavailable.
func TestSemanticBudgetDowngradesRatherThanRefusing(t *testing.T) {
	h := newTestHandlers(t, `{"@odata.count":0,"value":[]}`)
	h.semanticAll = newLimiter(60, 1)

	if code := get(t, h, "/api/search?q=go&mode=semantic").Code; code != http.StatusOK {
		t.Fatalf("first semantic request: got %d, want 200", code)
	}
	if code := get(t, h, "/api/search?q=go&mode=semantic").Code; code != http.StatusOK {
		t.Error("a spent semantic budget should still answer the query, ranked by keyword")
	}
}

// getFrom issues a search that appears to come from a given address, which is what
// the platform's X-Forwarded-For hop looks like to clientKey.
func getFrom(t *testing.T, h *Handlers, addr string) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/api/search?q=go", nil)
	r.Header.Set("X-Forwarded-For", addr)

	rec := httptest.NewRecorder()
	h.Search(rec, r)
	return rec
}

// A per-caller limit is no limit against a flood, because a flood is polite from
// every address it uses. The service-wide bucket is the one that has to catch it.
func TestSearchServiceWideLimitCatchesAFloodFromManyAddresses(t *testing.T) {
	limits := DefaultLimits()
	limits.AllPerMinute, limits.AllBurst = 2, 2

	h, _ := newCapturingHandlers(t, "", emptyResult, limits)

	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		// A different address each time, so the per-caller bucket is always full.
		addr := fmt.Sprintf("198.51.100.%d", i+1)
		if got := getFrom(t, h, addr).Code; got != want {
			t.Errorf("request %d from %s: status = %d, want %d", i+1, addr, got, want)
		}
	}
}

// The limit only earns its place if it never fires on a good day. Busiest minute
// ever observed in production is under one search; this is a hundred in a burst.
func TestSearchServiceWideLimitStaysOutOfTheWayByDefault(t *testing.T) {
	h, _ := newCapturingHandlers(t, "", emptyResult, DefaultLimits())

	for i := range 100 {
		if got := getFrom(t, h, fmt.Sprintf("203.0.113.%d", i%256)).Code; got != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d", i+1, got, http.StatusOK)
		}
	}
}

// Refusing a request is the cheapest thing the service does and logging it is not,
// so the logging is bounded too. What must survive the bounding is the count: a
// line that stands for thousands has to say so, or a flood looks like a trickle.
func TestSearchThrottleLoggingIsBoundedButKeepsTheCount(t *testing.T) {
	buf := captureLogs(t)

	limits := DefaultLimits()
	limits.AllPerMinute, limits.AllBurst = 1, 1

	h, _ := newCapturingHandlers(t, "", emptyResult, limits)

	const refusals = 40
	for i := range refusals + 1 {
		getFrom(t, h, fmt.Sprintf("192.0.2.%d", i%256))
	}

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if record["msg"] == "search throttled" {
			lines = append(lines, record)
		}
	}

	// The gate's burst, and no more: the refusals arrive far faster than it refills.
	if len(lines) != throttleLogBurst {
		t.Fatalf("throttle log lines = %d, want %d", len(lines), throttleLogBurst)
	}
	if scope := lines[0]["scope"]; scope != "service" {
		t.Errorf("scope = %v, want \"service\"", scope)
	}

	// Every refusal is counted whether or not it was logged, so a burst that stops
	// before the next line is due is still visible the moment one is.
	if got, want := h.refused.Load(), int64(refusals); got != want {
		t.Errorf("refused total = %d, want %d", got, want)
	}
	if got := lines[len(lines)-1]["refused_total"].(float64); got != float64(len(lines)) {
		t.Errorf("last logged refused_total = %v, want %d", got, len(lines))
	}
}
