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
	"log/slog"
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
)

type Index struct {
	endpoint string
	name     string
	apiKey   string
	cred     *azidentity.DefaultAzureCredential
	http     *http.Client
}

func New(endpoint, name, apiKey string) *Index {
	idx := &Index{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		name:     name,
		apiKey:   apiKey,
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
	Summary     string   `json:"summary,omitempty"`
	Content     string   `json:"content,omitempty"`
	Topics      []string `json:"topics,omitempty"`
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
				Summary:  a.Summary,
				Content:  a.Content,
				Topics:   a.Topics,
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
	Value []struct {
		Score       float64  `json:"@search.score"`
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Author      string   `json:"author"`
		Summary     string   `json:"summary"`
		Topics      []string `json:"topics"`
		PublishedAt string   `json:"publishedAt"`
	} `json:"value"`
}

// Query runs a full-text search and returns ranked results.
func (i *Index) Query(ctx context.Context, q string, limit int) ([]article.Result, error) {
	body := map[string]any{
		"search":     q,
		"top":        limit,
		"queryType":  "simple",
		"searchMode": "any",
		"select":     "url,title,author,summary,topics,publishedAt",
	}

	var resp searchResponse
	if err := i.do(ctx, http.MethodPost, "/docs/search", body, &resp); err != nil {
		return nil, err
	}

	results := make([]article.Result, 0, len(resp.Value))
	for _, v := range resp.Value {
		r := article.Result{
			URL:     v.URL,
			Title:   v.Title,
			Author:  v.Author,
			Summary: v.Summary,
			Topics:  v.Topics,
			Score:   v.Score,
		}
		if v.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, v.PublishedAt); err == nil {
				r.PublishedAt = t
			}
		}
		results = append(results, r)
	}

	return results, nil
}

func (i *Index) do(ctx context.Context, method, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/indexes/%s%s?api-version=%s",
		i.endpoint, url.PathEscape(i.name), path, apiVersion)

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := i.authorize(ctx, req); err != nil {
		return err
	}

	resp, err := i.http.Do(req)
	if err != nil {
		return fmt.Errorf("call search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
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
