// Package httpapi serves the read side of the service: a search endpoint and a
// health check, both anonymous. Throttling lives in ratelimit.go.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Counted in runes rather than bytes, to match the characters the browser's
	// search box counts. In bytes the same number rejects a query the client had
	// already judged short enough, three times sooner in Japanese than in English.
	maxQueryLen  = 512
	defaultLimit = 20
	maxLimit     = 50
	// Semantic reranking only reorders the top 50 keyword matches, so that window is
	// the whole result set worth offering in that mode. Past it the ordering reverts
	// to keyword scoring part-way down a scroll, which reads as results getting worse
	// for no reason.
	// https://learn.microsoft.com/azure/search/semantic-how-to-query-request
	semanticWindow = 50
	// Keyword ranking scores the whole result set, so it can page as deep as is worth
	// paying for. Relevance is long gone by this depth.
	maxKeywordOffset = 1000
	// Documents read per row a page is meant to hold. See fetchFor.
	overFetch = 3
	// How much of a query reaches the logs. A query is third-party input and
	// telemetry is billed by volume.
	maxLoggedQuery = 128
	// How long a browser may reuse a page of results. Repeats are ordinary (a reload,
	// a shared link, the back button) and each one avoided is an execution not
	// billed.
	//
	// Two minutes rather than one. Discovery runs hourly, so anything well inside that
	// cycle serves the same corpus the index would have answered from anyway, and a
	// minute was short enough to expire between a reader opening a result and coming
	// back for the next one. Against the queries on record it roughly doubles what the
	// browser can answer without asking, 8% of requests to 14%.
	searchMaxAge = 120
)

type Handlers struct {
	index *index.Index

	// The endpoint is anonymous by design, so these are the only bound on what one
	// caller can spend. See ratelimit.go for what each protects.
	limits         Limits
	perClient      *limiter
	all            *limiter
	semanticClient *limiter
	semanticAll    *limiter
	suggestClient  *limiter
	suggestAll     *limiter

	// Throttling is loud when it fires, but not once per refused request: see
	// throttleLogPerMinute. refused counts every refusal this instance has made and
	// rides on each line that gets past the gate.
	throttleLog *limiter
	refused     atomic.Int64

	// The same arrangement for completions that could not be fetched, kept separate so
	// neither kind of noise can crowd the other out. See logSuggestFailure.
	suggestLog    *limiter
	suggestFailed atomic.Int64
}

func New(idx *index.Index, limits Limits) *Handlers {
	return &Handlers{
		index:          idx,
		limits:         limits,
		perClient:      newLimiter(float64(limits.PerMinute), limits.Burst),
		all:            newLimiter(float64(limits.AllPerMinute), limits.AllBurst),
		semanticClient: newLimiter(float64(limits.SemanticPerMinute), limits.SemanticBurst),
		semanticAll:    newLimiter(float64(limits.SemanticPerHour)/60, limits.SemanticHourBurst),
		suggestClient:  newLimiter(float64(limits.SuggestPerMinute), limits.SuggestBurst),
		suggestAll:     newLimiter(float64(limits.SuggestAllPerMinute), limits.SuggestAllBurst),
		throttleLog:    newLimiter(throttleLogPerMinute, throttleLogBurst),
		suggestLog:     newLimiter(throttleLogPerMinute, throttleLogBurst),
	}
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

	// Second, so a caller already over their own limit is stopped by their own bucket
	// rather than by the shared one. Reaching this limit means the instance as a whole
	// is over, which per-caller limiting cannot catch: a flood arrives from many
	// addresses, each of them polite.
	if ok, wait := h.all.allow(globalKey, started); !ok {
		h.logThrottled(ctx, "service", caller, wait, started)
		writeRateLimited(ctx, w, h.limits.AllPerMinute, wait)
		return
	}

	p, err := parseSearch(r.URL.Query())
	if err != nil {
		writeError(ctx, w, http.StatusBadRequest, err.Error())
		return
	}

	// Reranking is the metered resource, so it carries its own tighter allowance.
	// Exhausting it downgrades the query to keyword ranking rather than refusing it,
	// the same trade index.Query makes when the reranker is unavailable.
	//
	// Spent only once the request is known to be answerable: a 400 never reaches the
	// reranker, so charging it for one would let malformed traffic drain a budget
	// every caller shares. Recorded as a field on the search line below rather than
	// logged on its own, so one query stays one record.
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

	// One line per search, whatever the outcome. Query volume, latency and result
	// counts are the whole operational picture, and none of it was visible while only
	// failures were logged: a corpus that stopped matching anything looked like a
	// quiet day.
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

	// Only on the answer. An error describes this moment rather than the query, so
	// caching one would keep serving a failure already recovered from.
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

// Health handles GET /api/health.
//
// Answers whether this instance can serve a search rather than whether the worker
// started. The deploy workflow gates on it, so a check that only proved the process
// was up would pass an environment whose search credential never arrived.
//
// Success is not logged: this is polled, and one line per poll would bury everything
// worth reading. A failure is, because by then something is wrong that nobody has
// noticed.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// A cached "ok" would outlive the condition it describes, which for the one
	// endpoint whose job is to be current is worse than no answer at all.
	w.Header().Set("Cache-Control", "no-store")

	if err := h.index.Ready(ctx); err != nil {
		slog.ErrorContext(ctx, "health check failed", "error", err)
		writeError(ctx, w, http.StatusServiceUnavailable, "search index unreachable")
		return
	}

	writeJSON(ctx, w, http.StatusOK, map[string]string{"status": "ok"})
}

