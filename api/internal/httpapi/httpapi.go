package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

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
)

// maxOffsetFor reports how deep the given ranking mode is allowed to page.
func maxOffsetFor(rank string) int {
	if rank == index.RankKeyword {
		return maxKeywordOffset
	}
	return maxSemanticOffset
}

type Handlers struct {
	index *index.Index
}

func New(idx *index.Index) *Handlers {
	return &Handlers{index: idx}
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
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}
	if len(q) > maxQueryLen {
		writeError(w, http.StatusBadRequest, "query parameter 'q' is too long")
		return
	}

	limit, ok := queryInt(r, "limit", defaultLimit, 1, maxLimit)
	if !ok {
		writeError(w, http.StatusBadRequest, "query parameter 'limit' must be between 1 and "+strconv.Itoa(maxLimit))
		return
	}

	// The ranking mode decides how deep paging may go, so it is read before the offset.
	rank := index.RankSemantic
	switch r.URL.Query().Get("mode") {
	case "", index.RankSemantic:
	case index.RankKeyword:
		rank = index.RankKeyword
	default:
		writeError(w, http.StatusBadRequest, "query parameter 'mode' must be 'semantic' or 'keyword'")
		return
	}

	maxOffset := maxOffsetFor(rank)
	offset, ok := queryInt(r, "offset", 0, 0, maxOffset)
	if !ok {
		writeError(w, http.StatusBadRequest, "query parameter 'offset' must be between 0 and "+strconv.Itoa(maxOffset))
		return
	}

	origin := r.URL.Query().Get("origin")
	if origin != "" && origin != article.OriginFeed && origin != article.OriginSitemap {
		writeError(w, http.StatusBadRequest, "query parameter 'origin' must be 'feed' or 'sitemap'")
		return
	}

	results, total, err := h.index.Query(r.Context(), q, index.QueryOptions{
		Limit:  limit,
		Offset: offset,
		Origin: origin,
		Rank:   rank,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "search failed", "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	writeJSON(w, http.StatusOK, searchResponse{
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
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write response failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
