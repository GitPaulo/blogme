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
	"sync/atomic"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
)

const (
	emptyResultJSON = `{"@odata.count": 0, "value": []}`
	oneResultJSON   = `{"@odata.count": 1, "value": [{"url": "https://example.com/a", "title": "A post"}]}`
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

// The preview asks three questions of one field, so the projection has to keep null
// apart from false: null is a page whose headers nobody has read, false is one that
// was read and framed fine. Collapsing them would have the app stop previewing every
// document indexed before the crawler started looking.
func TestQueryCarriesTheFramingVerdict(t *testing.T) {
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{
			"@odata.count": 3,
			"value": [
				{"url": "https://a.example/1", "title": "Refused", "framingDenied": true},
				{"url": "https://b.example/2", "title": "Allowed", "framingDenied": false},
				{"url": "https://c.example/3", "title": "Unknown", "framingDenied": null}
			]
		}`)
	}))
	defer srv.Close()

	results := query(t, New(srv.URL, "articles", "test-key", ""), "go", QueryOptions{Limit: 20}).Results

	if selected, _ := sent["select"].(string); !strings.Contains(selected, "framingDenied") {
		t.Errorf("select = %q, want it to ask for framingDenied", selected)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	yes, no := true, false
	for i, want := range []*bool{&yes, &no, nil} {
		got := results[i].FramingDenied
		switch {
		case want == nil && got != nil:
			t.Errorf("result %d: got %v, want unknown", i, *got)
		case want != nil && got == nil:
			t.Errorf("result %d: got unknown, want %v", i, *want)
		case want != nil && got != nil && *got != *want:
			t.Errorf("result %d: got %v, want %v", i, *got, *want)
		}
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
// goes back to "any" on purpose: wide keyword recall is what feeds the reranker.
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
	// Requiring every word would settle the query before the reranker saw it, which
	// is how a sentence, the thing this mode is for, came to match nothing at all:
	// "why is my postgres query slow" had 0 candidates under "all" and 266,204 under
	// "any". The keyword body sets "all" for its own good reasons; inheriting it here
	// was the bug.
	if sent["searchMode"] != "any" {
		t.Errorf("searchMode = %v, want any", sent["searchMode"])
	}
}

// A reranked row is scored by the reranker. Azure keeps sending @search.score on a
// semantic query, but it is the keyword score of a list the reranker has already
// reordered, so reporting it hands back a number that disagrees with the position it
// sits in: live, the top of one page read 146.7, 59.7, 79.5, 68.9 downwards.
func TestQueryReportsTheScoreThatOrderedTheRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"@odata.count":2,"value":[
			{"url":"https://example.com/a","title":"A","@search.score":59.7,"@search.rerankerScore":2.98},
			{"url":"https://example.com/b","title":"B","@search.score":146.7,"@search.rerankerScore":2.82}
		]}`)
	}))
	defer srv.Close()

	page := query(t, New(srv.URL, "articles", "test-key", "blogme-semantic"), "q",
		QueryOptions{Limit: 20})

	got := []float64{page.Results[0].Score, page.Results[1].Score}
	if want := []float64{2.98, 2.82}; !slices.Equal(got, want) {
		t.Errorf("scores = %v, want %v: the keyword score was reported for a reranked row", got, want)
	}
	if got[0] < got[1] {
		t.Error("the first row scores below the second, so the score contradicts the order")
	}
}

