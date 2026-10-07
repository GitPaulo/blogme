package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

// deletingIndex records what a prune takes out of the index.
type deletingIndex struct {
	deleted []string
}

func (x *deletingIndex) Upsert(context.Context, []article.Article) error { return nil }

func (x *deletingIndex) Delete(_ context.Context, ids []string) error {
	x.deleted = append(x.deleted, ids...)
	return nil
}

// pruneFixture is a Discoverer holding two articles for a source that has failed every
// attempt for long enough to be checked, and one for a healthy source beside it.
type pruneFixture struct {
	d      *Discoverer
	store  *memStore
	index  *deletingIndex
	source sources.Source
}

func newPruneFixture(t *testing.T, site string, failures int) pruneFixture {
	t.Helper()

	s := sources.Source{ID: "gone", Site: site}
	st := &memStore{have: map[string]bool{
		articleID("gone", site+"posts/one"): true,
		articleID("gone", site+"posts/two"): true,
		articleID("kept", site+"posts/one"): true,
	}}
	idx := &deletingIndex{}

	d := newLocalDiscoverer(5)
	d.store, d.index, d.prune = st, idx, "on"
	d.health = newTestHealth(&fakeBlobs{})
	for range failures {
		d.health.Record(s.ID, errStub)
	}

	return pruneFixture{d: d, store: st, index: idx, source: s}
}

var errStub = &statusError{code: http.StatusNotFound, status: "404 Not Found"}

// A source qualifies at threshold plus pruneAfterProbes failures running.
const unreachableAfter = 3 + pruneAfterProbes

// What the homepage says is the whole decision, once a source has failed for weeks: a
// site gone for readers loses its articles, one that still answers anybody keeps them.
func TestPruneDecidesOnTheHomepage(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, tc := range []struct {
		name string
		site func(t *testing.T) string
		gone bool
	}{
		{"404", statusSite(http.StatusNotFound), true},
		{"410", statusSite(http.StatusGone), true},
		{"connection refused", func(*testing.T) string { return closed.URL + "/" }, true},
		// .invalid never resolves: see RFC 2606.
		{"domain gone", func(*testing.T) string { return "http://blogme-pruned.invalid/" }, true},
		{"still up, only the feed moved", statusSite(http.StatusOK), false},
		{"refuses this crawler", statusSite(http.StatusForbidden), false},
		{"rate limits this crawler", statusSite(http.StatusTooManyRequests), false},
		// What CSDN answers a crawler while serving readers, and what a briefly broken
		// site answers everyone: either way, not proof.
		{"server error", statusSite(521), false},
		{"unavailable", statusSite(http.StatusServiceUnavailable), false},
		// Self-signed, which the test client cannot verify: the shape of a missing
		// intermediate, which a browser repairs and Go does not.
		{"certificate of unknown authority", func(t *testing.T) string {
			srv := httptest.NewTLSServer(http.NotFoundHandler())
			t.Cleanup(srv.Close)
			return srv.URL + "/"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPruneFixture(t, tc.site(t), unreachableAfter)
			f.d.pruneUnreachable(context.Background(), []sources.Source{f.source})

			want, left := 0, 3
			if tc.gone {
				want, left = 2, 1
			}
			if len(f.index.deleted) != want {
				t.Errorf("deleted %d documents from the index, want %d", len(f.index.deleted), want)
			}
			if len(f.store.have) != left {
				t.Errorf("store holds %d articles, want %d", len(f.store.have), left)
			}
			// Pruned once, then left alone; kept, and looked at again on the next probe.
			if got := f.d.health.Unreachable(f.source.ID); got == tc.gone {
				t.Errorf("Unreachable() = %v after the prune, want %v", got, !tc.gone)
			}
		})
	}
}

func statusSite(code int) func(t *testing.T) string {
	return func(t *testing.T) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		t.Cleanup(srv.Close)
		return srv.URL + "/"
	}
}

// A pass has to fail every attempt for weeks before a source's articles are at stake;
// one short of that is not even listed.
func TestPruneLeavesASourceThatHasNotFailedLongEnough(t *testing.T) {
	f := newPruneFixture(t, statusSite(http.StatusNotFound)(t), unreachableAfter-1)
	f.d.pruneUnreachable(context.Background(), []sources.Source{f.source})

	if f.store.lists != 0 || len(f.index.deleted) != 0 {
		t.Errorf("listed %d times and deleted %d, want neither", f.store.lists, len(f.index.deleted))
	}
}

// Dry is for reading what would go before anything does.
func TestPruneDryRunRemovesNothing(t *testing.T) {
	f := newPruneFixture(t, statusSite(http.StatusNotFound)(t), unreachableAfter)
	f.d.prune = "dry"
	f.d.pruneUnreachable(context.Background(), []sources.Source{f.source})

	if len(f.index.deleted) != 0 || len(f.store.have) != 3 {
		t.Errorf("deleted %d documents and kept %d articles, want 0 and 3",
			len(f.index.deleted), len(f.store.have))
	}
	if !f.d.health.Unreachable(f.source.ID) {
		t.Error("a dry run marked the source pruned")
	}
}

// Most unreachable sources never worked and hold nothing. One listing settles those,
// without a request to a site that has been failing for weeks.
func TestPruneMarksAnEmptySourceWithoutAskingTheSite(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	f := newPruneFixture(t, srv.URL+"/", unreachableAfter)
	f.store.have = map[string]bool{}
	f.d.pruneUnreachable(context.Background(), []sources.Source{f.source})

	if got := hits.Load(); got != 0 {
		t.Errorf("made %d requests to the site, want 0", got)
	}
	if f.d.health.Unreachable(f.source.ID) {
		t.Error("an empty source was not marked, so it would be listed again every probe")
	}
}

// A pass with no time left must not read its own deadline as the site being gone.
func TestPruneDoesNothingOnceThePassIsOver(t *testing.T) {
	f := newPruneFixture(t, statusSite(http.StatusNotFound)(t), unreachableAfter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.d.pruneUnreachable(ctx, []sources.Source{f.source})

	if len(f.index.deleted) != 0 {
		t.Errorf("deleted %d documents after the pass ended", len(f.index.deleted))
	}
}

// The same, when the pass runs out while the homepage is still being asked: a site that
// had not answered yet has not been shown to be gone.
func TestPruneDoesNotTakeThePassDeadlineForADeadSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	f := newPruneFixture(t, srv.URL+"/", unreachableAfter)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	f.d.pruneUnreachable(ctx, []sources.Source{f.source})

	if len(f.index.deleted) != 0 {
		t.Errorf("deleted %d documents when the pass ran out mid-check", len(f.index.deleted))
	}
}

// A source that comes back is a source again: its next run of failures counts afresh.
func TestHealthSuccessClearsPruned(t *testing.T) {
	h := newTestHealth(&fakeBlobs{})
	for range unreachableAfter {
		h.Record("back", errStub)
	}
	h.MarkPruned("back")
	if h.Unreachable("back") {
		t.Fatal("Unreachable() = true for a pruned source")
	}

	h.Record("back", nil)
	for range unreachableAfter {
		h.Record("back", errStub)
	}
	if !h.Unreachable("back") {
		t.Error("Unreachable() = false, want the success to have cleared the old prune")
	}
}
