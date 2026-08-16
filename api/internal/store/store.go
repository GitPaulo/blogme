package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/blob"
)

// Store persists canonical article JSON. Azure Blob Storage is the source of
// truth; the search index is rebuildable from it.
type Store struct {
	client    *blob.Client
	container string
}

func New(client *blob.Client, container string) *Store {
	return &Store{client: client, container: container}
}

// Save writes the canonical JSON for a single article.
func (s *Store) Save(ctx context.Context, a article.Article) error {
	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", a.ID, err)
	}
	return s.client.Upload(ctx, s.container, a.ID+".json", data)
}
