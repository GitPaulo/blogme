package index

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

	results, total, err := New(srv.URL, "articles", "test-key").
		Query(context.Background(), "go", 20, 40)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if sent["top"] != float64(20) || sent["skip"] != float64(40) {
		t.Errorf("got top=%v skip=%v, want 20 and 40", sent["top"], sent["skip"])
	}
	if sent["count"] != true {
		t.Errorf("got count=%v, want true", sent["count"])
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

	results, _, err := New(srv.URL, "articles", "test-key").
		Query(context.Background(), "go", 20, 0)
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
