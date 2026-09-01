package quality

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GitPaulo/blogme/api/internal/blob"
)

// hnServer answers like Hacker News with a fixed body, and reports what was asked.
func hnServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()

	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("query")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	previous := hnEndpoint
	hnEndpoint = srv.URL
	t.Cleanup(func() { hnEndpoint = previous })

	return srv, &query
}

// hnFailing answers every lookup with a refusal, standing in for the rate limits and
// network faults a sweep of thousands of third-party sites meets in production.
func hnFailing(t *testing.T, status int) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	previous := hnEndpoint
	hnEndpoint = srv.URL
	t.Cleanup(func() { hnEndpoint = previous })
}

// The search matches URLs loosely, so the answer contains other sites. Counting them
// would hand every blog on a shared platform the standing of the loudest one on it.
func TestHackerNewsCountsOnlyTheSiteItAskedAbout(t *testing.T) {
	_, query := hnServer(t, `{"hits":[
		{"url":"https://simonwillison.net/2023/Feb/15/bing/","points":100},
		{"url":"https://www.simonwillison.net/2024/Jan/1/other/","points":50},
		{"url":"https://notsimonwillison.net/copycat","points":900},
		{"url":"https://example.com/simonwillison-net-review","points":800}
	]}`)

	points, stories, err := hackerNews(context.Background(), http.DefaultClient, "simonwillison.net")
	if err != nil {
		t.Fatalf("hackerNews: %v", err)
	}

	if points != 150 || stories != 2 {
		t.Errorf("got %d points over %d stories, want 150 over 2", points, stories)
	}
	if *query != "simonwillison.net" {
		t.Errorf("asked about %q, want the site", *query)
	}
}

func TestHackerNewsReportsARefusal(t *testing.T) {
	hnFailing(t, http.StatusTooManyRequests)

	if _, _, err := hackerNews(context.Background(), http.DefaultClient, "example.com"); err == nil {
		t.Error("a refusal was read as an answer")
	}
}

func TestScoreRisesWithAttentionAndStopsAtOne(t *testing.T) {
	store := &Store{entries: map[string]Entry{
		"quiet.example":   {Points: 5},
		"known.example":   {Points: 200},
		"famous.example":  {Points: 5000},
		"nothing.example": {Points: 0},
	}}

	quiet := store.Score("https://quiet.example/post")
	known := store.Score("https://known.example/post")
	famous := store.Score("https://famous.example/post")

	if !(quiet < known && known < famous) {
		t.Errorf("scores did not rise with attention: %.3f, %.3f, %.3f", quiet, known, famous)
	}
	if famous > 1 {
		t.Errorf("a very popular site scored %.3f, outside the range the boost expects", famous)
	}
	// A site nobody has posted and a site never asked about are the same answer, and
	// that answer costs an article nothing.
	if got := store.Score("https://nothing.example/post"); got != 0 {
		t.Errorf("a site with no stories scored %.3f, want 0", got)
	}
	if got := store.Score("https://unknown.example/post"); got != 0 {
		t.Errorf("a site never asked about scored %.3f, want 0", got)
	}
}

// The scorer runs before popularity has ever been gathered, and must still score.
func TestNilStoreScoresNothingRatherThanFailing(t *testing.T) {
	var store *Store
	if got := store.Score("https://example.com/post"); got != 0 {
		t.Errorf("nil store scored %.3f, want 0", got)
	}
}

