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

// How long one source may take before the run moves on without it.
//
// A single source can string together a robots fetch, several sitemap probes and
// a page fetch per post, each with its own client timeout, so without a deadline
// its worst case is the sum of all of them and a run's length is a hope rather
// than a calculation. Generous enough that no healthy blog reaches it; the
// articles gathered before the deadline are still kept.
const sourceTimeout = 90 * time.Second

// sourceResult carries one source's crawl outcome back from a worker.
type sourceResult struct {
	source   sources.Source
	articles []article.Article
	err      error
	// How long the crawl took. Kept per source because the slow ones are what
	// decide whether a pass fits in the invocation, and an average hides them.
	duration time.Duration
}

// failureKind buckets a source failure so a pass can be summarised by cause
// rather than by a count that says only "some blogs did not work".
//
// Timeouts are called out because they mean a source is costing more than its
// share of the run: a rising count is how the sitemap path's growing scan cost
// becomes visible before it starts eating whole passes.
func failureKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
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
	client := newCrawlClient(20 * time.Second)
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
	started := time.Now()

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
		timedOut  int
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

	// save persists one source's articles and queues them for the index. Blob storage
	// is written per article, so a failure here is that source's problem alone.
	save := func(articles []article.Article) error {
		for _, a := range articles {
			if err := d.store.Save(ctx, a); err != nil {
				return fmt.Errorf("save %s: %w", a.ID, err)
			}
			pending = append(pending, a)
		}
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

			sourceCtx, cancel := context.WithTimeout(ctx, sourceTimeout)
			defer cancel()

			start := time.Now()
			found, err := d.crawl(sourceCtx, s)
			results <- sourceResult{
				source: s, articles: found, err: err, duration: time.Since(start),
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		// One bad source must not abort the whole pass, whether it failed while being
		// crawled or while being stored.
		err := res.err
		if err == nil {
			err = save(res.articles)
		}
		if err != nil {
			kind := failureKind(err)
			if kind == "timeout" {
				timedOut++
			}
			slog.WarnContext(ctx, "source failed",
				"source_id", res.source.ID,
				"kind", kind,
				"duration_ms", res.duration.Milliseconds(),
				"error", err)
			failed++
			continue
		}

		// Debug rather than Info: one line per source is the detail you want when
		// hunting a slow pass, and noise the rest of the time.
		slog.DebugContext(ctx, "source done",
			"source_id", res.source.ID,
			"articles", len(res.articles),
			"duration_ms", res.duration.Milliseconds())

		processed += len(res.articles)

		// The index is a shared sink, so unlike a per-source failure its errors will
		// recur for every remaining source; stopping leaves the cursor where it is.
		if len(pending) >= indexBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}

	if err := flush(); err != nil {
		return err
	}

	// Nothing succeeding is a systemic failure rather than a batch of bad blogs, so
	// the cursor stays put and the same slice is retried instead of being skipped.
	if failed > 0 && failed == len(batch) {
		return fmt.Errorf("all %d sources in the batch failed", failed)
	}

	if err := d.cursor.write(ctx, next); err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}

	slog.InfoContext(ctx, "discovery pass complete",
		"articles", processed,
		"sources", len(batch),
		"sources_failed", failed,
		"sources_timed_out", timedOut,
		"duration_ms", time.Since(started).Milliseconds(),
		"next_cursor", next)
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
