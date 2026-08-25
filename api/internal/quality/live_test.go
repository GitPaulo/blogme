package quality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/index"
)

// This is the whole pipeline against a real search service: the schema as it is
// committed, articles written the way the crawler writes them, the scorer judging
// them, and Azure ranking the result. Everything else in this package tests a piece.
//
//	BLOGME_LIVE_INDEX_TEST=1 \
//	BLOGME_SEARCH_ENDPOINT=https://<service>.search.windows.net \
//	BLOGME_SEARCH_API_KEY=<admin key> \
//	go test ./internal/quality -run TestLive -v
//
// It works in an index of its own, created from infra/search-index.json and deleted
// afterwards, so it never touches the corpus. Two opt-ins are required rather than
// one: an endpoint alone is what the ranking harness needs, and this writes.

const liveAPIVersion = "2024-07-01"

type liveIndex struct {
	endpoint string
	name     string
	key      string
	client   *http.Client
}

func newLiveIndex(t *testing.T) *liveIndex {
	t.Helper()

	if os.Getenv("BLOGME_LIVE_INDEX_TEST") != "1" {
		t.Skip("set BLOGME_LIVE_INDEX_TEST=1 to run against a real search service")
	}
	endpoint, key := os.Getenv("BLOGME_SEARCH_ENDPOINT"), os.Getenv("BLOGME_SEARCH_API_KEY")
	if endpoint == "" || key == "" {
		t.Skip("set BLOGME_SEARCH_ENDPOINT and BLOGME_SEARCH_API_KEY to run this")
	}

	live := &liveIndex{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		// Named for the run so a leftover from a crashed test is recognisable, and so
		// two runs cannot collide.
		name:   fmt.Sprintf("quality-e2e-%d", time.Now().Unix()),
		key:    key,
		client: &http.Client{Timeout: 30 * time.Second},
	}

	schema := map[string]any{}
	raw, err := os.ReadFile("../../../infra/search-index.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	schema["name"] = live.name

	live.do(t, http.MethodPut, "/indexes/"+live.name, schema)
	t.Cleanup(func() { live.do(t, http.MethodDelete, "/indexes/"+live.name, nil) })

	return live
}

// do calls the search service and fails the test on anything but success.
func (l *liveIndex) do(t *testing.T, method, path string, body any) []byte {
	t.Helper()

	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, l.endpoint+path+"?api-version="+liveAPIVersion, payload)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("api-key", l.key)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := l.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s returned %s: %s", method, path, resp.Status, answer)
	}
	return answer
}

// awaitCount waits for indexing to catch up. Writes are not visible the instant they
// are accepted, so without this the test races the service rather than testing it.
func (l *liveIndex) awaitCount(t *testing.T, want int) {
	t.Helper()

	for range 20 {
		got, err := strconv.Atoi(strings.TrimSpace(string(l.do(t, http.MethodGet, "/indexes/"+l.name+"/docs/$count", nil))))
		if err == nil && got == want {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("index did not reach %d documents", want)
}

// titles runs a search under one scoring profile and returns the titles in order.
func titles(t *testing.T, idx *index.Index, q, profile string) []string {
	t.Helper()

	page, err := idx.Query(context.Background(), q, index.QueryOptions{
		Limit: 10, Fetch: 30, Rank: index.RankKeyword, Profile: profile,
	})
	if err != nil {
		t.Fatalf("query %q under %q: %v", q, profile, err)
	}

	out := make([]string, 0, len(page.Results))
	for _, r := range page.Results {
		out = append(out, r.Title)
	}
	return out
}

func TestLiveQualityPipelineReordersResults(t *testing.T) {
	live := newLiveIndex(t)
	idx := index.New(live.endpoint, live.name, live.key, "")
	ctx := context.Background()

	// Written exactly as the crawler writes them, so the document shape under test is
	// the real one rather than one this test invented.
	articles := []article.Article{{
		ID:       "pydocs-landing",
		URL:      "https://docs.python.org/3.12/",
		Title:    "Python 3.12 documentation",
		Author:   "Python documentation",
		SourceID: "pydocs",
		Origin:   article.OriginSitemap,
		Summary:  "Welcome! This is the official documentation for Python 3.12.13.",
		Content:  repeat(listing, 3),
		Topics:   []string{"python"},
	}, {
		ID:          "invent-gotchas",
		URL:         "https://inventwithpython.com/blog/2023/08/13/python-gotchas",
		Title:       "8 Common Python Gotchas",
		Author:      "Invent with Python",
		SourceID:    "invent",
		Origin:      article.OriginFeed,
		Summary:     "Mutable defaults and import cycles surprise people for the same reason.",
		Content:     repeat(prose, 3),
		Topics:      []string{"python"},
		PublishedAt: time.Date(2023, 8, 13, 0, 0, 0, 0, time.UTC),
	}}

	if err := idx.Upsert(ctx, articles); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	live.awaitCount(t, len(articles))

	// The failure this exists to correct, reproduced against the real ranker: under
	// the profile in use today the landing page beats the article.
	if before := titles(t, idx, "python", "relevance"); len(before) == 0 || before[0] != "Python 3.12 documentation" {
		t.Fatalf("before scoring, ranking was %v — the fixture no longer reproduces the problem", before)
	}

	// Everything is unscored, which is what a freshly built index looks like.
	pending, remaining, err := idx.Unscored(ctx, Version, 100)
	if err != nil {
		t.Fatalf("unscored: %v", err)
	}
	if len(pending) != len(articles) || remaining != len(articles) {
		t.Fatalf("found %d unscored of %d reported, want %d of %d",
			len(pending), remaining, len(articles), len(articles))
	}

	if err := New(idx, nil, nil, Options{ScoreBatch: 100}).Run(ctx); err != nil {
		t.Fatalf("scorer run: %v", err)
	}

	// The set drains: what has been judged leaves it, which is the whole mechanism
	// standing in for a queue. Awaited rather than asserted outright, because a score
	// is accepted before it is searchable — which is the same lag the scorer itself
	// has to tolerate.
	awaitUnscored(t, idx, 0)

	after := titles(t, idx, "python", "relevance-quality")
	if len(after) == 0 || after[0] != "8 Common Python Gotchas" {
		t.Errorf("after scoring, ranking was %v, want the article first", after)
	}

	// The default profile is untouched by any of this, so turning the boost on stays
	// a deliberate act rather than something scoring does on its own.
	if unchanged := titles(t, idx, "python", "relevance"); unchanged[0] != "Python 3.12 documentation" {
		t.Errorf("the default profile reordered on its own: %v", unchanged)
	}
}

// awaitUnscored waits for the unscored set to reach a given size. Scores are accepted
// before they are searchable, so asserting on the next line would test the lag rather
// than the drain.
func awaitUnscored(t *testing.T, idx *index.Index, want int) {
	t.Helper()

	var last int
	for range 20 {
		pending, _, err := idx.Unscored(context.Background(), Version, 100)
		if err != nil {
			t.Fatalf("unscored: %v", err)
		}
		if last = len(pending); last == want {
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("unscored set settled at %d articles, want %d", last, want)
}
