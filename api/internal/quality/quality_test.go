package quality

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

// fakeIndex stands in for the search index, holding a corpus that shrinks as it is
// scored, which is the property the whole design rests on.
type fakeIndex struct {
	unscored []index.Candidate
	saved    []index.Scores
	reads    int
	saveErr  error
}

func (f *fakeIndex) Unscored(_ context.Context, _, limit int) ([]index.Candidate, int, error) {
	f.reads++

	// The count the index reports includes the documents it is about to hand over.
	remaining := len(f.unscored)
	batch := f.unscored[:min(limit, len(f.unscored))]
	f.unscored = f.unscored[len(batch):]
	return batch, remaining, nil
}

func (f *fakeIndex) SaveScores(_ context.Context, scores []index.Scores) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, scores...)
	return nil
}

func corpus(n int) []index.Candidate {
	out := make([]index.Candidate, n)
	for i := range out {
		out[i] = candidate(
			fmt.Sprintf("post-%d", i),
			fmt.Sprintf("https://example.com/blog/post-%d", i),
			fmt.Sprintf("Post number %d", i),
			"Someone", "feed", repeat(prose, 3))
	}
	return out
}

// A run reads a thousand at a time because that is all one query returns, and keeps
// going until its budget is spent or the corpus is judged.
func TestRunScoresTheWholeCorpusAcrossSeveralReads(t *testing.T) {
	idx := &fakeIndex{unscored: corpus(2500)}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 5000})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(idx.saved) != 2500 {
		t.Errorf("scored %d articles, want the whole corpus of 2500", len(idx.saved))
	}
	if idx.reads < 3 {
		t.Errorf("read the index %d times, want at least three: a query returns at most %d",
			idx.reads, maxSearchTop)
	}
	for _, s := range idx.saved {
		if s.Version != Version {
			t.Fatalf("a score carried version %d, want %d", s.Version, Version)
		}
	}
}

// The budget is what keeps a pass inside its invocation, so it has to bind even when
// there is far more work available.
func TestRunStopsAtItsBudget(t *testing.T) {
	idx := &fakeIndex{unscored: corpus(4000)}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 1500})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(idx.saved) != 1500 {
		t.Errorf("scored %d articles, want the budgeted 1500", len(idx.saved))
	}
	if len(idx.unscored) != 2500 {
		t.Errorf("%d articles left unscored, want 2500 waiting for the next pass", len(idx.unscored))
	}
}

// An empty set is the resting state, not an error: once a corpus is judged, every run
// costs one query and stops.
func TestRunOnAJudgedCorpusDoesNothing(t *testing.T) {
	idx := &fakeIndex{}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 5000})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if idx.reads != 1 {
		t.Errorf("read the index %d times, want one look that found nothing", idx.reads)
	}
	if len(idx.saved) != 0 {
		t.Errorf("wrote %d scores against an empty corpus", len(idx.saved))
	}
}

// A write that fails must stop the pass rather than spin: the articles it could not
// score are still unscored, so the next run picks them up unchanged.
func TestRunStopsWhenScoresCannotBeWritten(t *testing.T) {
	failure := errors.New("index unavailable")
	idx := &fakeIndex{unscored: corpus(50), saveErr: failure}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 5000})

	if err := scorer.Run(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("run returned %v, want the write failure", err)
	}
	if idx.reads != 1 {
		t.Errorf("read the index %d times after a failed write, want one", idx.reads)
	}
}

// Popularity is a bonus, so a scorer without it still scores. This is also the state
// every run is in before the first sweep finishes.
func TestRunScoresWithoutPopularity(t *testing.T) {
	idx := &fakeIndex{unscored: corpus(10)}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 100, SweepBatch: 0})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(idx.saved) != 10 {
		t.Fatalf("scored %d articles, want 10", len(idx.saved))
	}
	for _, s := range idx.saved {
		if s.Popularity != 0 {
			t.Fatalf("popularity = %.3f with nothing gathered, want 0", s.Popularity)
		}
		if s.Quality <= 0 {
			t.Fatalf("quality = %.3f without popularity, want the article judged on its text", s.Quality)
		}
	}
}

// fakeSources is the approved list, without a file or a storage account behind it.
type fakeSources []sources.Source

func (f fakeSources) Load(context.Context) ([]sources.Source, error) { return f, nil }

// Saving a sweep on top of a map that could not be read would replace everything known
// about every site with whatever this one run happened to fetch. The sweep has to not
// happen at all.
func TestRunDoesNotOverwritePopularityItCouldNotRead(t *testing.T) {
	storage := &fakeBlob{loadErr: errors.New("storage unavailable")}
	idx := &fakeIndex{unscored: corpus(5)}

	scorer := New(idx, fakeSources{{ID: "one", Site: "https://example.com"}},
		NewStore(storage, "sources", "popularity.json"),
		Options{ScoreBatch: 100, SweepBatch: 10})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if storage.uploads != 0 {
		t.Errorf("wrote the popularity map %d times after failing to read it", storage.uploads)
	}
	// Popularity is a bonus, so losing it must not cost the corpus its scoring.
	if len(idx.saved) != 5 {
		t.Errorf("scored %d articles, want 5: popularity failing is not a reason to stop", len(idx.saved))
	}
}

// Sites come from the approved list, and one host listed twice must not be asked about
// twice in the same pass.
func TestRunSweepsEachSiteOnce(t *testing.T) {
	hnServer(t, `{"hits":[{"url":"https://example.com/a","points":90}]}`)

	storage := &fakeBlob{missing: true}
	store := NewStore(storage, "sources", "popularity.json")
	idx := &fakeIndex{unscored: corpus(1)}

	scorer := New(idx, fakeSources{
		{ID: "one", Site: "https://example.com/blog"},
		{ID: "two", Site: "https://www.example.com/"},
		{ID: "three", Site: "https://other.example/"},
	}, store, Options{ScoreBatch: 10, SweepBatch: 10})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(store.entries) != 2 {
		t.Errorf("asked about %d sites, want 2: www is not a different blog", len(store.entries))
	}
	if storage.uploads != 1 {
		t.Errorf("saved the map %d times, want once", storage.uploads)
	}
}

// stubbornIndex keeps handing back the same articles, the way a real index does for a
// moment after a write: scores are accepted before they are searchable.
type stubbornIndex struct {
	batch []index.Candidate
	saved int
	reads int
}

func (s *stubbornIndex) Unscored(context.Context, int, int) ([]index.Candidate, int, error) {
	s.reads++
	return s.batch, len(s.batch), nil
}

func (s *stubbornIndex) SaveScores(_ context.Context, scores []index.Scores) error {
	s.saved += len(scores)
	return nil
}

// A run must not spend its budget rewriting the same articles while indexing catches
// up, and must not report having done so as progress.
func TestRunDoesNotRescoreWhatItJustScored(t *testing.T) {
	idx := &stubbornIndex{batch: corpus(3)}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 1000})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if idx.saved != 3 {
		t.Errorf("wrote %d scores for 3 articles, want 3", idx.saved)
	}
	if idx.reads != 2 {
		t.Errorf("read the index %d times, want two: one that found work and one that found only repeats", idx.reads)
	}
}
