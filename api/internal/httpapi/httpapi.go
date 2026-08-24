package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/index"
)

const (
	// Counted in runes rather than bytes, because the browser holding the search box
	// counts characters. Measured in bytes the same number rejects a query the client
	// had already judged short enough, and does so three times sooner in Japanese
	// than in English.
	maxQueryLen  = 512
	defaultLimit = 20
	maxLimit     = 50
	// Semantic reranking only reorders the top 50 keyword matches, so that window is
	// the entire result set worth offering in that mode: past it the ordering
	// silently reverts to keyword scoring part-way down a scroll, which reads as the
	// results getting worse for no reason.
	semanticWindow = 50
	// Keyword ranking scores the whole result set, so it can page as deep as is worth
	// paying for. Relevance is long gone by this depth, so the tail stops here.
	maxKeywordOffset = 1000
	// Documents read per row a page is meant to hold. See fetchFor.
	overFetch = 3
	// How much of a query reaches the logs. Knowing what was searched for is the
	// difference between "search is slow" and "this search is slow", but a query is
	// third-party input and the telemetry bill is charged by volume.
	maxLoggedQuery = 128
	// How long a browser may reuse a page of results. Repeats are ordinary — a
	// reload, a shared link opened twice, the back button — and each one avoided is
	// an execution not billed and an index query not made. Kept short because
	// discovery adds documents every hour.
	searchMaxAge = 60
)

// logQuery makes a caller's query safe to log.
//
// Control characters are folded to spaces so nothing in a query can forge a line
// break and fake a second log record, and the result is cut on a rune boundary so
// a multi-byte character is never split into mojibake.
func logQuery(q string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, q)

	if runes := []rune(cleaned); len(runes) > maxLoggedQuery {
		return string(runes[:maxLoggedQuery]) + "…"
	}
	return cleaned
}

// allowSemantic spends from the reranking budgets, the caller's own first. Order
// matters: checking the service-wide budget first would let a caller who is
// already over their own limit go on draining the allowance everyone shares.
func (h *Handlers) allowSemantic(caller string, now time.Time) bool {
	if ok, _ := h.semanticClient.allow(caller, now); !ok {
		return false
	}
	ok, _ := h.semanticAll.allow(globalKey, now)
	return ok
}

// maxOffsetFor reports how deep the given ranking mode may page when serving pages
// of the given size.
//
// The page size is part of the answer because the *last* page is what has to land
// inside the window the mode can actually order, and for semantic ranking that is
// the reranked window rather than the whole result set. Deriving the limit from a
// fixed page size instead lets a caller who asks for a larger page read straight
// past the window and back into keyword ordering, which is the exact failure the
// window exists to prevent.
func maxOffsetFor(rank string, limit int) int {
	if rank == index.RankKeyword {
		return maxKeywordOffset
	}
	return max(semanticWindow-limit, 0)
}

// fetchFor reports how many documents to read to fill a page of the given size.
//
// More than the page holds, because the per-source cap discards rows after the index
// has ranked them: read exactly a page's worth and a query one blog dominates comes
// back with whatever survives. "claude" returned three rows of twenty, its first
// twenty-nine matches all being the same site — and the same query yields 24 usable
// rows inside its first 50 documents, so the results were there, just past where a
// page-sized read looks. Three times is that measured worst case with room to spare;
// the cost is documents transferred, so there is no case for making it larger.
func fetchFor(rank string, limit, offset int) int {
	fetch := limit * overFetch
	if rank == index.RankKeyword {
		return fetch
	}
	// Semantic ranking only orders the reranked window. Reading past it would top the
	// page up with rows the reranker never sorted — keyword order wearing a semantic
	// label — so the tail of that window returns a short page instead.
	return min(fetch, max(semanticWindow-offset, 0))
}

type Handlers struct {
	index *index.Index

	// The endpoint is anonymous by design, so these are the only bound on what a
	// single caller can spend. See ratelimit.go for what each one protects.
	limits         Limits
	perClient      *limiter
	all            *limiter
	semanticClient *limiter
	semanticAll    *limiter

	// Throttling is loud when it fires, but not once per refused request: see
	// throttleLogPerMinute. refused counts every refusal this instance has made,
	// and rides on each line that gets past the gate.
	throttleLog *limiter
	refused     atomic.Int64
}

func New(idx *index.Index, limits Limits) *Handlers {
	return &Handlers{
		index:          idx,
		limits:         limits,
		perClient:      newLimiter(float64(limits.PerMinute), limits.Burst),
		all:            newLimiter(float64(limits.AllPerMinute), limits.AllBurst),
		semanticClient: newLimiter(float64(limits.SemanticPerMinute), limits.SemanticBurst),
		semanticAll:    newLimiter(float64(limits.SemanticPerHour)/60, limits.SemanticHourBurst),
		throttleLog:    newLimiter(throttleLogPerMinute, throttleLogBurst),
	}
}

