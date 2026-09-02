package discovery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

func list(ids ...string) []sources.Source {
	out := make([]sources.Source, len(ids))
	for i, id := range ids {
		out[i] = sources.Source{ID: id}
	}
	return out
}

func ids(in []sources.Source) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResumeIndex(t *testing.T) {
	l := list("a", "b", "c")

	tests := []struct {
		name   string
		cursor string
		want   int
	}{
		{"no cursor starts at the beginning", "", 0},
		{"resumes after the recorded source", "a", 1},
		{"wraps after the last source", "c", 0},
		{"unknown cursor restarts", "removed", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeIndex(l, tt.cursor); got != tt.want {
				t.Errorf("resumeIndex(%q) = %d, want %d", tt.cursor, got, tt.want)
			}
		})
	}
}

func TestBatchFromWrapsAround(t *testing.T) {
	l := list("a", "b", "c", "d")

	batch, next := batchFrom(l, 2, 3, nil)
	if want := []string{"c", "d", "a"}; !equal(ids(batch), want) {
		t.Errorf("batchFrom() = %v, want %v", ids(batch), want)
	}
	if next != "a" {
		t.Errorf("next cursor = %q, want %q", next, "a")
	}
}

func TestBatchFromCapsAtListLength(t *testing.T) {
	l := list("a", "b")

	batch, next := batchFrom(l, 0, 10, nil)
	if len(batch) != 2 {
		t.Errorf("batchFrom() returned %d sources, want 2", len(batch))
	}
	if next != "b" {
		t.Errorf("next cursor = %q, want %q", next, "b")
	}
}

// Successive batches must eventually cover every source.
func TestBatchFromCoversAllSourcesOverMultipleRuns(t *testing.T) {
	l := list("a", "b", "c", "d", "e")
	seen := map[string]bool{}

	cursor := ""
	for range 3 {
		batch, next := batchFrom(l, resumeIndex(l, cursor), 2, nil)
		for _, s := range batch {
			seen[s.ID] = true
		}
		cursor = next
	}

	for _, s := range l {
		if !seen[s.ID] {
			t.Errorf("source %q was never processed", s.ID)
		}
	}
}

// skipping reports the given IDs as quarantined.
func skipping(ids ...string) func(string) bool {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return func(id string) bool {
		_, ok := set[id]
		return ok
	}
}

// The point of quarantine: a skipped source is replaced rather than subtracted, so the
// pass still crawls a full batch and simply covers more ground doing it.
func TestBatchFromFillsPastQuarantinedSources(t *testing.T) {
	l := list("a", "b", "c", "d", "e")

	batch, next := batchFrom(l, 0, 3, skipping("b", "c"))
	if want := []string{"a", "d", "e"}; !equal(ids(batch), want) {
		t.Errorf("batchFrom() = %v, want %v", ids(batch), want)
	}
	if next != "e" {
		t.Errorf("next cursor = %q, want %q", next, "e")
	}
}

// The cursor is the last source examined, not the last one crawled, or the sources
// passed over would be walked again by the next pass forever.
func TestBatchFromCursorCoversSkippedSources(t *testing.T) {
	l := list("a", "b", "c", "d")

	_, next := batchFrom(l, 0, 2, skipping("b"))
	if next != "c" {
		t.Fatalf("next cursor = %q, want %q", next, "c")
	}
	if got := resumeIndex(l, next); got != 3 {
		t.Errorf("next pass resumes at %d, want 3", got)
	}
}

// A list where everything is quarantined must still terminate and still advance, or a
// pass would scan the whole corpus every time to find nothing.
func TestBatchFromBoundsTheScanWhenAllAreQuarantined(t *testing.T) {
	l := list("a", "b", "c", "d", "e", "f", "g", "h")

	batch, next := batchFrom(l, 0, 2, skipping("a", "b", "c", "d", "e", "f", "g", "h"))
	if len(batch) != 0 {
		t.Errorf("batchFrom() returned %d sources, want 0", len(batch))
	}
	// Bounded at scanFactor batches rather than the whole list.
	if next != "f" {
		t.Errorf("next cursor = %q, want %q", next, "f")
	}
}

// recordingSink records the order in which the index and the store were written, so
// the ordering the corpus depends on is asserted rather than assumed.
type recordingSink struct {
	ops      []string
	storeErr error
}

func (r *recordingSink) Upsert(_ context.Context, articles []article.Article) error {
	r.ops = append(r.ops, fmt.Sprintf("index:%d", len(articles)))
	return nil
}

func (r *recordingSink) Save(_ context.Context, a article.Article) error {
	if r.storeErr != nil {
		return r.storeErr
	}
	r.ops = append(r.ops, "store:"+a.ID)
	return nil
}

func (r *recordingSink) Has(context.Context, string) (bool, error) { return false, nil }

func batch(ids ...string) []article.Article {
	out := make([]article.Article, len(ids))
	for i, id := range ids {
		out[i] = article.Article{ID: id, URL: "https://example.com/" + id, Title: id}
	}
	return out
}

// The store is what skipStored consults to decide an article has been dealt with, so
// storing before indexing means a pass killed in between leaves an article that is
// stored, unsearchable, and never looked at again. Twelve runs hit the invocation
// ceiling on 17 August and did exactly that, and the articles are still invisible.
func TestProjectIndexesBeforeItStores(t *testing.T) {
	sink := &recordingSink{}
	d := &Discoverer{index: sink, store: sink}

	if err := d.project(context.Background(), batch("one", "two")); err != nil {
		t.Fatalf("project: %v", err)
	}

	want := []string{"index:2", "store:one", "store:two"}
	if !equal(sink.ops, want) {
		t.Errorf("writes went %v, want %v: the store must not be written before the index",
			sink.ops, want)
	}
}

// A store that refuses one article leaves it unstored, which is the state the next
// pass knows how to fix. Abandoning the batch would throw away a thousand articles
// already indexed for the sake of one blob.
func TestProjectKeepsGoingWhenTheStoreRefuses(t *testing.T) {
	sink := &recordingSink{storeErr: errors.New("storage unavailable")}
	d := &Discoverer{index: sink, store: sink}

	if err := d.project(context.Background(), batch("one", "two")); err != nil {
		t.Errorf("project returned %v, want the pass to continue", err)
	}
	if want := []string{"index:2"}; !equal(sink.ops, want) {
		t.Errorf("writes went %v, want %v", sink.ops, want)
	}
}

// The index is a shared sink: if it refuses, every remaining source will meet the same
// failure, so the pass stops and the cursor stays where it is.
func TestProjectStopsWhenTheIndexRefuses(t *testing.T) {
	failure := errors.New("index unavailable")
	d := &Discoverer{index: refusingIndex{failure}, store: &recordingSink{}}

	if err := d.project(context.Background(), batch("one")); !errors.Is(err, failure) {
		t.Errorf("project returned %v, want the index failure", err)
	}
}

type refusingIndex struct{ err error }

func (r refusingIndex) Upsert(context.Context, []article.Article) error { return r.err }
