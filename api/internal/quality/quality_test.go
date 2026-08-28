package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

// fakeIndex stands in for the search index, holding a corpus that shrinks as it is
// scored, which is the property the whole design rests on.
type fakeIndex struct {
	unscored []index.Candidate
	// undated is the other cohort. The real index keeps the two apart with mutually
	// exclusive filters, so no article is ever handed back by both.
	undated []index.Candidate
	saved   []index.Scores
	reads   int
	saveErr error
}

func (f *fakeIndex) Unscored(_ context.Context, _, limit int, cohort index.Cohort) ([]index.Candidate, int, error) {
	f.reads++

	pool := &f.unscored
	if cohort == index.Undated {
		pool = &f.undated
	}

	// The count the index reports includes the documents it is about to hand over.
	remaining := len(*pool)
	batch := (*pool)[:min(limit, len(*pool))]
	*pool = (*pool)[len(batch):]
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
// costs one query per cohort and stops.
func TestRunOnAJudgedCorpusDoesNothing(t *testing.T) {
	idx := &fakeIndex{}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 5000})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if idx.reads != cohorts {
		t.Errorf("read the index %d times, want one look per cohort that found nothing", idx.reads)
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
	// One empty look at the undated cohort, then the dated read whose write failed.
	if idx.reads != cohorts {
		t.Errorf("read the index %d times after a failed write, want %d", idx.reads, cohorts)
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

func (s *stubbornIndex) Unscored(_ context.Context, _, _ int, cohort index.Cohort) ([]index.Candidate, int, error) {
	// Everything it holds is dated, so the undated read finds nothing — as it would
	// against a real index, where the two cohorts cannot overlap.
	if cohort == index.Undated {
		return nil, 0, nil
	}

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

// cohorts is how many reads a run makes when there is nothing to do: the unscored set
// is read once per cohort, because the two cannot share an ordering.
const cohorts = 2

// undatedCorpus builds articles carrying no publication date. In the index these are
// the ones no ordering by date can reach.
func undatedCorpus(n int) []index.Candidate {
	out := make([]index.Candidate, n)
	for i := range out {
		out[i] = candidate(
			fmt.Sprintf("undated-%d", i),
			fmt.Sprintf("https://opengl-tutorial.org/tutorial-%d", i),
			fmt.Sprintf("Tutorial %d : Opening a window", i),
			"OpenGL Tutorial", "sitemap", repeat(prose, 3))
	}
	return out
}

// The failure this guards against is not a slow drain but a permanent one: read as a
// single newest-first set, an article with no date sits behind every article with one,
// and a corpus that grows faster than a run can judge it never reaches the end. Against
// the live index that was 163,219 articles, a seventh of the corpus, none of which had
// ever been judged.
func TestRunJudgesUndatedArticlesAlongsideDatedOnes(t *testing.T) {
	idx := &fakeIndex{unscored: corpus(4000), undated: undatedCorpus(400)}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 1000})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var undated int
	for _, s := range idx.saved {
		if strings.HasPrefix(s.ID, "undated-") {
			undated++
		}
	}

	// A quarter of the budget is reserved, and there is more than that waiting, so a
	// run should spend the whole reserve on them.
	if want := 1000 / undatedReserve; undated != want {
		t.Errorf("judged %d undated articles, want the reserved %d", undated, want)
	}
	// The rest of the budget still goes where it went before.
	if len(idx.saved) != 1000 {
		t.Errorf("judged %d articles in total, want the full budget of 1000", len(idx.saved))
	}
}

// The reserve is a backlog to clear, not a standing cost: once the undated are judged,
// the whole budget goes back to the dated cohort.
func TestUndatedReserveCostsNothingOnceDrained(t *testing.T) {
	idx := &fakeIndex{unscored: corpus(4000)}
	scorer := New(idx, nil, nil, Options{ScoreBatch: 1000})

	if err := scorer.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(idx.saved) != 1000 {
		t.Errorf("judged %d articles with no undated backlog, want the full budget of 1000", len(idx.saved))
	}
}
