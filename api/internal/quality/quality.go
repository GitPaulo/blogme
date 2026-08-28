// Package quality judges how good an article is, independently of any query.
//
// Search answers two questions at once: does this document match what was asked for,
// and is it worth reading at all. The index answers the first well and the second not
// at all, which is why a search for "python" returned documentation landing pages,
// newsletter archives and a Portuguese meetup announcement from 2007 ahead of articles
// about Python. This package answers the second, once per article, so ranking can use
// it on every query without paying for it on any of them.
package quality

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/GitPaulo/blogme/api/internal/index"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

// Azure AI Search returns at most 1000 documents to one query, so a run's budget is
// spent a thousand at a time rather than in one request.
const maxSearchTop = 1000

// scoreIndex is what scoring needs of the search index: the articles not yet judged,
// and somewhere to put the verdict.
//
// An interface rather than *index.Index so the job can be exercised without an Azure
// account behind it, matching how discovery takes its store.
type scoreIndex interface {
	Unscored(ctx context.Context, version, limit int, cohort index.Cohort) ([]index.Candidate, int, error)
	SaveScores(ctx context.Context, scores []index.Scores) error
}

// undatedReserve is the fraction of a run's budget kept for articles with no
// publication date: one part in this many.
//
// They are about a seventh of the corpus, so a quarter is more than their share. That
// is deliberate — the reserve is a backlog to clear, not a standing cost. Once the
// undated are judged their read returns nothing and the whole budget goes where it
// went before.
const undatedReserve = 4

// Scorer keeps every article's quality figures up to date.
//
// It has no queue and no cursor. An article leaves the unscored set by being scored, so
// each run takes the head of that set and the set shrinks by exactly what was done: a
// corpus of any size drains in as many runs as it takes and then costs nothing but the
// asking. Raising Version puts every article back into the set, which is how a change
// to the model reaches articles judged under the old one.
type Scorer struct {
	index      scoreIndex
	sources    sources.Provider
	popularity *Store

	client     *http.Client
	scoreBatch int
	sweepBatch int
}

// Options bound how much one run does.
type Options struct {
	// ScoreBatch is how many articles a run may judge.
	ScoreBatch int
	// SweepBatch is how many sites a run may ask Hacker News about. Zero turns
	// popularity gathering off, leaving articles judged on their own text.
	SweepBatch int
}

func New(idx scoreIndex, provider sources.Provider, popularity *Store, opts Options) *Scorer {
	return &Scorer{
		index:      idx,
		sources:    provider,
		popularity: popularity,
		// Third-party and off the reader's path, so it can afford to be patient, but
		// not indefinitely, or one unresponsive host holds up a sweep.
		client:     &http.Client{Timeout: 15 * time.Second},
		scoreBatch: opts.ScoreBatch,
		sweepBatch: opts.SweepBatch,
	}
}

// Run performs one bounded pass: gather some popularity, then judge some articles.
func (s *Scorer) Run(ctx context.Context) error {
	started := time.Now()

	// Popularity first, so anything learned this run is available to the articles
	// judged in it. Failing at it is not a reason to skip scoring: it only ever adds
	// to a score, so its absence costs an article nothing it had.
	swept := s.gatherPopularity(ctx)

	scored, remaining, err := s.score(ctx)
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "quality pass complete",
		"scored", scored,
		"remaining", remaining,
		"sites_swept", swept,
		"version", Version,
		"duration_ms", time.Since(started).Milliseconds())
	return nil
}

// gatherPopularity makes what is known about each site available to this run, learns a
// little more, and reports how many sites it read.
//
// Reading and gathering are separate steps because they fail and switch off separately.
// Turning the sweep off must not also stop the scores already gathered from being used,
// and a failed read must not be followed by a write: saving a sweep on top of a map that
// could not be loaded would replace everything known with whatever this run fetched.
func (s *Scorer) gatherPopularity(ctx context.Context) int {
	if s.popularity == nil {
		return 0
	}

	if err := s.popularity.Load(ctx); err != nil {
		slog.WarnContext(ctx, "popularity unavailable, scoring on text alone", "error", err)
		return 0
	}

	if s.sweepBatch <= 0 || s.sources == nil {
		return 0
	}

	read, err := s.sweep(ctx)
	if err != nil {
		slog.WarnContext(ctx, "popularity sweep failed", "error", err)
	}
	return read
}

// sweep reads the standing of the sites that have gone longest without being checked.
func (s *Scorer) sweep(ctx context.Context) (int, error) {
	list, err := s.sources.Load(ctx)
	if err != nil {
		return 0, fmt.Errorf("load sources: %w", err)
	}

	sites := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, src := range list {
		site := siteOf(src.Site)
		if site == "" {
			continue
		}
		// Several sources can share a host, and asking about it more than once in a
		// run would spend the budget on the same answer.
		if _, repeat := seen[site]; repeat {
			continue
		}
		seen[site] = struct{}{}
		sites = append(sites, site)
	}

	read := s.popularity.Sweep(ctx, s.client, s.popularity.Stale(sites, s.sweepBatch))
	if err := s.popularity.Save(ctx); err != nil {
		return read, err
	}
	return read, nil
}

// score judges articles until the run's budget is spent or none are left, reporting how
// many it judged and how many remain in the corpus.
//
// The budget is split because the two cohorts cannot compete for the same read: an
// article with no date sorts behind every article with one, so a single newest-first
// read starves it forever. Undated goes first with a reserved slice, and whatever it
// leaves unspent falls through to the dated read rather than being lost, so reserving
// costs a run nothing once the undated are done.
func (s *Scorer) score(ctx context.Context) (int, int, error) {
	undated, undatedLeft, err := s.drain(ctx, index.Undated, s.scoreBatch/undatedReserve)
	if err != nil {
		return undated, undatedLeft, err
	}

	dated, datedLeft, err := s.drain(ctx, index.Dated, s.scoreBatch-undated)
	return undated + dated, undatedLeft + datedLeft, err
}

// drain judges one cohort until its budget is spent or it runs out.
//
// The articles already handled are remembered, because a score is not visible to the
// next query the moment it is accepted. Without that, a run reads the same head of the
// queue over and over while indexing catches up: judging two articles spent nineteen
// rounds and reported thirty-eight against a real service. Nothing was written wrongly,
// but the budget went on repeats and the count became fiction.
func (s *Scorer) drain(ctx context.Context, cohort index.Cohort, budget int) (int, int, error) {
	scored, remaining := 0, 0
	seen := make(map[string]struct{}, budget)

	for scored < budget {
		candidates, left, err := s.index.Unscored(ctx, Version, min(maxSearchTop, budget-scored), cohort)
		if err != nil {
			return scored, remaining, err
		}

		scores := make([]index.Scores, 0, len(candidates))
		for _, c := range candidates {
			if _, repeat := seen[c.ID]; repeat {
				continue
			}
			seen[c.ID] = struct{}{}
			scores = append(scores, Judge(c, s.popularity.Score(c.URL)))
		}

		// Either the queue is empty or everything in it was judged a moment ago and
		// the index has yet to say so. Both mean this run is finished; the next one
		// picks up whatever is genuinely left.
		//
		// The count is left as it was, deliberately. A read that returned only
		// repeats is a read of a stale index, and taking a figure from it would
		// report work already done as still outstanding.
		if len(scores) == 0 {
			break
		}

		if err := s.index.SaveScores(ctx, scores); err != nil {
			return scored, remaining, err
		}

		scored += len(scores)
		remaining = max(left-len(scores), 0)
	}

	return scored, remaining, nil
}
