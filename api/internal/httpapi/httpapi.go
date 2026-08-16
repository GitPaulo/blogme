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
	// Deep paging costs the index more with every page and relevance is long gone by
	// this depth, so the tail is simply not offered.
	maxOffset = 1000
)

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

// Search handles GET /api/search?q=...&limit=...&offset=...
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

	offset, ok := queryInt(r, "offset", 0, 0, maxOffset)
	if !ok {
		writeError(w, http.StatusBadRequest, "query parameter 'offset' must be between 0 and "+strconv.Itoa(maxOffset))
		return
	}

	results, total, err := h.index.Query(r.Context(), q, limit, offset)
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
