package index

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
)

// query runs a search that is expected to succeed. Every test in this file is about
// what comes back rather than about failing, so the error check belongs in one place.
func query(t *testing.T, idx *Index, q string, opts QueryOptions) Page {
	t.Helper()

	page, err := idx.Query(context.Background(), q, opts)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return page
}

// Paging is what the "load more" control rides on, so the request has to carry the
// offset and ask for the corpus-wide count rather than the page size.
func TestQueryRequestsPageAndTotal(t *testing.T) {
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{
			"@odata.count": 137,
			"value": [{
				"@search.score": 1.5,
				"url": "https://example.com/post",
				"title": "A post",
				"publishedAt": "2024-05-01T09:30:00Z"
			}]
		}`)
	}))
	defer srv.Close()

	page := query(t, New(srv.URL, "articles", "test-key", ""), "go",
		QueryOptions{Limit: 20, Offset: 40})
	results, total := page.Results, page.Total

	if sent["top"] != float64(20) || sent["skip"] != float64(40) {
		t.Errorf("got top=%v skip=%v, want 20 and 40", sent["top"], sent["skip"])
	}
	if sent["count"] != true {
		t.Errorf("got count=%v, want true", sent["count"])
	}
	if _, ok := sent["filter"]; ok {
		t.Errorf("got filter=%v, want none when no origin is requested", sent["filter"])
	}
	if total != 137 {
		t.Errorf("got total %d, want 137", total)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	want := time.Date(2024, time.May, 1, 9, 30, 0, 0, time.UTC)
	if !results[0].PublishedAt.Equal(want) {
		t.Errorf("got publishedAt %v, want %v", results[0].PublishedAt, want)
	}
}

// A feed without a usable date leaves the field empty, which must stay a zero time
// rather than becoming an epoch date on the result card.
func TestQueryLeavesMissingDateZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"@odata.count":1,"value":[{"url":"https://example.com/post","title":"A post"}]}`)
	}))
	defer srv.Close()

	results := query(t, New(srv.URL, "articles", "test-key", ""), "go",
		QueryOptions{Limit: 20}).Results

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if !results[0].PublishedAt.IsZero() {
		t.Errorf("got publishedAt %v, want zero", results[0].PublishedAt)
	}
}

// Reranking is requested by naming the index's semantic configuration. searchMode
// stays "any" on purpose: wide keyword recall is what feeds the reranker.
func TestQueryRequestsSemanticRanking(t *testing.T) {
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"@odata.count":1,"value":[{"url":"https://example.com/p","title":"A post"}]}`)
	}))
	defer srv.Close()

	query(t, New(srv.URL, "articles", "test-key", "blogme-semantic"),
		"scaling single threaded servers", QueryOptions{Limit: 20})

	if sent["queryType"] != "semantic" {
		t.Errorf("queryType = %v, want semantic", sent["queryType"])
	}
	if sent["semanticConfiguration"] != "blogme-semantic" {
		t.Errorf("semanticConfiguration = %v", sent["semanticConfiguration"])
	}
	// The reranker orders the top fifty candidates whichever mode picked them, so it
	// is better served by fifty that contain the whole query than by fifty drawn from
	// everything containing any word of it.
	if sent["searchMode"] != "all" {
		t.Errorf("searchMode = %v, want all", sent["searchMode"])
	}
}

// Keyword ranking is the deliberate opt-out, so a configured index must still send a
// plain query when it is asked for.
func TestQueryKeywordRankSkipsSemantic(t *testing.T) {
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"@odata.count":1,"value":[{"url":"https://example.com/p","title":"A post"}]}`)
	}))
	defer srv.Close()

	query(t, New(srv.URL, "articles", "test-key", "blogme-semantic"), "go",
		QueryOptions{Limit: 20, Rank: RankKeyword})

	if sent["queryType"] != "simple" {
		t.Errorf("queryType = %v, want simple", sent["queryType"])
	}
	if _, ok := sent["semanticConfiguration"]; ok {
		t.Error("semanticConfiguration was sent for a keyword-ranked query")
	}
}

