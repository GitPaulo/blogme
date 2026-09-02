package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/GitPaulo/blogme/api/internal/blob"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

// scanFactor bounds how far past its batch size a pass walks looking for sources worth
// crawling. Quarantined sources are passed over rather than counted against the batch,
// so without a bound a mostly-quarantined list would scan the whole thing every pass.
const scanFactor = 3

// health is what previous passes learned about one source.
type health struct {
	// Failures is consecutive: one success clears it, so a source has to fail
	// repeatedly to be set aside and returns the moment it works again.
	Failures int       `json:"failures"`
	LastOK   time.Time `json:"lastOk,omitzero"`
	// LastTried is written on every attempt and is what the retry interval is measured
	// from. A source never tried holds the zero time, which is older than any interval,
	// so nothing is quarantined before it has actually failed.
	LastTried time.Time `json:"lastTried,omitzero"`
}

// blobStore is what source health needs of storage: one blob to read and write.
//
// An interface rather than *blob.Client so a pass can be exercised without an Azure
// account behind it, matching how discovery takes its article store.
type blobStore interface {
	DownloadString(ctx context.Context, container, name string) (string, error)
	Upload(ctx context.Context, container, name string, data []byte) error
}

// Health records which sources are still worth crawling.
//
// About a tenth of the list fails every pass, 93% of those fail again the next time,
// and two thirds of the failures are 404s — entries that never worked rather than blogs
// that died. Measured over four days in September 2026, 291 of 300 such sources had
// never contributed a single indexed article. Re-attempting one costs the most
// expensive path in the crawler, since a failure is what sends it down the whole
// ladder: robots, feed, several sitemap probes, the homepage, then any feed advertised
// there. So a source that fails threshold times running is set aside and probed only
// once per retryAfter.
//
// One blob for the whole list, for the reason quality.Store keeps one: a few megabytes
// read once and written once a pass, where a blob per source would be tens of thousands
// of requests to save nothing.
//
// Unsynchronised, unlike quality.Store, because a pass reads it while choosing a batch
// and writes it from the single goroutine draining the results channel, never from the
// crawl workers themselves.
type Health struct {
	client    blobStore
	container string
	name      string

	threshold  int
	retryAfter time.Duration

	entries map[string]health
}

func NewHealth(client blobStore, container, name string, threshold int, retryAfter time.Duration) *Health {
	return &Health{
		client:     client,
		container:  container,
		name:       name,
		threshold:  threshold,
		retryAfter: retryAfter,
		entries:    make(map[string]health),
	}
}

// Load reads what previous passes learned.
//
// A blob that has never been written is empty rather than an error: before the first
// save there is nothing to read, and knowing nothing means crawling everything, which
// is what a pass did before quarantine existed.
func (h *Health) Load(ctx context.Context) error {
	if h == nil {
		return nil
	}

	raw, err := h.client.DownloadString(ctx, h.container, h.name)
	if errors.Is(err, blob.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read source health: %w", err)
	}

	entries := make(map[string]health)
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return fmt.Errorf("parse source health: %w", err)
	}

	h.entries = entries
	return nil
}

func (h *Health) Save(ctx context.Context) error {
	if h == nil {
		return nil
	}

	data, err := json.Marshal(h.entries)
	if err != nil {
		return fmt.Errorf("marshal source health: %w", err)
	}
	return h.client.Upload(ctx, h.container, h.name, data)
}

// Retain drops what is known about sources no longer on the list, so the blob cannot
// accumulate entries for blogs that have been removed from it.
func (h *Health) Retain(list []sources.Source) {
	if h == nil {
		return
	}

	keep := make(map[string]struct{}, len(list))
	for _, s := range list {
		keep[s.ID] = struct{}{}
	}
	maps.DeleteFunc(h.entries, func(id string, _ health) bool {
		_, ok := keep[id]
		return !ok
	})
}

// Skip reports whether a source is quarantined and not yet due for another attempt.
//
// A nil Health skips nothing, which is the state a pass runs in when the blob could not
// be read or quarantine was never configured.
func (h *Health) Skip(id string) bool {
	if h == nil || h.threshold <= 0 {
		return false
	}

	e, ok := h.entries[id]
	if !ok || e.Failures < h.threshold {
		return false
	}
	return time.Since(e.LastTried) < h.retryAfter
}

// Record notes how one source's crawl went.
//
// Cancellation is not a verdict on the source. A pass cut short by a deploy or by the
// invocation ceiling cancels every crawl still in flight, and counting those would
// quarantine healthy blogs wholesale, so it is left unrecorded.
func (h *Health) Record(id string, err error) {
	if h == nil || errors.Is(err, context.Canceled) {
		return
	}

	e := h.entries[id]
	e.LastTried = time.Now().UTC()
	if err != nil {
		e.Failures++
	} else {
		e.Failures = 0
		e.LastOK = e.LastTried
	}
	h.entries[id] = e
}

// Quarantined is how many sources are currently set aside, which is the standing
// measure of how much of the source list has rotted.
func (h *Health) Quarantined() int {
	if h == nil || h.threshold <= 0 {
		return 0
	}

	n := 0
	for _, e := range h.entries {
		if e.Failures >= h.threshold {
			n++
		}
	}
	return n
}
