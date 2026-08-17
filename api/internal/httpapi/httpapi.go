package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/index"
)

const (
	maxQueryLen  = 512
	defaultLimit = 20
	maxLimit     = 50
	// Semantic reranking only reorders the top 50 keyword matches, so that window is
	// the entire result set worth offering in that mode: past it the ordering
	// silently reverts to keyword scoring part-way down a scroll, which reads as the
	// results getting worse for no reason.
	semanticWindow    = 50
	maxSemanticOffset = semanticWindow - defaultLimit
	// Keyword ranking scores the whole result set, so it can page as deep as is worth
	// paying for. Relevance is long gone by this depth, so the tail stops here.
	maxKeywordOffset = 1000
	// How much of a query reaches the logs. Knowing what was searched for is the
	// difference between "search is slow" and "this search is slow", but a query is
	// third-party input and the telemetry bill is charged by volume.
	maxLoggedQuery = 128
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

// maxOffsetFor reports how deep the given ranking mode is allowed to page.
func maxOffsetFor(rank string) int {
	if rank == index.RankKeyword {
		return maxKeywordOffset
	}
	return maxSemanticOffset
}

type Handlers struct {
	index *index.Index

	// The endpoint is anonymous by design, so these are the only bound on what a
	// single caller can spend. See ratelimit.go for what each one protects.
	limits         Limits
	perClient      *limiter
	semanticClient *limiter
	semanticAll    *limiter
}

func New(idx *index.Index, limits Limits) *Handlers {
	return &Handlers{
		index:          idx,
		limits:         limits,
		perClient:      newLimiter(float64(limits.PerMinute), limits.Burst),
		semanticClient: newLimiter(float64(limits.SemanticPerMinute), limits.SemanticBurst),
		semanticAll:    newLimiter(float64(limits.SemanticPerHour)/60, limits.SemanticHourBurst),
	}
}

type searchResponse struct {
	Query string `json:"query"`
	// Count is the size of this page; Total is how many matches exist in all.
	Count   int              `json:"count"`
	Total   int              `json:"total"`
	Offset  int              `json:"offset"`
	Results []article.Result `json:"results"`
}

// Search handles GET /api/search?q=...&limit=...&offset=...&origin=...
func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	started := time.Now()
	caller := clientKey(r)

	// Before any work, including validation: an execution costs money whatever the
	// request turns out to say.
	if ok, wait := h.perClient.allow(caller, started); !ok {
		// Warn, not Info: being throttled is either abuse worth looking at or a limit
		// set too low, and both need someone to notice.
		slog.WarnContext(ctx, "search throttled",
			"caller", caller, "retry_after_s", int(wait.Seconds())+1)
		writeRateLimited(ctx, w, h.limits.PerMinute, wait)
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(ctx, w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}
	if len(q) > maxQueryLen {
		writeError(ctx, w, http.StatusBadRequest, "query parameter 'q' is too long")
		return
	}

	limit, ok := queryInt(r, "limit", defaultLimit, 1, maxLimit)
	if !ok {
		writeError(ctx, w, http.StatusBadRequest, "query parameter 'limit' must be between 1 and "+strconv.Itoa(maxLimit))
		return
	}

	// The ranking mode decides how deep paging may go, so it is read before the offset.
	rank := index.RankKeyword
	switch r.URL.Query().Get("mode") {
	case "", index.RankKeyword:
	case index.RankSemantic:
		rank = index.RankSemantic
	default:
		writeError(ctx, w, http.StatusBadRequest, "query parameter 'mode' must be 'semantic' or 'keyword'")
		return
	}

	maxOffset := maxOffsetFor(rank)
	offset, ok := queryInt(r, "offset", 0, 0, maxOffset)
	if !ok {
		writeError(ctx, w, http.StatusBadRequest, "query parameter 'offset' must be between 0 and "+strconv.Itoa(maxOffset))
		return
	}

	// Reranking is the metered resource, so it carries its own tighter allowance,
	// per caller and across the service. Exhausting it downgrades the query to
	// keyword ranking rather than refusing it — the same trade the index client
	// already makes when the reranker is unavailable, because worse ranking is a
	// disappointment and no search at all is an outage.
	//
	// Deliberately after the offset check, so the depth a caller is allowed still
	// follows the mode they asked for rather than the one throttling gave them.
	//
	// Recorded as a field on the one search line below rather than logged on its own,
	// so a single query is a single record.
	downgraded := false
	if rank == index.RankSemantic && !h.allowSemantic(caller, started) {
		rank, downgraded = index.RankKeyword, true
	}

	origin := r.URL.Query().Get("origin")
	if origin != "" && origin != article.OriginFeed && origin != article.OriginSitemap {
		writeError(ctx, w, http.StatusBadRequest, "query parameter 'origin' must be 'feed' or 'sitemap'")
		return
	}

	results, total, err := h.index.Query(ctx, q, index.QueryOptions{
		Limit:  limit,
		Offset: offset,
		Origin: origin,
		Rank:   rank,
	})
	if err != nil {
		slog.ErrorContext(ctx, "search failed",
			"query", logQuery(q),
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
		"query", logQuery(q),
		"rank", rank,
		"origin", origin,
		"offset", offset,
		"count", len(results),
		"total", total,
		"downgraded", downgraded,
		"duration_ms", time.Since(started).Milliseconds())

	writeJSON(ctx, w, http.StatusOK, searchResponse{
		Query:   q,
		Count:   len(results),
		Total:   total,
		Offset:  offset,
		Results: results,
	})
}

// queryInt reads an optional integer parameter, reporting false when the value is
// present but outside [minimum, maximum].
func queryInt(r *http.Request, name string, fallback, minimum, maximum int) (int, bool) {
	raw := r.URL.Query().Get(name)
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
// Deliberately not logged: a platform probe calls this constantly, and one line
// per probe would bury everything worth reading.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
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
