package index

import (
	"context"
	"log/slog"

	"github.com/GitPaulo/blogme/api/internal/article"
)

// Index is the searchable projection of the corpus, backed by Azure AI Search.
// It is treated as rebuildable from the canonical store at any time.
type Index struct {
	endpoint string
	name     string
	apiKey   string
}

func New(endpoint, name, apiKey string) *Index {
	return &Index{endpoint: endpoint, name: name, apiKey: apiKey}
}

// Query runs a search and returns ranked results.
func (i *Index) Query(ctx context.Context, q string, limit int) ([]article.Result, error) {
	// TODO: query Azure AI Search (full-text first, hybrid later).
	slog.InfoContext(ctx, "index.query not implemented", "index", i.name, "limit", limit)
	return []article.Result{}, nil
}

// Upsert adds or replaces articles in the index.
func (i *Index) Upsert(ctx context.Context, articles []article.Article) error {
	// TODO: push documents to Azure AI Search.
	slog.InfoContext(ctx, "index.upsert not implemented", "index", i.name, "count", len(articles))
	return nil
}