// The free plan stops at 1,000 queries a month and the service sheds load under
// concurrency. Losing the reranker must degrade ranking, not take search down.
func TestQueryFallsBackWhenSemanticFails(t *testing.T) {
	var attempts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		queryType, _ := body["queryType"].(string)
		attempts = append(attempts, queryType)

		if queryType == "semantic" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"semantic quota exceeded"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"@odata.count":7,"value":[{"url":"https://example.com/p","title":"A post"}]}`)
	}))
	defer srv.Close()

	page := query(t, New(srv.URL, "articles", "test-key", "blogme-semantic"), "go",
		QueryOptions{Limit: 20})
	res, total := page.Results, page.Total

	if want := []string{"semantic", "simple"}; !slices.Equal(attempts, want) {
		t.Errorf("attempts = %v, want %v", attempts, want)
	}
	if total != 7 || len(res) != 1 {
		t.Errorf("got total=%d results=%d, want 7 and 1", total, len(res))
	}
}

// The filter is built from a fixed set, so a query parameter can never inject one.
// Documents predating the origin field came from feeds and must stay reachable.
func TestQueryFiltersByOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   string
	}{
		{article.OriginSitemap, "origin eq 'sitemap'"},
		{article.OriginFeed, "origin eq 'feed' or origin eq null"},
		{"' or true or '", ""},
	} {
		var sent map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{"@odata.count":0,"value":[]}`)
		}))

		query(t, New(srv.URL, "articles", "test-key", ""), "go",
			QueryOptions{Limit: 20, Origin: tc.origin})
		srv.Close()

		got, _ := sent["filter"].(string)
		if got != tc.want {
			t.Errorf("origin %q: filter = %q, want %q", tc.origin, got, tc.want)
		}
	}
}

// Health is the deploy workflow's gate, so a Ready that asks the wrong question
// would fail every deploy — or, worse, pass one it should not. Asserted against the
// request that leaves rather than the error that comes back, because a stand-in
// server answers any path and would hide a wrong one.
func TestReadyAsksTheIndexForACount(t *testing.T) {
	var (
		method string
		path   string
		body   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "75356")
	}))
	defer srv.Close()

	if err := New(srv.URL, "articles", "test-key", "").Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}

	if method != http.MethodGet {
		t.Errorf("got method %s, want GET", method)
	}
	if want := "/indexes/articles/docs/$count"; path != want {
		t.Errorf("got path %q, want %q", path, want)
	}
	// A GET carrying "null" is a request the service is entitled to refuse.
	if len(body) != 0 {
		t.Errorf("got a request body %q, want none", body)
	}
}

// Counting is not a search, so it must not be turned into one by the semantic
// configuration being set: reranking is metered and a polled endpoint would drain
// the month's allowance.
func TestReadyDoesNotRerank(t *testing.T) {
	var query string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, "0")
	}))
	defer srv.Close()

	if err := New(srv.URL, "articles", "test-key", "blogme-semantic").Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if strings.Contains(query, "semantic") {
		t.Errorf("got query %q, want no semantic parameters", query)
	}
}

// A reranker that hangs must not take the search down with it. The budget is per
// request rather than shared across the pair, so the keyword fallback still gets a
// whole one; sharing a deadline would turn every slow semantic query into a failure,
// which is precisely what falling back exists to avoid.
func TestQueryFallsBackWhenSemanticHangs(t *testing.T) {
	queryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { queryTimeout = 5 * time.Second })

	var semanticCalls, keywordCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		if request["queryType"] == "semantic" {
			semanticCalls++
			// Longer than the budget, and longer than the fallback will need.
			time.Sleep(200 * time.Millisecond)
			return
		}

		keywordCalls++
		_, _ = io.WriteString(w, `{
			"@odata.count": 1,
			"value": [{"url": "https://example.com/post", "title": "A post"}]
		}`)
	}))
	defer srv.Close()

	page := query(t, New(srv.URL, "articles", "test-key", "blogme-semantic"), "go",
		QueryOptions{Limit: 20})
	results, total := page.Results, page.Total

	if semanticCalls != 1 || keywordCalls != 1 {
		t.Errorf("semantic calls = %d, keyword calls = %d; want 1 and 1", semanticCalls, keywordCalls)
	}
	if total != 1 || len(results) != 1 {
		t.Fatalf("total = %d, results = %d; want 1 and 1", total, len(results))
	}
}

