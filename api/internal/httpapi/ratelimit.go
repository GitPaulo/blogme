package httpapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits caps how often one caller, and the service as a whole, may search.
//
// The endpoint is deliberately anonymous: there is no key to revoke and no account to
// suspend, so a rate limit is the only thing between a script and the bill. Two
// resources are protected and they are not the same size. A search costs a function
// execution and some search capacity; a semantic search also spends from the
// reranker's metered monthly allowance, which on the free plan is 1,000 queries for
// the whole service.
//
// The limits are per instance, not global. Flex Consumption scales out, so a caller
// spread across instances gets a correspondingly larger allowance. These numbers
// bound the blast radius rather than enforce a budget: they turn "burn the month's
// reranking in a minute" into "burn it over many hours", which is long enough to
// notice.
type Limits struct {
	// PerMinute and Burst apply to every search, keyed by caller address.
	PerMinute int
	Burst     int
	// AllPerMinute and AllBurst apply to every search from everyone at once. A
	// per-caller limit is no limit at all against traffic spread over many addresses,
	// which is what a flood looks like; this is the only figure that bounds what the
	// instance will serve in total.
	AllPerMinute int
	AllBurst     int
	// SemanticPerMinute and SemanticBurst additionally apply to one caller's semantic
	// queries.
	SemanticPerMinute int
	SemanticBurst     int
	// SemanticPerHour and SemanticHourBurst apply to semantic queries from everyone at
	// once, which is the only limit that touches the monthly quota.
	SemanticPerHour   int
	SemanticHourBurst int
	// SuggestPerMinute and SuggestBurst apply to one caller's typeahead requests, and
	// SuggestAllPerMinute and SuggestAllBurst to everyone's at once.
	//
	// Counted apart from search rather than drawn from the same buckets, because the
	// two are not the same request. Typeahead fires several times per search by
	// design, so sharing a bucket would mean one reader typing one query tripped their
	// own search limit; and a completion is a prefix lookup rather than a scored query
	// over the corpus, so it is not worth the same allowance. The cost that remains
	// shared is the instance an execution runs on, and what bounds that is
	// maximumInstanceCount rather than anything here.
	SuggestPerMinute    int
	SuggestBurst        int
	SuggestAllPerMinute int
	SuggestAllBurst     int
}

// DefaultLimits is sized for a personal search engine: generous enough that a reader
// typing in the search box never sees a 429, tight enough that a loop does.
func DefaultLimits() Limits {
	return Limits{
		PerMinute: 60,
		// Sized against the client's own fan-out rather than against someone typing.
		// One "load more" chases page after page while a filter hides what arrives, so
		// two clicks can spend tens of requests in a few seconds. What bounds a flood
		// is AllPerMinute below; this only has to stop a single caller looping, which
		// at a steady sixty a minute it still does.
		Burst: 60,
		// Set from what the service can serve rather than from what it receives: the
		// search tier folds well below this, so a limit here can only refuse traffic
		// that was going to fail anyway. The busiest real minute observed is under one
		// search.
		AllPerMinute:      600,
		AllBurst:          300,
		SemanticPerMinute: 10,
		SemanticBurst:     5,
		SemanticPerHour:   60,
		SemanticHourBurst: 15,
		// Sized against a reader typing rather than against a search. The client waits
		// out a pause before asking and answers repeats from its own cache, so a
		// sentence typed straight through costs a handful of requests; four a second
		// sustained is well past anyone's hands and well short of what a script would
		// need to hurt.
		SuggestPerMinute: 240,
		SuggestBurst:     60,
		// Twice the search allowance, for the one request that is meant to arrive
		// several times per search. Like AllPerMinute this is the figure that bounds a
		// flood arriving from many addresses, each of them polite.
		SuggestAllPerMinute: 1200,
		SuggestAllBurst:     600,
	}
}

// How often a rejection may be logged, and how many lines may arrive at once.
//
// A flood produces one refusal per request, and a record per refusal turns the
// cheapest path in the service into the most expensive one, since telemetry is billed
// by volume. Rate-limiting the log rather than dropping it keeps the signal: every
// line carries the running refusal count, so nothing is lost by the lines that never
// happened.
const (
	throttleLogPerMinute = 6
	throttleLogBurst     = 3
)

// How often idle buckets are swept out of the map. A discovery-free API instance can
// live for days, and a map keyed by caller address would otherwise only grow.
const sweepInterval = 10 * time.Minute

// globalKey is the bucket every caller shares, for limits that are service-wide.
const globalKey = "*"

// bucket is one key's allowance, refilled continuously rather than on a schedule so a
// caller never has to wait for a window boundary to make one more request.
type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

// newLimiter returns nil when either figure is non-positive, and a nil limiter allows
// everything. That keeps "this limit is turned off" out of every call site.
func newLimiter(perMinute float64, burst int) *limiter {
	if perMinute <= 0 || burst <= 0 {
		return nil
	}
	return &limiter{
		rate:    perMinute / 60,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
		swept:   time.Now(),
	}
}

// allow spends one token for key, reporting whether one was available and how long
// the caller should wait if it was not.
func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now

	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	}

	b.tokens--
	return true, 0
}

// sweep drops buckets idle long enough to have refilled completely, since recreating
// one of those costs exactly what keeping it does.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < sweepInterval {
		return
	}
	l.swept = now

	idle := time.Duration(l.burst/l.rate*float64(time.Second)) + sweepInterval
	for key, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, key)
		}
	}
}

// clientKey identifies the caller for throttling.
//
// Azure App Service appends the real client address to X-Forwarded-For, so the last
// entry is the one the platform vouches for; earlier entries are whatever the caller
// chose to send. Hence reading the list from the right rather than the left. Falls
// back to the connection's own address, which is what local development and the tests
// see.
func clientKey(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		hops := strings.Split(forwarded, ",")
		if addr := strings.TrimSpace(hops[len(hops)-1]); addr != "" {
			return stripPort(addr)
		}
	}
	return stripPort(r.RemoteAddr)
}

func stripPort(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// writeRateLimited answers a throttled request with what a client needs to back off
// politely: Retry-After, which is understood everywhere, alongside the RateLimit-*
// family that most API clients now read.
// https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/
func writeRateLimited(ctx context.Context, w http.ResponseWriter, limit int, wait time.Duration) {
	// Rounded up, because a client that waits the truncated value arrives early and is
	// refused again.
	seconds := int(wait.Seconds()) + 1

	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	w.Header().Set("RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("RateLimit-Remaining", "0")
	w.Header().Set("RateLimit-Reset", strconv.Itoa(seconds))

	writeError(ctx, w, http.StatusTooManyRequests, "too many requests; wait a moment and try again")
}
