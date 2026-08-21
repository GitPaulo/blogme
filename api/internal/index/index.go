// Package index is the searchable projection of the corpus, backed by Azure AI
// Search. It is rebuildable from the canonical article JSON in blob storage.
//
// Azure AI Search has no official Go data-plane SDK, so this calls the REST API
// directly. In Azure the function app's managed identity supplies a bearer token;
// locally an API key is used instead.
package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/GitPaulo/blogme/api/internal/article"
)

const (
	apiVersion = "2024-07-01"
	// Azure AI Search rejects indexing requests above 1000 documents.
	maxBatch = 1000
	// How much of a failed response is quoted back in the error.
	maxErrorBytes = 2 << 10
)

// queryTimeout is how long one search of the index may take.
//
// An execution is billed for its whole duration at the instance's memory size, so a
// query left to run out the HTTP client's 30 seconds costs some nine hundred times
// what a healthy one does — and answers a reader who left long ago. The slowest
// search seen in production is under 1.5s, so this is several times the worst real
// case. It is applied per request rather than to the client, because the client is
// shared with Upsert, which posts a thousand documents at a time and needs every
// second of the longer budget.
//
// A variable only so the tests can shorten it; nothing in the service reassigns it.
var queryTimeout = 5 * time.Second

type Index struct {
	endpoint string
	name     string
	apiKey   string
	// semantic names the index's semantic configuration. Empty turns reranking off.
	semantic string
	cred     *azidentity.DefaultAzureCredential
	http     *http.Client
}

func New(endpoint, name, apiKey, semantic string) *Index {
	idx := &Index{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		name:     name,
		apiKey:   apiKey,
		semantic: semantic,
		http:     &http.Client{Timeout: 30 * time.Second},
	}

	if apiKey == "" {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			slog.Error("search credential unavailable", "error", err)
		} else {
			idx.cred = cred
		}
	}

	return idx
}

// document is the wire shape of an indexed article, matching infra/search-index.json.
type document struct {
	Action      string   `json:"@search.action"`
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Author      string   `json:"author,omitempty"`
	SourceID    string   `json:"sourceId"`
	Origin      string   `json:"origin,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Content     string   `json:"content,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	Kind        []string `json:"kind,omitempty"`
	PublishedAt *string  `json:"publishedAt,omitempty"`
}

// Upsert adds or replaces articles in the index.
func (i *Index) Upsert(ctx context.Context, articles []article.Article) error {
	for start := 0; start < len(articles); start += maxBatch {
		end := min(start+maxBatch, len(articles))

		docs := make([]document, 0, end-start)
		for _, a := range articles[start:end] {
			d := document{
				Action:   "mergeOrUpload",
				ID:       a.ID,
				URL:      a.URL,
				Title:    a.Title,
				Author:   a.Author,
				SourceID: a.SourceID,
				Origin:   a.Origin,
				Summary:  a.Summary,
				Content:  a.Content,
				Topics:   a.Topics,
				Kind:     a.Kind,
			}
			if !a.PublishedAt.IsZero() {
				ts := a.PublishedAt.UTC().Format(time.RFC3339)
				d.PublishedAt = &ts
			}
			docs = append(docs, d)
		}

		if err := i.do(ctx, http.MethodPost, "/docs/index", map[string]any{"value": docs}, nil); err != nil {
			return fmt.Errorf("index %d documents: %w", len(docs), err)
		}
	}

	return nil
}

type searchResponse struct {
	Total int `json:"@odata.count"`
	Value []struct {
		Score       float64  `json:"@search.score"`
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Author      string   `json:"author"`
		Origin      string   `json:"origin"`
		Summary     string   `json:"summary"`
		Topics      []string `json:"topics"`
		PublishedAt string   `json:"publishedAt"`
		SourceID    string   `json:"sourceId"`
	} `json:"value"`
}

// maxPerSource caps how much of one page a single blog may occupy.
//
// Three posts from one site is rarely what a reader wanted, so this earns its
// place on ordinary queries; it also means a source that stuffs its posts with
// popular terms takes three rows rather than the page. Applied to the page that
// came back rather than by over-fetching, so a page can come back short but the
// paging arithmetic is untouched.
const maxPerSource = 3

// Ranking modes. Semantic reranks the top keyword matches with a language model, which
// is what makes a query phrased as a sentence work. Keyword is plain relevance scoring:
// worse at intent, but it ranks the whole result set rather than a 50-document window,
// so it is the one that can page deep.
const (
	RankSemantic = "semantic"
	RankKeyword  = "keyword"
)

// QueryOptions narrows a search beyond the query text itself.
type QueryOptions struct {
	Limit  int
	Offset int
	// Origin, when set to a known discovery method, restricts results to it.
	Origin string
	// Rank selects the ranking mode. Empty means semantic, which is the default.
	Rank string
}

