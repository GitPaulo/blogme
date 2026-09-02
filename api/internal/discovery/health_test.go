package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/blob"
)

// fakeBlobs is one blob held in memory, so health can be exercised without storage.
type fakeBlobs struct {
	data    string
	written string
	present bool
	failGet error
}

func (f *fakeBlobs) DownloadString(_ context.Context, _, _ string) (string, error) {
	if f.failGet != nil {
		return "", f.failGet
	}
	if !f.present {
		return "", blob.ErrNotFound
	}
	return f.data, nil
}

func (f *fakeBlobs) Upload(_ context.Context, _, _ string, data []byte) error {
	f.written = string(data)
	f.present, f.data = true, f.written
	return nil
}

func newTestHealth(store blobStore) *Health {
	return NewHealth(store, "sources", "source-health.json", 3, 7*24*time.Hour)
}

func TestHealthQuarantinesAfterRepeatedFailures(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})
	boom := errors.New("404")

	for i := range 2 {
		h.Record("dead", boom)
		if h.Skip("dead") {
			t.Fatalf("quarantined after %d failures, want 3", i+1)
		}
	}

	h.Record("dead", boom)
	if !h.Skip("dead") {
		t.Error("not quarantined after 3 consecutive failures")
	}
	if h.Quarantined() != 1 {
		t.Errorf("Quarantined() = %d, want 1", h.Quarantined())
	}
}

func TestHealthSuccessClearsTheCount(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})
	boom := errors.New("404")

	h.Record("flaky", boom)
	h.Record("flaky", boom)
	h.Record("flaky", nil)
	h.Record("flaky", boom)

	if h.Skip("flaky") {
		t.Error("quarantined a source that succeeded in between failures")
	}
}

// A quarantined source is set aside, not abandoned: a blog that comes back has to be
// found again.
func TestHealthRetriesAfterTheInterval(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})
	h.entries["dead"] = health{Failures: 9, LastTried: time.Now().UTC().Add(-8 * 24 * time.Hour)}

	if h.Skip("dead") {
		t.Error("still skipped 8 days after the last attempt, want a retry")
	}
	// Still counted as rotted while it waits: it has not succeeded, only come due.
	if h.Quarantined() != 1 {
		t.Errorf("Quarantined() = %d, want 1", h.Quarantined())
	}
}

// A pass cut short cancels every crawl in flight. Counting those would quarantine
// healthy blogs wholesale.
func TestHealthIgnoresCancellation(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})

	for range 5 {
		h.Record("good", context.Canceled)
	}

	if h.Skip("good") {
		t.Error("quarantined a source whose crawls were cancelled")
	}
	if _, recorded := h.entries["good"]; recorded {
		t.Error("cancellation left an entry behind")
	}
}

// A deadline is the source's own fault, unlike cancellation: it spent 90 seconds and
// returned nothing.
func TestHealthCountsTimeouts(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})

	for range 3 {
		h.Record("slow", context.DeadlineExceeded)
	}

	if !h.Skip("slow") {
		t.Error("a source that timed out three times running was not quarantined")
	}
}

func TestHealthRetainDropsRemovedSources(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})
	h.Record("kept", nil)
	h.Record("removed", nil)

	h.Retain(list("kept", "added"))

	if _, ok := h.entries["removed"]; ok {
		t.Error("kept an entry for a source no longer on the list")
	}
	if _, ok := h.entries["kept"]; !ok {
		t.Error("dropped an entry for a source still on the list")
	}
}

func TestHealthLoadOfAMissingBlobIsEmpty(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})

	if err := h.Load(context.Background()); err != nil {
		t.Fatalf("Load() on a blob that was never written = %v, want nil", err)
	}
	if h.Skip("anything") {
		t.Error("skipped a source with nothing known about it")
	}
}

func TestHealthSurvivesARoundTrip(t *testing.T) {
	store := &fakeBlobs{}
	saved := newTestHealth(store)
	for range 3 {
		saved.Record("dead", errors.New("404"))
	}
	if err := saved.Save(context.Background()); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	loaded := newTestHealth(store)
	if err := loaded.Load(context.Background()); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if !loaded.Skip("dead") {
		t.Error("a quarantine did not survive being written and read back")
	}
}

// Health is advisory, so every entry point has to tolerate not having one.
func TestNilHealthIsInert(t *testing.T) {
	var h *Health

	h.Record("anything", errors.New("404"))
	h.Retain(list("a"))
	if h.Skip("anything") {
		t.Error("a nil Health skipped a source")
	}
	if h.Quarantined() != 0 {
		t.Error("a nil Health reported quarantined sources")
	}
	if err := h.Load(context.Background()); err != nil {
		t.Errorf("Load() on a nil Health = %v, want nil", err)
	}
	if err := h.Save(context.Background()); err != nil {
		t.Errorf("Save() on a nil Health = %v, want nil", err)
	}
}

// Turning the threshold off must leave a pass crawling everything, which is the escape
// hatch if quarantine ever sets aside something it should not.
func TestHealthDisabledByZeroThreshold(t *testing.T) {
	h := NewHealth(&fakeBlobs{}, "sources", "source-health.json", 0, 7*24*time.Hour)

	for range 20 {
		h.Record("dead", errors.New("404"))
	}

	if h.Skip("dead") {
		t.Error("quarantined a source with the threshold switched off")
	}
	if h.Quarantined() != 0 {
		t.Errorf("Quarantined() = %d with the threshold off, want 0", h.Quarantined())
	}
}