// The budget bounds a single request, so a search that is merely slow still answers.
func TestQueryAnswersInsideTheBudget(t *testing.T) {
	queryTimeout = 500 * time.Millisecond
	t.Cleanup(func() { queryTimeout = 5 * time.Second })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"@odata.count": 1, "value": [{"url": "https://example.com/p", "title": "P"}]}`)
	}))
	defer srv.Close()

	if total := query(t, New(srv.URL, "articles", "test-key", ""), "go",
		QueryOptions{Limit: 20}).Total; total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
}

// dominatedBy builds a window whose first `run` documents all belong to one blog and
// whose remainder is one blog each, which is the shape of a query like "claude":
// live, its first twenty-nine matches were all the same site.
func dominatedBy(run, total int) []string {
	docs := make([]string, total)
	for i := range total {
		source := "loud"
		if i >= run {
			source = fmt.Sprintf("quiet%d", i)
		}
		docs[i] = fmt.Sprintf(
			`{"url":"https://example.com/%03d","title":"Post %03d","sourceId":%q}`, i, i, source)
	}
	return docs
}

// windowServer answers like the index does: honour skip and top over a fixed corpus.
func windowServer(t *testing.T, docs []string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Skip int `json:"skip"`
			Top  int `json:"top"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		window := docs[min(body.Skip, len(docs)):min(body.Skip+body.Top, len(docs))]
		_, _ = io.WriteString(w, fmt.Sprintf(
			`{"@odata.count":%d,"value":[%s]}`, len(docs), strings.Join(window, ",")))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Reading exactly one page's worth is what made "claude" return three rows out of
// twenty. The rows were never missing — they sat just past where a page-sized read
// looks — so reading further is what fills the page without giving up the cap.
func TestQueryOverFetchesToFillAPageOneSourceDominates(t *testing.T) {
	const run = 29

	srv := windowServer(t, dominatedBy(run, 100))
	idx := New(srv.URL, "articles", "test-key", "")

	short := query(t, idx, "q", QueryOptions{Limit: 20})
	if len(short.Results) != maxPerSource {
		t.Fatalf("reading one page's worth gave %d rows, want %d — the fixture is not dominated",
			len(short.Results), maxPerSource)
	}

	full := query(t, idx, "q", QueryOptions{Limit: 20, Fetch: 60})
	if len(full.Results) != 20 {
		t.Errorf("got %d rows, want a full page of 20", len(full.Results))
	}

	// Filling the page must not be a way around the cap: the loud blog still holds
	// three rows of it and no more.
	loud := 0
	for _, r := range full.Results {
		var n int
		if _, err := fmt.Sscanf(r.URL, "https://example.com/%d", &n); err == nil && n < run {
			loud++
		}
	}
	if loud != maxPerSource {
		t.Errorf("the dominant blog took %d rows, want %d", loud, maxPerSource)
	}
}

// The whole point of NextOffset: rows returned and documents read are different
// numbers, and only the second one can drive paging.
func TestQueryNextOffsetCountsDocumentsReadNotRowsReturned(t *testing.T) {
	srv := windowServer(t, dominatedBy(29, 100))

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if page.NextOffset == len(page.Results) {
		t.Fatalf("NextOffset = %d, which is just the row count — the cap read further than that",
			page.NextOffset)
	}
	// Three of the first 29 survive, so 17 more are needed from the tail: 29+17 = 46.
	if want := 46; page.NextOffset != want {
		t.Errorf("NextOffset = %d, want %d", page.NextOffset, want)
	}
}