// www is not a different blog.
func TestSiteOfIgnoresWWWAndCase(t *testing.T) {
	cases := map[string]string{
		"https://WWW.Example.com/post": "example.com",
		"https://example.com/post":     "example.com",
		"https://blog.example.com/":    "blog.example.com",
		"nonsense":                     "",
	}
	for in, want := range cases {
		if got := siteOf(in); got != want {
			t.Errorf("siteOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sites never asked about come first, then the ones asked about longest ago. Without
// that order a sweep would keep re-reading the same sites and never reach the rest.
func TestStalePutsTheNeverCheckedFirst(t *testing.T) {
	now := time.Now().UTC()
	store := &Store{entries: map[string]Entry{
		"recent.example": {CheckedAt: now},
		"old.example":    {CheckedAt: now.Add(-30 * 24 * time.Hour)},
	}}

	got := store.Stale([]string{"recent.example", "old.example", "new.example"}, 2)

	if len(got) != 2 || got[0] != "new.example" || got[1] != "old.example" {
		t.Errorf("stale order = %v, want the unchecked site then the oldest", got)
	}
}

// A site that could not be read has to be marked as tried, or it stays at the head of
// the queue and starves everything behind it.
func TestSweepMarksSitesItCouldNotRead(t *testing.T) {
	hnFailing(t, http.StatusInternalServerError)

	store := &Store{entries: map[string]Entry{}}
	if got := store.Sweep(context.Background(), http.DefaultClient, []string{"example.com"}); got.Read != 0 || got.Failed != 1 {
		t.Errorf("reported %d read and %d failed, want none read and one failed", got.Read, got.Failed)
	}

	entry, ok := store.entries["example.com"]
	if !ok {
		t.Fatal("a site that failed was not recorded, so it will be retried forever")
	}
	if entry.CheckedAt.IsZero() {
		t.Error("a site that failed was left looking as though it had never been tried")
	}
}

// A lookup that failed says nothing about the site, and must not be written as though
// it had. Recording zeroes here is indistinguishable from a site nobody has ever
// posted, and because the entry is marked as checked in the same breath, that verdict
// stands until the site comes round again a full rotation later.
func TestSweepKeepsWhatItKnewWhenALookupFails(t *testing.T) {
	hnFailing(t, http.StatusTooManyRequests)

	before := time.Now().UTC().Add(-30 * 24 * time.Hour)
	store := &Store{entries: map[string]Entry{
		"known.example": {Points: 320, Stories: 4, CheckedAt: before},
	}}

	store.Sweep(context.Background(), http.DefaultClient, []string{"known.example"})

	entry := store.entries["known.example"]
	if entry.Points != 320 || entry.Stories != 4 {
		t.Errorf("a failed lookup overwrote what was known: %d points over %d stories, want 320 over 4",
			entry.Points, entry.Stories)
	}
	if !entry.CheckedAt.After(before) {
		t.Error("a failed lookup left the site looking unchecked, so it holds the head of the queue")
	}
	if got := store.Score("https://known.example/post"); got == 0 {
		t.Error("a failed lookup cost the site the standing it had already earned")
	}
}

func TestSweepRecordsWhatItLearns(t *testing.T) {
	hnServer(t, `{"hits":[{"url":"https://example.com/a","points":120}]}`)

	store := &Store{entries: map[string]Entry{}}
	if got := store.Sweep(context.Background(), http.DefaultClient, []string{"example.com"}); got.Read != 1 || got.Failed != 0 {
		t.Errorf("reported %d read and %d failed, want one read and none failed", got.Read, got.Failed)
	}

	if got := store.entries["example.com"]; got.Points != 120 || got.Stories != 1 {
		t.Errorf("recorded %d points over %d stories, want 120 over 1", got.Points, got.Stories)
	}
}

// fakeBlob stands in for storage, so the popularity map can be exercised without an
// Azure account.
type fakeBlob struct {
	data    string
	missing bool
	loadErr error

	uploads int
	saved   string
}

func (f *fakeBlob) DownloadString(_ context.Context, _, _ string) (string, error) {
	switch {
	case f.missing:
		return "", blob.ErrNotFound
	case f.loadErr != nil:
		return "", f.loadErr
	}
	return f.data, nil
}

func (f *fakeBlob) Upload(_ context.Context, _, _ string, data []byte) error {
	f.uploads++
	f.saved = string(data)
	return nil
}

// The first run has nothing to read, and popularity only ever adds, so knowing
// nothing is a state to score from rather than a failure.
func TestLoadTreatsAMissingBlobAsNothingKnown(t *testing.T) {
	store := NewStore(&fakeBlob{missing: true}, "sources", "popularity.json")

	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := store.Score("https://example.com/post"); got != 0 {
		t.Errorf("score = %.3f against an empty map, want 0", got)
	}
}

func TestSaveAndLoadRoundTripWhatWasLearned(t *testing.T) {
	storage := &fakeBlob{}
	written := NewStore(storage, "sources", "popularity.json")
	written.entries["example.com"] = Entry{Points: 320, Stories: 4, CheckedAt: time.Now().UTC()}

	if err := written.Save(context.Background()); err != nil {
		t.Fatalf("save: %v", err)
	}

	read := NewStore(&fakeBlob{data: storage.saved}, "sources", "popularity.json")
	if err := read.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got := read.entries["example.com"]; got.Points != 320 || got.Stories != 4 {
		t.Errorf("read back %d points over %d stories, want 320 over 4", got.Points, got.Stories)
	}
}

func TestLoadRejectsAnUnreadableMap(t *testing.T) {
	store := NewStore(&fakeBlob{data: "{not json"}, "sources", "popularity.json")

	if err := store.Load(context.Background()); err == nil {
		t.Error("a map that could not be parsed was accepted as empty")
	}
}