// Without a reranker there is no second score, and the keyword one is the order.
func TestQueryReportsTheKeywordScoreWhenNothingReranked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w,
			`{"@odata.count":1,"value":[{"url":"https://example.com/a","title":"A","@search.score":12.5}]}`)
	}))
	defer srv.Close()

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q", QueryOptions{Limit: 20})

	if page.Results[0].Score != 12.5 {
		t.Errorf("score = %v, want 12.5", page.Results[0].Score)
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
	// The mode the semantic branch overrides has to survive being opted out of: this
	// is the one case where a configured index sends a keyword query, so it is the
	// one that catches the override leaking onto the wrong body.
	if sent["searchMode"] != "all" {
		t.Errorf("searchMode = %v, want all", sent["searchMode"])
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
// would fail every deploy, or worse pass one it should not. Asserted against the
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

// Startup warms the connection to the search service, and it does so only where the
// service actually runs: behind a managed identity. An index built with an API key is
// a test or a laptop, and there the warm-up would be a request nobody asked for,
// arriving at a stub server at a moment no test controls — which is a flake in every
// test that counts what it was sent, not a slow first search avoided.
//
// See warm, and TestQuerySendsTheEscapedTextOnEveryRequest, which asserts a request
// count this would otherwise raise by one.
func TestAnAPIKeyedIndexDoesNotWarmOnItsOwn(t *testing.T) {
	var requests atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "0")
	}))
	defer srv.Close()

	New(srv.URL, "articles", "test-key", "")

	// Warming is a goroutine, so an assertion made immediately would pass whether or
	// not one was started. Long enough for a local round trip many times over.
	time.Sleep(200 * time.Millisecond)

	if n := requests.Load(); n != 0 {
		t.Errorf("New made %d request(s) before anything was asked of it, want 0", n)
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
// twenty. The rows were never missing: they sat just past where a page-sized read
// looks, so reading further is what fills the page without giving up the cap.
func TestQueryOverFetchesToFillAPageOneSourceDominates(t *testing.T) {
	const run = 29

	srv := windowServer(t, dominatedBy(run, 100))
	idx := New(srv.URL, "articles", "test-key", "")

	short := query(t, idx, "q", QueryOptions{Limit: 20})
	if len(short.Results) != maxPerSource {
		t.Fatalf("reading one page's worth gave %d rows, want %d: the fixture is not dominated",
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
		t.Fatalf("NextOffset = %d, which is just the row count, but the cap read further",
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
	if !page.Exhausted {
		t.Error("Exhausted = false at the end of the index")
	}
}

// A page that filled from a full window has not reached the end, and saying it had
// would strand every result past it.
func TestQueryIsNotExhaustedWhileTheIndexHasMore(t *testing.T) {
	srv := windowServer(t, dominatedBy(0, 100))

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if page.Exhausted {
		t.Errorf("Exhausted = true with %d of %d documents read", page.Read, page.Total)
	}
}

// The count and the rows answer different questions, and the gap between them is the
// per-source cap: Total counts documents, including the ones the cap will discard.
// A caller told only "26 of 27" cannot tell a page it is missing from one that does
// not exist, which is what left the live "load more" dead beside a count that still
// promised another result. Exhausted is the difference, so it has to survive the case
// that produced the complaint: a last page whose rows fall short of the total.
func TestQueryReportsExhaustionEvenWhenRowsFallShortOfTheTotal(t *testing.T) {
	const corpus = 27

	// Five posts from one blog, as "photolithography" had: the cap keeps three.
	srv := windowServer(t, dominatedBy(5, corpus))
	idx := New(srv.URL, "articles", "test-key", "")

	page := query(t, idx, "q", QueryOptions{Limit: 50, Fetch: 150})

	if !page.Exhausted {
		t.Fatalf("Exhausted = false having read all %d documents", corpus)
	}
	if len(page.Results) >= page.Total {
		t.Fatalf("got %d rows of %d documents: the fixture is not capped, so it cannot show the gap",
			len(page.Results), page.Total)
	}
	// The point of the flag: the shortfall is the cap doing its job, not a page left
	// unread, and only the flag says which.
	if want := corpus - (5 - maxPerSource); len(page.Results) != want {
		t.Errorf("got %d rows, want %d", len(page.Results), want)
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
	// Distinct titles, because one source repeating a title is itself a repeat now.
	for i := range 4 {
		docs = append(docs, fmt.Sprintf(
			`{"url":"https://example.com/%d","title":"P%d","sourceId":"loud"}`, i, i))
	}
	srv := windowServer(t, docs)

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if len(page.Results) != maxPerSource {
		t.Errorf("got %d rows, want %d: the repeat ate part of the source's share",
			len(page.Results), maxPerSource)
	}
}

// Under keyword ranking every word of the query has to appear. Asking for any of them
// made the corpus look far larger than it was ("ai text watermarks" reported 185,796
// matches where 265 documents held all three words) and filled the tail of every
// result set with pages that happened to say "text". Semantic ranking wants the
// opposite; see TestQueryRequestsSemanticRanking.
func TestQueryKeywordRequiresEveryWordOfTheQuery(t *testing.T) {
	// The first ask, specifically. An empty answer is broadened to "any" afterwards, so
	// only recording the last request would test the fallback and call it the rule.
	var first map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent map[string]any
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if first == nil {
			first = sent
		}
		_, _ = io.WriteString(w, `{"@odata.count":0,"value":[]}`)
	}))
	defer srv.Close()

	query(t, New(srv.URL, "articles", "test-key", ""), "ai text watermarks",
		QueryOptions{Limit: 20})

	if first["searchMode"] != "all" {
		t.Errorf("searchMode = %v, want all", first["searchMode"])
	}
}

// Naming the fields is what keeps a query away from author and topics, which have no
// analyzer and so keep the stopwords the rest of the index drops. Left unsent, the
// service searches every searchable field and "a tour of go" comes back as two dozen
// bylines. Asserted on both rankings and on the broadened retry, because the poison is
// in the field set rather than in the mode: a request that dropped it anywhere would
// answer that one from the wrong fields.
func TestQueryNamesTheFieldsItMatchesOn(t *testing.T) {
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		sent = append(sent, body)
		// Empty, so the keyword branch also makes its broadened second ask.
		_, _ = io.WriteString(w, `{"@odata.count":0,"value":[]}`)
	}))
	defer srv.Close()

	idx := New(srv.URL, "articles", "test-key", "blogme-semantic")
	query(t, idx, "a tour of go", QueryOptions{Limit: 20})
	query(t, idx, "a tour of go", QueryOptions{Limit: 20, Rank: RankKeyword})

	if len(sent) != 3 {
		t.Fatalf("sent %d requests, want 3 (semantic, keyword, broadened)", len(sent))
	}
	for i, body := range sent {
		if body["searchFields"] != searchFields {
			t.Errorf("request %d searchFields = %v, want %q", i, body["searchFields"], searchFields)
		}
	}
}

