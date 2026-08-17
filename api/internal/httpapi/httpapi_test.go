package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/index"
)

func newTestHandlers(t *testing.T, body string) *Handlers {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return New(index.New(srv.URL, "articles", "test-key", ""))
}

func get(t *testing.T, h *Handlers, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestSearchReportsPageAndTotal(t *testing.T) {
	h := newTestHandlers(t, `{"@odata.count":137,"value":[{"url":"https://example.com/post","title":"A post"}]}`)

	rec := get(t, h, "/api/search?q=go&offset=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}

	var body searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Count != 1 || body.Total != 137 || body.Offset != 20 {
		t.Errorf("got count=%d total=%d offset=%d, want 1, 137 and 20", body.Count, body.Total, body.Offset)
	}
}

func TestSearchRejectsBadPaging(t *testing.T) {
	h := newTestHandlers(t, `{"@odata.count":0,"value":[]}`)

	for _, target := range []string{
		"/api/search?q=go&offset=-1",
		"/api/search?q=go&offset=100000",
		"/api/search?q=go&offset=abc",
		"/api/search?q=go&limit=0",
		"/api/search?q=go&limit=51",
		"/api/search?q=go&origin=everything",
		"/api/search?q=go&origin=%27%20or%20true",
	} {
		if code := get(t, h, target).Code; code != http.StatusBadRequest {
			t.Errorf("%s: got status %d, want 400", target, code)
		}
	}
}

// The ranking mode decides how deep paging may go: semantic can only offer the window
// its reranker actually reaches, while keyword ranking scores everything.
func TestSearchPagingDependsOnRankingMode(t *testing.T) {
	h := newTestHandlers(t, `{"@odata.count":0,"value":[]}`)

	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/api/search?q=go&mode=keyword", http.StatusOK},
		{"/api/search?q=go&mode=semantic", http.StatusOK},
		{"/api/search?q=go&mode=hybrid", http.StatusBadRequest},
		// Past the reranked window: fine for keyword (the default), refused for semantic.
		{"/api/search?q=go&offset=200&mode=keyword", http.StatusOK},
		{"/api/search?q=go&offset=200", http.StatusOK},
		{"/api/search?q=go&offset=200&mode=semantic", http.StatusBadRequest},
		// Beyond even the keyword tail.
		{"/api/search?q=go&offset=5000&mode=keyword", http.StatusBadRequest},
	} {
		if code := get(t, h, tc.target).Code; code != tc.want {
			t.Errorf("%s: got status %d, want %d", tc.target, code, tc.want)
		}
	}
}

func TestSearchAcceptsKnownOrigins(t *testing.T) {
	h := newTestHandlers(t, `{"@odata.count":0,"value":[]}`)

	for _, target := range []string{
		"/api/search?q=go",
		"/api/search?q=go&origin=feed",
		"/api/search?q=go&origin=sitemap",
	} {
		if code := get(t, h, target).Code; code != http.StatusOK {
			t.Errorf("%s: got status %d, want 200", target, code)
		}
	}
}
