package index

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
)

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

	results, total, err := New(srv.URL, "articles", "test-key", "").
		Query(context.Background(), "go", QueryOptions{Limit: 20, Offset: 40})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

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

	results, _, err := New(srv.URL, "articles", "test-key", "").
		Query(context.Background(), "go", QueryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

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

	_, _, err := New(srv.URL, "articles", "test-key", "blogme-semantic").
		Query(context.Background(), "scaling single threaded servers", QueryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if sent["queryType"] != "semantic" {
		t.Errorf("queryType = %v, want semantic", sent["queryType"])
	}
	if sent["semanticConfiguration"] != "blogme-semantic" {
		t.Errorf("semanticConfiguration = %v", sent["semanticConfiguration"])
	}
	if sent["searchMode"] != "any" {
		t.Errorf("searchMode = %v, want any so the reranker gets a wide candidate set", sent["searchMode"])
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

	_, _, err := New(srv.URL, "articles", "test-key", "blogme-semantic").
		Query(context.Background(), "go", QueryOptions{Limit: 20, Rank: RankKeyword})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

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

	res, total, err := New(srv.URL, "articles", "test-key", "blogme-semantic").
		Query(context.Background(), "go", QueryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("query should have fallen back, got error: %v", err)
	}

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

		_, _, err := New(srv.URL, "articles", "test-key", "").
			Query(context.Background(), "go", QueryOptions{Limit: 20, Origin: tc.origin})
		srv.Close()
		if err != nil {
			t.Fatalf("query: %v", err)
		}

		got, _ := sent["filter"].(string)
		if got != tc.want {
			t.Errorf("origin %q: filter = %q, want %q", tc.origin, got, tc.want)
		}
	}
}