// The index carries several scoring profiles that differ by one variable each, so
// that a change to ranking can be measured rather than argued about. A profile has to
// reach both rankings, or a comparison would only be testing one of them.
func TestQueryCarriesAScoringProfileIntoBothRankings(t *testing.T) {
	var sent []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		sent = append(sent, body)

		// Refusing the semantic attempt makes the run fall through to the keyword
		// query, so one call exercises both.
		if body["queryType"] == "semantic" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"@odata.count":1,"value":[]}`)
	}))
	defer srv.Close()

	query(t, New(srv.URL, "articles", "test-key", "blogme-semantic"), "go",
		QueryOptions{Limit: 20, Profile: "relevance-quality"})

	if len(sent) != 2 {
		t.Fatalf("sent %d queries, want the semantic attempt and its keyword fallback", len(sent))
	}
	for _, body := range sent {
		if body["scoringProfile"] != "relevance-quality" {
			t.Errorf("queryType %v carried scoringProfile %v, want the profile asked for",
				body["queryType"], body["scoringProfile"])
		}
	}
}

// Nothing in the request path chooses a profile, so the index's own default applies
// and a caller cannot pick how their results are ranked.
func TestQuerySendsNoProfileByDefault(t *testing.T) {
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"@odata.count":0,"value":[]}`)
	}))
	defer srv.Close()

	query(t, New(srv.URL, "articles", "test-key", ""), "go", QueryOptions{Limit: 20})

	if _, present := sent["scoringProfile"]; present {
		t.Errorf("scoringProfile = %v, want it absent", sent["scoringProfile"])
	}
}