// logThrottled reports a refusal without letting the reporting become the flood.
//
// Warn, not Info: being throttled is either abuse worth looking at or a limit set
// too low, and both need someone to notice. scope says which limit was reached, so
// "one caller is hammering us" and "everyone at once is" are not the same line.
//
// refused_total is cumulative for the life of the instance rather than a count of
// what the gate just held back, so no refusal is ever lost to a burst that stopped
// before the next line was due. It also reads better than a delta would: the jump
// between two consecutive lines is the rate the flood was arriving at.
func (h *Handlers) logThrottled(ctx context.Context, scope, caller string, wait time.Duration, now time.Time) {
	refused := h.refused.Add(1)

	if ok, _ := h.throttleLog.allow(globalKey, now); !ok {
		return
	}

	slog.WarnContext(ctx, "search throttled",
		"scope", scope,
		"caller", caller,
		"retry_after_s", int(wait.Seconds())+1,
		"refused_total", refused)
}

type searchResponse struct {
	Query string `json:"query"`
	// Count is the size of this page; Total is how many matches exist in all.
	Count  int `json:"count"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	// NextOffset is the offset to ask for to continue from here. A client must use
	// it rather than adding its own page size: the per-source cap drops rows after
	// ranking, so a page is wider than the rows it returns. See index.Page.
	NextOffset int `json:"nextOffset"`
	// Exhausted says this page reached the end of the index. A client that has paged
	// to here holds every row there is, and holds fewer than Total, which counts the
	// documents the per-source cap dropped along with the ones it kept. See
	// index.Page.Exhausted.
	Exhausted bool             `json:"exhausted"`
	Results   []article.Result `json:"results"`
}

// searchParams is a search request that has already been checked over.
type searchParams struct {
	q      string
	limit  int
	offset int
	origin string
	rank   string
}

// parseSearch validates every query parameter, returning the message to answer a
// 400 with, or an empty string when the request is sound.
//
// All of the checking lives here and none of it spends anything, which is the point
// of it being a function rather than a run of checks inline: the reranking budget
// the handler spends afterwards is shared by everyone on the instance, and keeping
// the two apart is what stops malformed traffic draining it.
func parseSearch(values url.Values) (searchParams, string) {
	p := searchParams{q: values.Get("q")}

	if p.q == "" {
		return p, "query parameter 'q' is required"
	}
	if utf8.RuneCountInString(p.q) > maxQueryLen {
		return p, "query parameter 'q' is too long"
	}

	limit, ok := queryInt(values, "limit", defaultLimit, 1, maxLimit)
	if !ok {
		return p, "query parameter 'limit' must be between 1 and " + strconv.Itoa(maxLimit)
	}
	p.limit = limit

	switch values.Get("mode") {
	case "", index.RankKeyword:
		p.rank = index.RankKeyword
	case index.RankSemantic:
		p.rank = index.RankSemantic
	default:
		return p, "query parameter 'mode' must be 'semantic' or 'keyword'"
	}

	switch origin := values.Get("origin"); origin {
	case "", article.OriginFeed, article.OriginSitemap:
		p.origin = origin
	default:
		return p, "query parameter 'origin' must be 'feed' or 'sitemap'"
	}

	// Last, because how deep paging may go follows from both the ranking mode and the
	// page size, and neither is settled until the checks above have run.
	maxOffset := maxOffsetFor(p.rank, p.limit)
	offset, ok := queryInt(values, "offset", 0, 0, maxOffset)
	if !ok {
		return p, offsetError(p.rank, p.limit, maxOffset)
	}
	p.offset = offset

	return p, ""
}

// offsetError explains a rejected offset.
//
// Semantic ranking earns the longer sentence because its limit moves with the page
// size: a caller who asked for a large page is told "between 0 and 0" and has no
// way to guess that the page size is what shrank it.
func offsetError(rank string, limit, maxOffset int) string {
	msg := "query parameter 'offset' must be between 0 and " + strconv.Itoa(maxOffset)
	if rank == index.RankSemantic {
		msg += " when 'limit' is " + strconv.Itoa(limit) +
			", because semantic ranking only orders the first " +
			strconv.Itoa(semanticWindow) + " matches"
	}
	return msg
}

// Search handles GET /api/search?q=...&limit=...&offset=...&origin=...&mode=...
func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	started := time.Now()
	caller := clientKey(r)

	// Before any work, including validation: an execution costs money whatever the
	// request turns out to say.
	if ok, wait := h.perClient.allow(caller, started); !ok {
		h.logThrottled(ctx, "caller", caller, wait, started)
		writeRateLimited(ctx, w, h.limits.PerMinute, wait)
		return
	}

	// Second, so that a caller already over their own limit is stopped by their own
	// bucket rather than by the one everybody shares — the same order, and for the
	// same reason, as the two reranking budgets in allowSemantic. Reaching this one
	// means the instance as a whole is over, which no amount of per-caller limiting
	// would have caught: a flood arrives from many addresses, each of them polite.
	if ok, wait := h.all.allow(globalKey, started); !ok {
		h.logThrottled(ctx, "service", caller, wait, started)
		writeRateLimited(ctx, w, h.limits.AllPerMinute, wait)
		return
	}

	p, invalid := parseSearch(r.URL.Query())
	if invalid != "" {
		writeError(ctx, w, http.StatusBadRequest, invalid)
		return
	}

	// Reranking is the metered resource, so it carries its own tighter allowance,
	// per caller and across the service. Exhausting it downgrades the query to
	// keyword ranking rather than refusing it — the same trade the index client
	// already makes when the reranker is unavailable, because worse ranking is a
	// disappointment and no search at all is an outage.
	//
	// Unlike the limiter above, this one is spent only once the request is known to
	// be answerable. A 400 never reaches the reranker, so charging it for one would
	// let malformed traffic drain a budget every caller shares. Validation having
	// already run also means the depth a caller was allowed still follows the mode
	// they asked for rather than the one throttling gave them.
	//
	// Recorded as a field on the one search line below rather than logged on its own,
	// so a single query is a single record.
	downgraded := false
	if p.rank == index.RankSemantic && !h.allowSemantic(caller, started) {
		p.rank, downgraded = index.RankKeyword, true
	}

	page, err := h.index.Query(ctx, p.q, index.QueryOptions{
		Limit:  p.limit,
		Offset: p.offset,
		Fetch:  fetchFor(p.rank, p.limit, p.offset),
		Origin: p.origin,
		Rank:   p.rank,
	})
	if err != nil {
		slog.ErrorContext(ctx, "search failed",
			"query", logQuery(p.q),
			"duration_ms", time.Since(started).Milliseconds(),
			"error", err)
		writeError(ctx, w, http.StatusInternalServerError, "search failed")
		return
	}

	// One line per search, whatever the outcome. Query volume, latency and how many
	// results came back are the entire operational picture for a search engine, and
	// none of it was visible while only failures were logged: a corpus that quietly
	// stopped matching anything looked exactly like a quiet day.
	slog.InfoContext(ctx, "search",
		"query", logQuery(p.q),
		"rank", p.rank,
		"origin", p.origin,
		"offset", p.offset,
		"count", len(page.Results),
		"total", page.Total,
		"read", page.Read,
		"downgraded", downgraded,
		"duration_ms", time.Since(started).Milliseconds())

	// Only on the answer. An error is about this moment rather than about the query,
	// so caching one would keep serving a failure the service has already recovered
	// from.
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(searchMaxAge))

	writeJSON(ctx, w, http.StatusOK, searchResponse{
		Query:      p.q,
		Count:      len(page.Results),
		Total:      page.Total,
		Offset:     p.offset,
		NextOffset: page.NextOffset,
		Exhausted:  page.Exhausted,
		Results:    page.Results,
	})
}

// queryInt reads an optional integer parameter, reporting false when the value is
// present but outside [minimum, maximum].
//
// Takes the parsed values rather than the request because a handler reads several
// of these, and each url.URL.Query() call re-parses the whole query string.
func queryInt(values url.Values, name string, fallback, minimum, maximum int) (int, bool) {
	raw := values.Get(name)
	if raw == "" {
		return fallback, true
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < minimum || n > maximum {
		return 0, false
	}
	return n, true
}

// Health handles GET /api/health.
//
// Answers whether this instance can serve a search, not merely whether the worker
// started. The deploy workflow gates on this, so a check that only proved the
// process was up would pass an environment whose search credential or role
// assignment never arrived — and the first sign of that would be every query
// failing.
//
// Success is deliberately not logged: this is polled, and one line per poll would
// bury everything worth reading. A failure is logged, because by then something is
// wrong that nobody has noticed yet.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// A cached "ok" would outlive the condition it describes, which for the one
	// endpoint whose whole job is to be current is worse than no answer at all.
	w.Header().Set("Cache-Control", "no-store")

	if err := h.index.Ready(ctx); err != nil {
		slog.ErrorContext(ctx, "health check failed", "error", err)
		writeError(ctx, w, http.StatusServiceUnavailable, "search index unreachable")
		return
	}

	writeJSON(ctx, w, http.StatusOK, map[string]string{"status": "ok"})
}

// The context is carried purely so a failed write is still attributable to the
// invocation that was serving it.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Usually a caller that hung up mid-response, which is their business rather
		// than a fault of ours — hence Warn.
		slog.WarnContext(ctx, "write response failed", "error", err)
	}
}

func writeError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	writeJSON(ctx, w, status, map[string]string{"error": msg})
}
