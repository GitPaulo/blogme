package article

import "time"

// How an article was found. Sitemap entries are described by the page itself rather
// than by a feed, so their metadata is less dependable and the UI says so.
const (
	OriginFeed    = "feed"
	OriginSitemap = "sitemap"
)

// Article is the canonical representation of a blog post. Azure Blob Storage holds
// this shape as the source of truth; the search index is a projection of it.
type Article struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	SourceID    string    `json:"sourceId"`
	Origin      string    `json:"origin,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Content     string    `json:"content,omitempty"`
	Topics      []string  `json:"topics,omitempty"`
	Kind        []string  `json:"kind,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	FetchedAt   time.Time `json:"fetchedAt,omitzero"`

	// FramingDenied records whether the page's own headers refuse to let it be shown
	// inside a frame, which is what the web app's hover preview puts it in. Recorded
	// from the response the crawler already had, so it is nil wherever the page was
	// never fetched — a feed carrying its posts in full is never read from source —
	// and on everything indexed before this was looked at. Nil means unknown, and the
	// preview treats unknown the way it treated every link before this existed.
	FramingDenied *bool `json:"framingDenied,omitempty"`
}

// Result is the trimmed projection returned by search, matching the fields the
// high-level plan specifies for a result row.
type Result struct {
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	Origin      string    `json:"origin,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Topics      []string  `json:"topics,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	Score       float64   `json:"score"`

	// FramingDenied is Article.FramingDenied, carried through so the preview knows
	// whether to try. Nil is unknown rather than allowed.
	FramingDenied *bool `json:"framingDenied,omitempty"`
}
