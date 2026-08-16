package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/blob"
	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
	"github.com/GitPaulo/blogme/api/internal/store"
)

// Azure AI Search accepts at most 1000 documents per indexing request.
const indexBatchSize = 1000

// Discoverer walks the approved source list, turns new posts into canonical
// articles, persists them, and projects them into the search index.
//
// The corpus is far larger than one invocation can process, so each run handles a
// fixed slice of the source list and records where it stopped. Successive runs
// continue from there and wrap around, giving every source regular coverage
// without any single run approaching the function timeout.
type Discoverer struct {
	sources   sources.Provider
	store     *store.Store
	index     *index.Index
	cursor    *Cursor
	batchSize int
}

func New(provider sources.Provider, st *store.Store, idx *index.Index, cur *Cursor, batchSize int) *Discoverer {
	return &Discoverer{sources: provider, store: st, index: idx, cursor: cur, batchSize: batchSize}
}

// Run performs one bounded discovery pass.
func (d *Discoverer) Run(ctx context.Context) error {
	list, err := d.sources.Load(ctx)
	if err != nil {
		return fmt.Errorf("load sources: %w", err)
	}
	if len(list) == 0 {
		slog.InfoContext(ctx, "no sources to process")
		return nil
	}

	last, err := d.cursor.read(ctx)
	if err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}

	start := resumeIndex(list, last)
	batch, next := slice(list, start, d.batchSize)

	slog.InfoContext(ctx, "discovery pass starting",
		"sources_total", len(list), "batch", len(batch), "start", start)

	var (
		pending   []article.Article
		processed int
		failed    int
	)

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := d.index.Upsert(ctx, pending); err != nil {
			return fmt.Errorf("index upsert: %w", err)
		}
		pending = pending[:0]
		return nil
	}

	for _, s := range batch {
		found, err := d.fromSource(ctx, s)
		if err != nil {
			// One bad source must not abort the whole pass.
			slog.ErrorContext(ctx, "source failed", "source", s.ID, "error", err)
			failed++
			continue
		}

		for _, a := range found {
			if err := d.store.Save(ctx, a); err != nil {
				return fmt.Errorf("save %s: %w", a.ID, err)
			}
			pending = append(pending, a)
			if len(pending) >= indexBatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}

		processed += len(found)
	}

	if err := flush(); err != nil {
		return err
	}

	if err := d.cursor.write(ctx, next); err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}

	slog.InfoContext(ctx, "discovery pass complete",
		"articles", processed, "sources_failed", failed, "next_cursor", next)
	return nil
}

func (d *Discoverer) fromSource(ctx context.Context, s sources.Source) ([]article.Article, error) {
	// TODO: check robots.txt, read the RSS/Atom feed or sitemap, fetch and extract
	// each new post, then score it for quality before accepting it.
	slog.DebugContext(ctx, "source discovery not implemented", "source", s.ID, "site", s.Site)
	return nil, nil
}

// resumeIndex finds where to continue from. Resuming by source ID rather than by
// offset keeps the position stable when the list is regenerated.
func resumeIndex(list []sources.Source, lastID string) int {
	if lastID == "" {
		return 0
	}
	for i, s := range list {
		if s.ID == lastID {
			return (i + 1) % len(list)
		}
	}
	return 0
}

// slice returns up to n sources from start, wrapping around, plus the ID to resume
// after on the next run.
func slice(list []sources.Source, start, n int) ([]sources.Source, string) {
	if n <= 0 || n > len(list) {
		n = len(list)
	}

	batch := make([]sources.Source, 0, n)
	for i := range n {
		batch = append(batch, list[(start+i)%len(list)])
	}
	return batch, batch[len(batch)-1].ID
}

// Cursor records the last source processed, so runs resume rather than restart.
type Cursor struct {
	client    *blob.Client
	container string
	name      string
}

func NewCursor(client *blob.Client, container, name string) *Cursor {
	return &Cursor{client: client, container: container, name: name}
}

func (c *Cursor) read(ctx context.Context) (string, error) {
	v, err := c.client.DownloadString(ctx, c.container, c.name)
	if errors.Is(err, blob.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

func (c *Cursor) write(ctx context.Context, id string) error {
	return c.client.Upload(ctx, c.container, c.name, []byte(id))
}
