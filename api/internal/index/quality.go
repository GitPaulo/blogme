package index

import (
	"context"
	"fmt"
	"net/http"
)

// This file is the index's side of quality scoring: reading the articles that have
// not been judged yet, and writing the figures back.
//
// The index is both the input and the output, which is what keeps the scorer free of
// any store of its own. There is no queue and no cursor: an article leaves the
// unscored set by being scored, so asking for the head of that set repeatedly walks
// the whole corpus and then stops. Rebuilding the index from blob storage empties
// every score with it, and the same loop simply fills them in again — the scores are
// derived from indexed text, so nothing is lost that cannot be recomputed.

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
// change most likely to be wanted, and holding the parts means it costs a read of
// three numbers per document rather than a re-read of every article's text.
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

// Unscored returns articles carrying no score, or one older than version, along with
// how many such articles remain in the whole index.
//
// Newest first, so a corpus that is still draining spends its effort where a reader
// is most likely to be looking. The count is returned because it is the only measure
// of progress a run has: rows handled says how much work was done, remaining says how
// much is left, and without the second a drain that has silently stopped advancing
// looks exactly like a healthy one.
func (i *Index) Unscored(ctx context.Context, version, limit int) ([]Candidate, int, error) {
	body := map[string]any{
		// A filter needs something to filter, and "*" matches every document without
		// ranking any of it.
		"search":  "*",
		"filter":  fmt.Sprintf("qualityVersion eq null or qualityVersion lt %d", version),
		"orderby": "publishedAt desc",
		"top":     limit,
		"count":   true,
		"select":  "id,url,title,author,origin,content",
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
// Upload would create a document from these fields alone if the article had since
// been deleted, and a document holding a score and no title or URL is one that can be
// returned by a search and rendered as an empty row. A merge onto nothing fails
// instead, which is the correct outcome for a score with no article under it.
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
