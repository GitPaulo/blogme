package store

import (
	"context"
	"log/slog"

	"github.com/GitPaulo/blogme/api/internal/article"
)

// Store persists canonical article JSON. Azure Blob Storage is the source of
// truth; the search index is rebuildable from it.
type Store struct {
	container string
}

func New(container string) *Store {
	return &Store{container: container}
}

// Save writes the canonical article JSON for a single article.
func (s *Store) Save(ctx context.Context, a article.Article) error {
	// TODO: upload to Azure Blob Storage (container s.container, blob a.ID+".json").
	slog.InfoContext(ctx, "store.save not implemented", "container", s.container, "id", a.ID)
	return nil
}