// One site can publish a single article under several urls — a tutorial translated
// into five languages is five paths holding one page — and the url check cannot see
// it. Live, "opengl tutorial" returned "Tutorial 12 : OpenGL Extensions" three times:
// the source's whole allowance spent on one article.
func TestQueryDropsOneSourcesRepeatedTitles(t *testing.T) {
	docs := []string{
		`{"url":"https://gl.example/tutorial-12","title":"Tutorial 12 : OpenGL Extensions","sourceId":"gl"}`,
		`{"url":"https://gl.example/hu/tutorial-12","title":"Tutorial 12 : OpenGL Extensions","sourceId":"gl"}`,
		`{"url":"https://gl.example/ru/tutorial-12","title":"tutorial 12 :  opengl extensions","sourceId":"gl"}`,
		`{"url":"https://gl.example/tutorial-13","title":"Tutorial 13 : Normal Mapping","sourceId":"gl"}`,
	}
	srv := windowServer(t, docs)

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if len(page.Results) != 2 {
		t.Fatalf("got %d rows, want 2: the translations are one article", len(page.Results))
	}
	if page.Results[0].Title != "Tutorial 12 : OpenGL Extensions" {
		t.Errorf("kept %q, want the first of the repeats", page.Results[0].Title)
	}
	if page.Results[1].Title != "Tutorial 13 : Normal Mapping" {
		t.Errorf("second row is %q, want the source's other article", page.Results[1].Title)
	}
}

// Two blogs posting under one title have written two different articles, and titles
// as plain as "Shaders" are common. Collapsing across sources would hide real writing.
func TestQueryKeepsTheSameTitleFromDifferentSources(t *testing.T) {
	docs := []string{
		`{"url":"https://one.example/shaders","title":"Shaders","sourceId":"one"}`,
		`{"url":"https://two.example/shaders","title":"Shaders","sourceId":"two"}`,
		`{"url":"https://three.example/shaders","title":"Shaders","sourceId":"three"}`,
	}
	srv := windowServer(t, docs)

	page := query(t, New(srv.URL, "articles", "test-key", ""), "q",
		QueryOptions{Limit: 20, Fetch: 60})

	if len(page.Results) != len(docs) {
		t.Errorf("got %d rows, want %d: different blogs, different articles",
			len(page.Results), len(docs))
	}
}

// broadening stands in for a service that finds nothing for every word of a query and
// something for any of them, recording which mode each request asked for.
func broadeningService(t *testing.T, strict, loose string) (*Index, func() []string) {
	t.Helper()

	var modes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mode, _ := request["searchMode"].(string)
		modes = append(modes, mode)
		if mode == "all" {
			_, _ = io.WriteString(w, strict)
			return
		}
		_, _ = io.WriteString(w, loose)
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, "articles", "test-key", ""), func() []string { return slices.Clone(modes) }
}

// The index does not analyse every field the same way, so a query holding a word one
// half of it discarded can be satisfiable only through the other half — and match
// nothing. Asking again for any of the words is the floor under that.
func TestQueryBroadensWhenNothingMatchesEveryWord(t *testing.T) {
	idx, modes := broadeningService(t, emptyResultJSON, oneResultJSON)

	page := query(t, idx, "the world generation of minecraft", QueryOptions{Limit: 20})

	if want := []string{"all", "any"}; !slices.Equal(modes(), want) {
		t.Errorf("asked %v, want %v", modes(), want)
	}
	if len(page.Results) != 1 {
		t.Fatalf("got %d results, want the broadened one", len(page.Results))
	}
	if !page.Broadened {
		t.Error("the page does not say it answered a looser question than it was asked")
	}
}

// A search that works is never touched: broadening exists for the empty page alone.
func TestQueryDoesNotBroadenWhenTheStrictQueryAnswers(t *testing.T) {
	idx, modes := broadeningService(t, oneResultJSON, oneResultJSON)

	page := query(t, idx, "rust ownership rules", QueryOptions{Limit: 20})

	if want := []string{"all"}; !slices.Equal(modes(), want) {
		t.Errorf("asked %v, want only the strict query", modes())
	}
	if page.Broadened {
		t.Error("a page that matched every word claims it was broadened")
	}
}

