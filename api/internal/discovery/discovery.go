package discovery

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
	"github.com/GitPaulo/blogme/api/internal/store"
)

// Discoverer walks the approved source list, turns new posts into canonical
// articles, persists them, and projects them into the search index.
type Discoverer struct {
	sourcesPath string
	store       *store.Store
	index       *index.Index
}

func New(sourcesPath string, st *store.Store, idx *index.Index) *Discoverer {
	return &Discoverer{sourcesPath: sourcesPath, store: st, index: idx}
}

// Run performs one discovery pass over every approved source.
func (d *Discoverer) Run(ctx context.Context) error {
	list, err := sources.Load(d.sourcesPath)
	if err != nil {
		return fmt.Errorf("load sources: %w", err)
	}

	slog.InfoContext(ctx, "discovery pass starting", "sources", len(list))

	var discovered []article.Article
	for _, s := range list {
		found, err := d.fromSource(ctx, s)
		if err != nil {
			// One bad source must not abort the whole pass.
			slog.ErrorContext(ctx, "source failed", "source", s.ID, "error", err)
			continue
		}
		discovered = append(discovered, found...)
	}

	for _, a := range discovered {
		if err := d.store.Save(ctx, a); err != nil {
			return fmt.Errorf("save %s: %w", a.ID, err)
		}
	}

	if err := d.index.Upsert(ctx, discovered); err != nil {
		return fmt.Errorf("index upsert: %w", err)
	}

	slog.InfoContext(ctx, "discovery pass complete", "articles", len(discovered))
	return nil
}

func (d *Discoverer) fromSource(ctx context.Context, s sources.Source) ([]article.Article, error) {
	// TODO: check robots.txt, read the RSS/Atom feed or sitemap, fetch and extract
	// each new post, then score it for quality before accepting it.
	slog.InfoContext(ctx, "source discovery not implemented", "source", s.ID, "site", s.Site)
	return nil, nil
}
