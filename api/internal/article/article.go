package article

import "time"

// Article is the canonical representation of a blog post. Azure Blob Storage holds
// this shape as the source of truth; the search index is a projection of it.
type Article struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	SourceID    string    `json:"sourceId"`
	Summary     string    `json:"summary,omitempty"`
	Content     string    `json:"content,omitempty"`
	Topics      []string  `json:"topics,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	FetchedAt   time.Time `json:"fetchedAt,omitzero"`
}

// Result is the trimmed projection returned by search, matching the fields the
// high-level plan specifies for a result row.
type Result struct {
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Topics      []string  `json:"topics,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	Score       float64   `json:"score"`
}