// With one term the two modes ask the same question, so a second request would spend an
// execution to be told the same thing.
func TestQueryDoesNotBroadenASingleWord(t *testing.T) {
	idx, modes := broadeningService(t, emptyResultJSON, oneResultJSON)

	page := query(t, idx, "zzzqqq", QueryOptions{Limit: 20})

	if want := []string{"all"}; !slices.Equal(modes(), want) {
		t.Errorf("asked %v, want only the strict query", modes())
	}
	if len(page.Results) != 0 || page.Broadened {
		t.Error("a single word was broadened")
	}
}

// A failed retry must not turn an empty answer into a broken one.
func TestQueryKeepsTheStrictAnswerWhenBroadeningFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["searchMode"] == "all" {
			_, _ = io.WriteString(w, emptyResultJSON)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	page, err := New(srv.URL, "articles", "test-key", "").
		Query(context.Background(), "two words", QueryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("a failed retry failed the whole search: %v", err)
	}
	if len(page.Results) != 0 || page.Broadened {
		t.Error("got something other than the strict, empty answer")
	}
}

// The derived copies are written by a struct literal, so nothing but a test notices
// when one goes missing: an article indexed without authorText is not findable by its
// author's name, and one without titleSuggest completes nothing — both silently, and
// both only for the articles discovered after the omission.
func TestUpsertWritesTheDerivedCopies(t *testing.T) {
	var sent struct {
		Value []struct {
			Title          string `json:"title"`
			TitleSuggest   string `json:"titleSuggest"`
			Author         string `json:"author"`
			AuthorText     string `json:"authorText"`
			SuggestVersion int    `json:"suggestVersion"`
		} `json:"value"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "articles", "test-key", "").Upsert(context.Background(),
		[]article.Article{{ID: "a", URL: "https://example.com/a", Title: "A post", Author: "Ada"}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if len(sent.Value) != 1 {
		t.Fatalf("indexed %d documents, want 1", len(sent.Value))
	}
	doc := sent.Value[0]
	if doc.TitleSuggest != doc.Title {
		t.Errorf("titleSuggest = %q, want the title %q", doc.TitleSuggest, doc.Title)
	}
	if doc.AuthorText != doc.Author {
		t.Errorf("authorText = %q, want the author %q", doc.AuthorText, doc.Author)
	}
	// Without it the backfill cannot tell a document it has finished from one it has
	// never seen, so it would either redo the corpus or skip it.
	if doc.SuggestVersion != suggestVersion {
		t.Errorf("suggestVersion = %d, want %d", doc.SuggestVersion, suggestVersion)
	}
}

// A key and managed identity are alternatives, and which one is in play decides whether
// a request can reach the network before it has a token.
//
// Worth pinning both halves. The key path must not build a credential: doing so would
// start the startup fetch in every test and on every developer's machine, where there
// is no instance metadata service to answer and the only result is a delay and a log
// line. And the key has to actually be sent, or every request is anonymous.
func TestCredentialIsOnlyUsedWithoutAKey(t *testing.T) {
	keyed := New("https://example.search.windows.net", "articles", "test-key", "")
	if keyed.cred != nil {
		t.Error("a key was given and a managed identity credential was built anyway, " +
			"which starts a token fetch nothing is waiting for")
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.search.windows.net", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := keyed.authorize(context.Background(), req); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if got := req.Header.Get("api-key"); got != "test-key" {
		t.Errorf("api-key = %q, want the configured key", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none when a key is configured", got)
	}
}

// Without a key and without a credential there is nothing to sign a request with, and
// saying so beats sending an anonymous request for the service to refuse.
func TestAuthorizeRefusesWithNothingToSignWith(t *testing.T) {
	idx := &Index{endpoint: "https://example.search.windows.net", name: "articles"}

	req, err := http.NewRequest(http.MethodGet, idx.endpoint, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := idx.authorize(context.Background(), req); err == nil {
		t.Error("authorize accepted a request it had no way to sign")
	}
}
