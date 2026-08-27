package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/index"
)

const emptyResult = `{"@odata.count":0,"value":[]}`

// newCapturingHandlers stands in for Azure AI Search, recording what was asked of it.
// The second result reports the requests that reached the index, which is where the
// paging and ranking decisions actually show up, since a status code cannot tell a
// reranked page from a keyword one.
func newCapturingHandlers(t *testing.T, semantic, body string, limits Limits) (*Handlers, func() []map[string]any) {
	t.Helper()

	var mu sync.Mutex
	var sent []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode search request: %v", err)
		}
		mu.Lock()
		sent = append(sent, request)
		mu.Unlock()
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return New(index.New(srv.URL, "articles", "test-key", semantic), limits), func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}
}

func newTestHandlers(t *testing.T, body string) *Handlers {
	t.Helper()

	h, _ := newCapturingHandlers(t, "", body, DefaultLimits())
	return h
}

// newHandlersReturning stands in for a search service that answers everything with
// one status, which is how an index we cannot read looks from here whatever the
// underlying reason.
func newHandlersReturning(t *testing.T, status int) *Handlers {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	return New(index.New(srv.URL, "articles", "test-key", ""), DefaultLimits())
}

func get(t *testing.T, h *Handlers, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// window builds n distinct documents, each from its own source so the cap never fires.
func window(n int) string {
	docs := make([]string, n)
	for i := range n {
		docs[i] = fmt.Sprintf(
			`{"url":"https://example.com/%03d","title":"Post %03d","sourceId":"s%d"}`, i, i, i)
	}
	return strings.Join(docs, ",")
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

// Total counts documents and the per-source cap drops rows, so a client that has
// paged to the end still holds fewer rows than Total. It cannot infer that from the
// numbers alone, which is what left "load more" dead beside "26 of 27", so the
// answer has to carry the flag, under the name the browser reads.
func TestSearchReportsExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"short window", `{"@odata.count":137,"value":[{"url":"https://example.com/a","title":"A"}]}`, true},
		{"full window", fmt.Sprintf(`{"@odata.count":137,"value":[%s]}`, window(60)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(t, newTestHandlers(t, tc.body), "/api/search?q=go")
			if rec.Code != http.StatusOK {
				t.Fatalf("got status %d, want 200", rec.Code)
			}

			var body struct {
				Exhausted bool `json:"exhausted"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Exhausted != tc.want {
				t.Errorf("exhausted = %v, want %v", body.Exhausted, tc.want)
			}
		})
	}
}

func TestSearchRejectsBadPaging(t *testing.T) {
	h := newTestHandlers(t, emptyResult)

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
	h := newTestHandlers(t, emptyResult)

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

// How deep semantic paging may go depends on the page size as well as the mode,
// because it is the *last* page that has to land inside the reranked window. A limit
// derived from a fixed page size let `limit=50&offset=30` read documents 30 to 80 of
// a 50-document window, quietly reverting the tail of the page to keyword ordering.
//
// Asserted against the request that reaches the index rather than the status code,
// since that reversion is exactly the failure a 200 cannot show.
func TestSearchSemanticPageStaysInsideRerankedWindow(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/api/search?q=go&mode=semantic&limit=20&offset=30", http.StatusOK},
		{"/api/search?q=go&mode=semantic&limit=10&offset=40", http.StatusOK},
		{"/api/search?q=go&mode=semantic&limit=50&offset=0", http.StatusOK},
		// Each of these would read past the window.
		{"/api/search?q=go&mode=semantic&limit=50&offset=1", http.StatusBadRequest},
		{"/api/search?q=go&mode=semantic&limit=30&offset=21", http.StatusBadRequest},
		{"/api/search?q=go&mode=semantic&limit=20&offset=31", http.StatusBadRequest},
	} {
		h, sent := newCapturingHandlers(t, "blogme-semantic", emptyResult, DefaultLimits())

		if code := get(t, h, tc.target).Code; code != tc.want {
			t.Errorf("%s: got status %d, want %d", tc.target, code, tc.want)
			continue
		}
		if tc.want != http.StatusOK {
			continue
		}

		calls := sent()
		if len(calls) != 1 {
			t.Fatalf("%s: got %d index calls, want 1", tc.target, len(calls))
		}
		skip, top := calls[0]["skip"].(float64), calls[0]["top"].(float64)
		if skip+top > semanticWindow {
			t.Errorf("%s: reads documents %v..%v, past the %d-document reranked window",
				tc.target, skip, skip+top, semanticWindow)
		}
	}
}

// A refused request never reaches the reranker, so it must not spend from the
// reranking budget. That budget is shared by everyone on the instance, so letting
// malformed traffic drain it would silently downgrade every real semantic query
// behind it, for an hour on the default settings.
func TestSearchRefusedRequestKeepsSemanticBudget(t *testing.T) {
	limits := DefaultLimits()
	limits.SemanticHourBurst = 2 // The whole service's reranking allowance, for this test.

	h, sent := newCapturingHandlers(t, "blogme-semantic", emptyResult, limits)

	// More refused requests than the budget could survive, for three different reasons.
	for _, target := range []string{
		"/api/search?q=go&mode=semantic&origin=bogus",
		"/api/search?q=go&mode=semantic&origin=%27%20or%20true",
		"/api/search?q=go&mode=semantic&limit=99",
	} {
		if code := get(t, h, target).Code; code != http.StatusBadRequest {
			t.Fatalf("%s: got status %d, want 400", target, code)
		}
	}
	if calls := sent(); len(calls) != 0 {
		t.Fatalf("a refused request reached the index %d times", len(calls))
	}

	if code := get(t, h, "/api/search?q=go&mode=semantic").Code; code != http.StatusOK {
		t.Fatalf("got status %d, want 200", code)
	}

	calls := sent()
	if len(calls) != 1 {
		t.Fatalf("got %d index calls, want 1", len(calls))
	}
	if got := calls[0]["queryType"]; got != "semantic" {
		t.Errorf("got queryType %q, want semantic: the refused requests spent the shared reranking budget", got)
	}
}

// The cap counts characters, matching the limit the browser puts on the search box.
// Counted in bytes the same number refuses a query the client had already trimmed to
// size: at 171 characters of Japanese rather than 512.
func TestSearchQueryLengthCountsRunes(t *testing.T) {
	h := newTestHandlers(t, emptyResult)

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"ascii at the cap", strings.Repeat("a", maxQueryLen), http.StatusOK},
		{"ascii past the cap", strings.Repeat("a", maxQueryLen+1), http.StatusBadRequest},
		// Three bytes each, one UTF-16 unit each: the browser counts these as 512 too.
		{"multi-byte at the cap", strings.Repeat("漢", maxQueryLen), http.StatusOK},
		{"multi-byte past the cap", strings.Repeat("漢", maxQueryLen+1), http.StatusBadRequest},
		// Four bytes each, and two UTF-16 units each, so the browser stops at half.
		{"astral at the browser's cap", strings.Repeat("😀", maxQueryLen/2), http.StatusOK},
	} {
		target := "/api/search?" + url.Values{"q": {tc.query}}.Encode()
		if code := get(t, h, target).Code; code != tc.want {
			t.Errorf("%s: got status %d, want %d", tc.name, code, tc.want)
		}
	}
}

// The deploy workflow gates on health, so the one thing it must not do is pass
// while search is broken. The failures worth catching all authenticate correctly:
// a role assignment that was never granted still issues a token, and a misspelled
// index name is a valid request to somewhere that is not there, so health is
// asserted against what the index answers rather than against the process running.
func TestHealthFollowsWhetherTheIndexCanBeRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
		want  int
	}{
		{"index answers", http.StatusOK, http.StatusOK},
		{"role assignment missing", http.StatusForbidden, http.StatusServiceUnavailable},
		{"index name wrong", http.StatusNotFound, http.StatusServiceUnavailable},
		{"service unwell", http.StatusInternalServerError, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newHandlersReturning(t, tc.index).
				Health(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

			if rec.Code != tc.want {
				t.Errorf("got status %d, want %d", rec.Code, tc.want)
			}
			// A cached answer would outlive the condition it describes.
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("got Cache-Control %q, want no-store", got)
			}
		})
	}
}

// A page of results is worth reusing, so a reload or a shared link opened twice
// costs nothing. A failure is about this moment rather than about the query, and
// caching one would go on serving it after the service had recovered.
func TestSearchCachesAnAnswerAndNothingElse(t *testing.T) {
	const want = "public, max-age=120"

	if got := get(t, newTestHandlers(t, emptyResult), "/api/search?q=go").
		Header().Get("Cache-Control"); got != want {
		t.Errorf("an answer: got Cache-Control %q, want %q", got, want)
	}

	for _, tc := range []struct {
		name    string
		handler *Handlers
		target  string
	}{
		{"rejected", newTestHandlers(t, emptyResult), "/api/search?q=go&limit=99"},
		{"failed", newHandlersReturning(t, http.StatusInternalServerError), "/api/search?q=go"},
	} {
		if got := get(t, tc.handler, tc.target).Header().Get("Cache-Control"); got != "" {
			t.Errorf("a %s request set Cache-Control %q", tc.name, got)
		}
	}
}

// A page is thinned after the index has ranked it, so reading exactly a page's worth
// hands back whatever survives: three rows of twenty for a query one blog dominates.
// Reading further is what fills it, and the request is where that shows up.
func TestSearchReadsMoreDocumentsThanThePageHolds(t *testing.T) {
	h, sent := newCapturingHandlers(t, "", emptyResult, DefaultLimits())

	if code := get(t, h, "/api/search?q=go&limit=20").Code; code != http.StatusOK {
		t.Fatalf("got status %d, want 200", code)
	}

	requests := sent()
	if len(requests) != 1 {
		t.Fatalf("got %d index requests, want 1", len(requests))
	}
	if got, want := requests[0]["top"], float64(20*overFetch); got != want {
		t.Errorf("top = %v, want %v", got, want)
	}
}

// Reading past the reranked window would top the page up with rows the reranker never
// ordered, which is keyword ranking wearing a semantic label. A short page is the
// honest answer at the tail of that window.
func TestSearchSemanticReadStaysInsideTheRerankedWindow(t *testing.T) {
	h, sent := newCapturingHandlers(t, "blogme-semantic", emptyResult, DefaultLimits())

	// The deepest offset semantic ranking allows for this page size.
	offset := maxOffsetFor(index.RankSemantic, 20)
	if code := get(t, h, fmt.Sprintf("/api/search?q=go&limit=20&mode=semantic&offset=%d", offset)).Code; code != http.StatusOK {
		t.Fatalf("got status %d, want 200", code)
	}

	requests := sent()
	if len(requests) == 0 {
		t.Fatal("no index request was made")
	}
	if got, want := requests[0]["top"], float64(semanticWindow-offset); got != want {
		t.Errorf("top = %v, want %v: the read ran past the reranked window", got, want)
	}
	// Never below the page size, or the clamp would be shortening pages on its own.
	if requests[0]["top"].(float64) < 20 {
		t.Errorf("top = %v, want at least the page size", requests[0]["top"])
	}
}

// The client cannot work out where the next page starts, because the rows it received
// do not say how many documents were read to produce them.
func TestSearchReportsWhereTheNextPageStarts(t *testing.T) {
	// A full window, opening with four documents from one blog. The cap keeps three
	// of those and discards the fourth, so filling a 20-row page reads 21 documents.
	const window = 20 * overFetch
	docs := make([]string, window)
	for i := range window {
		source := fmt.Sprintf("quiet%d", i)
		if i < 4 {
			source = "loud"
		}
		docs[i] = fmt.Sprintf(
			`{"url":"https://example.com/%d","title":"Post","sourceId":%q}`, i, source)
	}
	h := newTestHandlers(t, `{"@odata.count":900,"value":[`+strings.Join(docs, ",")+`]}`)

	rec := get(t, h, "/api/search?q=go&limit=20&offset=100")
	var body searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Count != 20 {
		t.Fatalf("count = %d, want a full page of 20", body.Count)
	}
	if want := 100 + 21; body.NextOffset != want {
		t.Errorf("nextOffset = %d, want %d: rows returned are not documents read",
			body.NextOffset, want)
	}
}