type searchResponse struct {
	Query string `json:"query"`
	// Count is the size of this page; Total is how many matches exist in all.
	Count  int `json:"count"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	// NextOffset is the offset to ask for to continue from here. A client must use it
	// rather than adding its own page size, because the per-source cap drops rows
	// after ranking: a page is wider than the rows it returns. See index.Page.
	NextOffset int `json:"nextOffset"`
	// Exhausted says this page reached the end of the index. A client that has paged
	// to here holds every row there is, and holds fewer than Total, which counts the
	// documents the per-source cap dropped. See index.Page.Exhausted.
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

// parseSearch validates every query parameter. Its error is answered verbatim with a
// 400, so it is written for the caller rather than for a log.
//
// Separate from the handler because none of it spends anything: the reranking budget
// the handler spends afterwards is shared across the instance, and validating first
// is what stops malformed traffic draining it.
func parseSearch(values url.Values) (searchParams, error) {
	p := searchParams{q: values.Get("q")}

	if p.q == "" {
		return p, errors.New("query parameter 'q' is required")
	}
	if utf8.RuneCountInString(p.q) > maxQueryLen {
		return p, errors.New("query parameter 'q' is too long")
	}

	limit, ok := queryInt(values, "limit", defaultLimit, 1, maxLimit)
	if !ok {
		return p, fmt.Errorf("query parameter 'limit' must be between 1 and %d", maxLimit)
	}
	p.limit = limit

	switch values.Get("mode") {
	case "", index.RankKeyword:
		p.rank = index.RankKeyword
	case index.RankSemantic:
		p.rank = index.RankSemantic
	default:
		return p, errors.New("query parameter 'mode' must be 'semantic' or 'keyword'")
	}

	switch origin := values.Get("origin"); origin {
	case "", article.OriginFeed, article.OriginSitemap:
		p.origin = origin
	default:
		return p, errors.New("query parameter 'origin' must be 'feed' or 'sitemap'")
	}

	// Last, because how deep paging may go follows from both the ranking mode and the
	// page size, and neither is settled until the checks above have run.
	maxOffset := maxOffsetFor(p.rank, p.limit)
	offset, ok := queryInt(values, "offset", 0, 0, maxOffset)
	if !ok {
		return p, offsetError(p.rank, p.limit, maxOffset)
	}
	p.offset = offset

	return p, nil
}

// offsetError explains a rejected offset. Semantic ranking earns the longer sentence
// because its limit moves with the page size: a caller who asked for a large page is
// told "between 0 and 0" and cannot otherwise guess why.
func offsetError(rank string, limit, maxOffset int) error {
	if rank == index.RankSemantic {
		return fmt.Errorf(
			"query parameter 'offset' must be between 0 and %d when 'limit' is %d, "+
				"because semantic ranking only orders the first %d matches",
			maxOffset, limit, semanticWindow)
	}
	return fmt.Errorf("query parameter 'offset' must be between 0 and %d", maxOffset)
}

// queryInt reads an optional integer parameter, reporting false when the value is
// present but outside [minimum, maximum].
//
// Takes the parsed values rather than the request because a handler reads several of
// these, and each url.URL.Query() call re-parses the whole query string.
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

// maxOffsetFor reports how deep the given ranking mode may page when serving pages of
// the given size.
//
// The page size is part of the answer because the last page has to land inside the
// window the mode can order, and for semantic ranking that is the reranked window
// rather than the whole result set. Deriving the limit from a fixed page size instead
// lets a caller asking for a larger page read past the window and back into keyword
// ordering, which is what the window exists to prevent.
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
// back nearly empty. Measured worst case was 3 usable rows in the first 20 documents
// against 24 in the first 50, so three times covers it with room to spare.
func fetchFor(rank string, limit, offset int) int {
	fetch := limit * overFetch
	if rank == index.RankKeyword {
		return fetch
	}
	// Semantic ranking only orders the reranked window. Reading past it would top the
	// page up with rows the reranker never sorted, so the tail of that window returns
	// a short page instead.
	return min(fetch, max(semanticWindow-offset, 0))
}

// allowSemantic spends from the reranking budgets, the caller's own first. Order
// matters: checking the service-wide budget first would let a caller already over
// their own limit go on draining the allowance everyone shares.
func (h *Handlers) allowSemantic(caller string, now time.Time) bool {
	if ok, _ := h.semanticClient.allow(caller, now); !ok {
		return false
	}
	ok, _ := h.semanticAll.allow(globalKey, now)
	return ok
}

// logThrottled reports a refusal without letting the reporting become the flood.
//
// Warn, not Info: being throttled is either abuse worth looking at or a limit set too
// low, and both need someone to notice. scope says which limit was reached.
// refused_total is cumulative for the life of the instance, so the jump between two
// consecutive lines is the rate the flood was arriving at, and no refusal is lost to
// a burst that ended before the next line was due.
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

// logQuery makes a caller's query safe to log: control characters are folded to
// spaces so nothing can forge a line break and fake a second record, and the result
// is cut on a rune boundary so no character is split into mojibake.
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

// writeJSON carries the context purely so a failed write stays attributable to the
// invocation that was serving it.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Usually a caller that hung up mid-response, which is their business rather
		// than a fault of ours, hence Warn.
		slog.WarnContext(ctx, "write response failed", "error", err)
	}
}

func writeError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	writeJSON(ctx, w, status, map[string]string{"error": msg})
}