// Paging is the contract "load more" rides on. Walk a corpus one blog dominates by
// following NextOffset and account for every row: nothing seen twice, and nothing
// silently stepped over by a stride that assumed pages are as wide as they are long.
func TestQueryPagingFollowingNextOffsetLosesAndRepeatsNothing(t *testing.T) {
	const (
		corpus = 100
		limit  = 20
	)

	srv := windowServer(t, dominatedBy(29, corpus))
	idx := New(srv.URL, "articles", "test-key", "")

	seen := map[string]int{}
	pages := 0
	for offset := 0; offset < corpus; pages++ {
		if pages > corpus {
			t.Fatal("paging did not terminate")
		}

		page := query(t, idx, "q", QueryOptions{Limit: limit, Offset: offset, Fetch: limit * 3})
		for _, r := range page.Results {
			seen[r.URL]++
		}
		if page.NextOffset <= offset {
			t.Fatalf("offset %d: NextOffset = %d, which would page forever", offset, page.NextOffset)
		}
		offset = page.NextOffset
	}

	for i := range corpus {
		url := fmt.Sprintf("https://example.com/%03d", i)
		// The cap deliberately discards some of the dominant blog, so a row being
		// absent is allowed; being present twice never is.
		if seen[url] > 1 {
			t.Errorf("%s was returned %d times", url, seen[url])
		}
	}
	// Every quiet blog is past the run and inside the cap, so all of them must appear.
	for i := 29; i < corpus; i++ {
		url := fmt.Sprintf("https://example.com/%03d", i)
		if seen[url] == 0 {
			t.Errorf("%s was never returned by any page", url)
		}
	}
}

// A window shorter than the one asked for means the index has nothing left. Without
// saying so, a caller driven by NextOffset would keep asking for the same empty page.
func TestQueryNextOffsetStopsPagingWhenTheIndexRunsOut(t *testing.T) {
	srv := windowServer(t, dominatedBy(0, 5))

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if len(page.Results) != 5 {
		t.Fatalf("got %d rows, want 5", len(page.Results))
	}
	if page.NextOffset < page.Total {
		t.Errorf("NextOffset = %d with total %d, so paging would continue past the end",
			page.NextOffset, page.Total)
	}
}

// A document's key is its source and its URL together, so one article listed under
// two sources is two documents and can land on a page twice. The browser drops the
// repeat, which leaves the reader with a short page and a count that overstates it:
// live, "claude" returned twenty rows of which only seventeen were distinct.
func TestQueryDropsRepeatedURLsWithinAPage(t *testing.T) {
	docs := []string{
		`{"url":"https://example.com/a","title":"A","sourceId":"one"}`,
		`{"url":"https://example.com/a","title":"A","sourceId":"two"}`,
		`{"url":"https://example.com/a","title":"A","sourceId":"three"}`,
		`{"url":"https://example.com/b","title":"B","sourceId":"one"}`,
	}
	srv := windowServer(t, docs)

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if len(page.Results) != 2 {
		t.Fatalf("got %d rows, want 2 distinct urls", len(page.Results))
	}
	// Every document was still read, so paging steps over the repeats rather than
	// meeting them again on the next page.
	if page.Read != len(docs) {
		t.Errorf("read = %d, want %d", page.Read, len(docs))
	}
}

// A repeat must not spend one of its source's three rows, or a site listed twice
// would quietly get less of the page than a site listed once.
func TestQueryRepeatsDoNotSpendTheSourcesShare(t *testing.T) {
	var docs []string
	// The same article twice, then four more from the same source.
	docs = append(docs,
		`{"url":"https://example.com/a","title":"A","sourceId":"loud"}`,
		`{"url":"https://example.com/a","title":"A","sourceId":"loud"}`)
	for i := range 4 {
		docs = append(docs, fmt.Sprintf(
			`{"url":"https://example.com/%d","title":"P","sourceId":"loud"}`, i))
	}
	srv := windowServer(t, docs)

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if len(page.Results) != maxPerSource {
		t.Errorf("got %d rows, want %d — the repeat ate part of the source's share",
			len(page.Results), maxPerSource)
	}
}

// Every word of the query has to appear. Asking for any of them made the corpus look
// far larger than it was — "ai text watermarks" reported 185,796 matches where 265
// documents held all three words — and filled the tail of every result set with pages
// that happened to say "text".
func TestQueryRequiresEveryWordOfTheQuery(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"@odata.count":0,"value":[]}`)
	}))
	defer srv.Close()

	query(t, New(srv.URL, "articles", "test-key", ""), "ai text watermarks",
		QueryOptions{Limit: 20})

	if sent["searchMode"] != "all" {
		t.Errorf("searchMode = %v, want all", sent["searchMode"])
	}
}