// Ready reports whether this instance can actually read the index.
//
// It asks for a document count rather than checking that a credential exists,
// because the failures worth catching all authenticate perfectly well: a role
// assignment that was never granted still issues a token, and a misspelled index
// name is a valid request to somewhere that is not there. Both answer every real
// search with an error while any cheaper check says all is well.
//
// Counting is not semantic, so it spends nothing from the metered reranking quota.
func (i *Index) Ready(ctx context.Context) error {
	return i.do(ctx, http.MethodGet, "/docs/$count", nil, nil)
}

// Query runs a full-text search and returns one page of ranked results along with
// the number of matches across the whole corpus.
func (i *Index) Query(ctx context.Context, q string, opts QueryOptions) ([]article.Result, int, error) {
	// sourceId is selected but never returned to the caller: it is there so one blog
	// can be stopped from filling the page. See maxPerSource.
	body := map[string]any{
		"search":     q,
		"top":        opts.Limit,
		"skip":       opts.Offset,
		"count":      true,
		"queryType":  "simple",
		"searchMode": "any",
		"select":     "url,title,author,origin,summary,topics,publishedAt,sourceId",
	}

	// Built from a fixed set rather than from the caller's string, so a filter can
	// never be injected through the query parameter.
	switch opts.Origin {
	case article.OriginSitemap:
		body["filter"] = "origin eq 'sitemap'"
	case article.OriginFeed:
		// Documents indexed before origin existed came from feeds.
		body["filter"] = "origin eq 'feed' or origin eq null"
	}

	if i.semantic != "" && opts.Rank != RankKeyword {
		semantic := maps.Clone(body)
		// Keyword scoring picks the candidates, the reranker decides their order. That
		// division is why searchMode stays "any": a wide net is exactly what the
		// reranker wants to sort out, and it is what made "any" a liability before.
		semantic["queryType"] = "semantic"
		semantic["semanticConfiguration"] = i.semantic

		var resp searchResponse
		err := i.search(ctx, semantic, &resp)
		if err == nil {
			return results(resp), resp.Total, nil
		}
		// Reranking is a metered, throttled resource: the free plan stops at 1,000
		// queries a month and the service sheds load above roughly ten concurrent
		// queries. Worse ranking is a disappointment, no search at all is an outage,
		// so a failure here falls through to the plain keyword query.
		slog.WarnContext(ctx, "semantic ranking unavailable, using keyword ranking", "error", err)
	}

	var resp searchResponse
	if err := i.search(ctx, body, &resp); err != nil {
		return nil, 0, err
	}
	return results(resp), resp.Total, nil
}

// search runs one query under its own time budget.
//
// Per call rather than around the pair of them, so that a semantic attempt which
// burns the whole budget still leaves the keyword fallback a full one. Sharing a
// deadline would turn the slow case into the failing case, which is the outcome
// the fallback exists to prevent.
func (i *Index) search(ctx context.Context, body map[string]any, out *searchResponse) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	return i.do(ctx, http.MethodPost, "/docs/search", body, out)
}

// results projects the wire response onto the shape the API returns.
func results(resp searchResponse) []article.Result {
	out := make([]article.Result, 0, len(resp.Value))
	perSource := make(map[string]int, len(resp.Value))

	for _, v := range resp.Value {
		// Documents indexed before sourceId was selected carry none, and an unknown
		// source cannot be shown to be over its share.
		if v.SourceID != "" {
			if perSource[v.SourceID] >= maxPerSource {
				continue
			}
			perSource[v.SourceID]++
		}

		r := article.Result{
			URL:     v.URL,
			Title:   v.Title,
			Author:  v.Author,
			Origin:  v.Origin,
			Summary: v.Summary,
			Topics:  v.Topics,
			Score:   v.Score,
		}
		if v.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, v.PublishedAt); err == nil {
				r.PublishedAt = t
			}
		}
		out = append(out, r)
	}
	return out
}

func (i *Index) do(ctx context.Context, method, path string, body, out any) error {
	// A nil body means a GET, which carries none: sending "null" with a JSON content
	// type would be a request the service is entitled to refuse.
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		payload = bytes.NewReader(raw)
	}

	endpoint := fmt.Sprintf("%s/indexes/%s%s?api-version=%s",
		i.endpoint, url.PathEscape(i.name), path, apiVersion)

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if err := i.authorize(ctx, req); err != nil {
		return err
	}

	resp, err := i.http.Do(req)
	if err != nil {
		return fmt.Errorf("call search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		// Bounded: this error text ends up in a log record, and an upstream failure
		// is entitled to return a response body of any size it likes.
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(io.LimitReader(resp.Body, maxErrorBytes))
		return fmt.Errorf("search returned %s: %s", resp.Status, strings.TrimSpace(msg.String()))
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (i *Index) authorize(ctx context.Context, req *http.Request) error {
	if i.apiKey != "" {
		req.Header.Set("api-key", i.apiKey)
		return nil
	}
	if i.cred == nil {
		return fmt.Errorf("no search credential configured")
	}

	token, err := i.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://search.azure.com/.default"},
	})
	if err != nil {
		return fmt.Errorf("acquire search token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	return nil
}
