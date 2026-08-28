package index

import (
	"context"
	"fmt"
	"net/http"
)

// Candidate is one article awaiting a quality score, carrying the fields the score is
// computed from.
type Candidate struct {
	ID      string
	URL     string
	Title   string
	Author  string
	Origin  string
	Content string
}

// Scores is one article's quality figures.
//
// The parts are stored alongside the total on purpose. Reweighting the blend is the
// change most likely to be wanted, and holding the parts makes that a read of three
// numbers per document rather than a re-read of every article's text.
type Scores struct {
	ID         string
	Quality    float64
	Content    float64
	Popularity float64
	WordCount  int
	Version    int
}

type candidateResponse struct {
	Total int `json:"@odata.count"`
	Value []struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		Title   string `json:"title"`
		Author  string `json:"author"`
		Origin  string `json:"origin"`
		Content string `json:"content"`
	} `json:"value"`
}

// Cohort names which part of the unscored set to read.
//
// It exists because publishedAt is nullable and the set was read newest first. Under
// any ordering by date an undated article sorts behind every dated one, so in a corpus
// that gains documents faster than a run can judge them it is not merely last — it is
// never reached. Read as one set, 163,219 undated articles, a seventh of the corpus,
// had never been judged at all, and sampling them turned up ordinary tutorials rather
// than the landing pages the ordering had been assumed to be burying.
type Cohort int

const (
	// Dated reads articles carrying a publication date, newest first, so that a post
	// published this hour is judged on the next pass rather than behind the backlog.
	Dated Cohort = iota
	// Undated reads the rest, in whatever order the index offers. There is nothing to
	// sort them by, which is the whole reason they need a read of their own.
	Undated
)

// Unscored returns articles from one cohort carrying no score, or one older than
// version, along with how many such articles remain in that cohort.
//
// The index is both the input and the output of scoring, which keeps the scorer free
// of any store of its own. There is no queue and no cursor: an article leaves the
// unscored set by being scored, so asking for the head of that set repeatedly walks
// the whole corpus and then stops. Rebuilding the index empties every score with it,
// and the same loop fills them in again.
//
// The count is per cohort, and the caller sums them: it is the only measure of
// progress a run has, and without it a drain that has silently stopped advancing
// looks exactly like a healthy one.
func (i *Index) Unscored(ctx context.Context, version, limit int, cohort Cohort) ([]Candidate, int, error) {
	// Built from the version and a fixed cohort rather than from anything a caller
	// spells, so no filter can be composed from outside this package.
	unjudged := fmt.Sprintf("(qualityVersion eq null or qualityVersion lt %d)", version)

	body := map[string]any{
		// A filter needs something to filter, and "*" matches every document without
		// ranking any of it.
		"search": "*",
		"top":    limit,
		"count":  true,
		"select": "id,url,title,author,origin,content",
	}

	if cohort == Undated {
		body["filter"] = unjudged + " and publishedAt eq null"
	} else {
		body["filter"] = unjudged + " and publishedAt ne null"
		body["orderby"] = "publishedAt desc"
	}

	var resp candidateResponse
	if err := i.do(ctx, http.MethodPost, "/docs/search", body, &resp); err != nil {
		return nil, 0, fmt.Errorf("read unscored articles: %w", err)
	}

	out := make([]Candidate, 0, len(resp.Value))
	for _, v := range resp.Value {
		out = append(out, Candidate{
			ID:      v.ID,
			URL:     v.URL,
			Title:   v.Title,
			Author:  v.Author,
			Origin:  v.Origin,
			Content: v.Content,
		})
	}
	return out, resp.Total, nil
}

// scoreDocument is one article's scores on the wire.
//
// The action is "merge" rather than the "mergeOrUpload" the crawler writes with.
// Upload would create a document from these fields alone if the article had since been
// deleted, and a document holding a score but no title or URL can be returned by a
// search and rendered as an empty row. A merge onto nothing fails instead.
type scoreDocument struct {
	Action      string  `json:"@search.action"`
	ID          string  `json:"id"`
	Quality     float64 `json:"quality"`
	QContent    float64 `json:"qContent"`
	QPopularity float64 `json:"qPopularity"`
	WordCount   int     `json:"wordCount"`
	Version     int     `json:"qualityVersion"`
}

// SaveScores writes quality figures onto articles that already exist.
func (i *Index) SaveScores(ctx context.Context, scores []Scores) error {
	for start := 0; start < len(scores); start += maxBatch {
		end := min(start+maxBatch, len(scores))

		docs := make([]scoreDocument, 0, end-start)
		for _, s := range scores[start:end] {
			docs = append(docs, scoreDocument{
				Action:      "merge",
				ID:          s.ID,
				Quality:     s.Quality,
				QContent:    s.Content,
				QPopularity: s.Popularity,
				WordCount:   s.WordCount,
				Version:     s.Version,
			})
		}

		if err := i.do(ctx, http.MethodPost, "/docs/index", map[string]any{"value": docs}, nil); err != nil {
			return fmt.Errorf("save %d scores: %w", len(docs), err)
		}
	}

	return nil
}
