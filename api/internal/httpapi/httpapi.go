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
)

type Handlers struct {
	index *index.Index
}

func New(idx *index.Index) *Handlers {
	return &Handlers{index: idx}
}

type searchResponse struct {
	Query   string           `json:"query"`
	Count   int              `json:"count"`
	Results []article.Result `json:"results"`
}

// Search handles GET /api/search?q=...&limit=...
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

	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			writeError(w, http.StatusBadRequest, "query parameter 'limit' must be between 1 and "+strconv.Itoa(maxLimit))
			return
		}
		limit = n
	}

	results, err := h.index.Query(r.Context(), q, limit)
	if err != nil {
		slog.ErrorContext(r.Context(), "search failed", "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	writeJSON(w, http.StatusOK, searchResponse{Query: q, Count: len(results), Results: results})
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
