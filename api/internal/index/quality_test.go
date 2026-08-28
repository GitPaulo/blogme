package index

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The unscored set is the whole work queue, so the request that reads it has to name
// the version that defines membership, and ask for the count, which is the only
// measure a drain has of how much is left.
func TestUnscoredAsksForTheArticlesNotYetJudged(t *testing.T) {
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{
			"@odata.count": 531757,
			"value": [{
				"id": "blog-abc123",
				"url": "https://example.com/post",
				"title": "A post",
				"author": "A blog",
				"origin": "feed",
				"content": "the text of the post"
			}]
		}`)
	}))
	defer srv.Close()

	got, remaining, err := New(srv.URL, "articles", "test-key", "").
		Unscored(context.Background(), 3, 1000, Dated)
	if err != nil {
		t.Fatalf("unscored: %v", err)
	}

	filter, _ := sent["filter"].(string)
	if !strings.Contains(filter, "qualityVersion eq null") || !strings.Contains(filter, "qualityVersion lt 3") {
		t.Errorf("filter = %q, want the unscored and the out-of-date", filter)
	}
	if sent["top"] != float64(1000) || sent["count"] != true {
		t.Errorf("got top=%v count=%v, want 1000 and true", sent["top"], sent["count"])
	}
	// Newest first, so a corpus still draining spends its effort where readers are.
	if sent["orderby"] != "publishedAt desc" {
		t.Errorf("orderby = %v, want the newest first", sent["orderby"])
	}
	// Content is what every measure is taken from; asking for less would mean
	// scoring articles without reading them.
	if selected, _ := sent["select"].(string); !strings.Contains(selected, "content") {
		t.Errorf("select = %q, want the article's text", selected)
	}

	if remaining != 531757 {
		t.Errorf("remaining = %d, want the corpus-wide count", remaining)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].ID != "blog-abc123" || got[0].Content != "the text of the post" {
		t.Errorf("candidate = %+v, want the document's id and text", got[0])
	}
}

// Scores are written onto articles that already exist. Uploading instead would create
// a document out of a score alone whenever the article had been deleted, and that
// document would be returned by searches as a row with no title and no link.
func TestSaveScoresMergesOntoExistingArticles(t *testing.T) {
	var sent struct {
		Value []map[string]any `json:"value"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	err := New(srv.URL, "articles", "test-key", "").SaveScores(context.Background(), []Scores{{
		ID:         "blog-abc123",
		Quality:    0.82,
		Content:    0.75,
		Popularity: 0.3,
		WordCount:  1200,
		Version:    2,
	}})
	if err != nil {
		t.Fatalf("save scores: %v", err)
	}

	if len(sent.Value) != 1 {
		t.Fatalf("sent %d documents, want 1", len(sent.Value))
	}
	doc := sent.Value[0]

	if doc["@search.action"] != "merge" {
		t.Errorf("action = %v, want merge: an upload would invent an article", doc["@search.action"])
	}
	for field, want := range map[string]any{
		"id":             "blog-abc123",
		"quality":        0.82,
		"qContent":       0.75,
		"qPopularity":    0.3,
		"wordCount":      float64(1200),
		"qualityVersion": float64(2),
	} {
		if doc[field] != want {
			t.Errorf("%s = %v, want %v", field, doc[field], want)
		}
	}
}

// Azure refuses an indexing request above a thousand documents, so a larger run has
// to arrive as several.
func TestSaveScoresSplitsOversizedRuns(t *testing.T) {
	batches := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Value []map[string]any `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(sent.Value) > maxBatch {
			t.Errorf("sent %d documents in one request, above the %d limit", len(sent.Value), maxBatch)
		}
		batches++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	scores := make([]Scores, 2500)
	for i := range scores {
		scores[i] = Scores{ID: "post", Quality: 0.5, Version: 1}
	}

	if err := New(srv.URL, "articles", "test-key", "").SaveScores(context.Background(), scores); err != nil {
		t.Fatalf("save scores: %v", err)
	}
	if batches != 3 {
		t.Errorf("sent %d requests for 2500 scores, want 3", batches)
	}
}
