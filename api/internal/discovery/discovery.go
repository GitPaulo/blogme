package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/blob"
	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
	"github.com/GitPaulo/blogme/api/internal/store"
)

// Azure AI Search accepts at most 1000 documents per indexing request.
const indexBatchSize = 1000

// sourceResult carries one source's crawl outcome back from a worker.
type sourceResult struct {
	source   sources.Source
	articles []article.Article
	err      error
}

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

	client       *http.Client
	fetcher      *fetcher
	robots       *robots
	maxPosts     int
	contentWords int
	concurrency  int
}

// Options tune how much work one run does and how much text it keeps.
type Options struct {
	BatchSize    int
	MaxPosts     int
	ContentWords int
	Concurrency  int
}

func New(provider sources.Provider, st *store.Store, idx *index.Index, cur *Cursor, opts Options) *Discoverer {
	client := &http.Client{Timeout: 20 * time.Second}
	f := newFetcher(client)

	return &Discoverer{
		sources:      provider,
		store:        st,
		index:        idx,
		cursor:       cur,
		batchSize:    opts.BatchSize,
		client:       client,
		fetcher:      f,
		robots:       newRobots(f),
		maxPosts:     opts.MaxPosts,
		contentWords: opts.ContentWords,
		concurrency:  opts.Concurrency,
	}
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

	// Crawling is almost entirely network wait, so run sources in parallel. Each
	// source is a different host, so this stays polite to any individual site.
	results := make(chan sourceResult, len(batch))
	sem := make(chan struct{}, max(d.concurrency, 1))
	var wg sync.WaitGroup

	for _, s := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			found, err := d.crawl(ctx, s)
			results <- sourceResult{source: s, articles: found, err: err}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if res.err != nil {
			// One bad source must not abort the whole pass.
			slog.WarnContext(ctx, "source failed", "source", res.source.ID, "error", res.err)
			failed++
			continue
		}

		for _, a := range res.articles {
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

		processed += len(res.articles)
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
