package discovery

import (
	"bytes"
	"encoding/xml"
	"html"
	"io"
	"strings"
	"time"
	"unicode"
)

// feedItem is one post as described by a feed, before any page fetch.
type feedItem struct {
	Title      string
	Link       string
	Author     string
	Summary    string
	Content    string
	Categories []string
	Published  time.Time
}

// atomLink is one <link> of an Atom entry.
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

// feedDoc covers RSS 2.0 (https://www.rssboard.org/rss-specification) and Atom
// (RFC 4287) in one shape. The two formats overlap enough that separate parsers would
// be redundant.
type feedDoc struct {
	// RSS
	Channel struct {
		Items []struct {
			Title       string   `xml:"title"`
			Link        string   `xml:"link"`
			Description string   `xml:"description"`
			Encoded     string   `xml:"encoded"`
			Creator     string   `xml:"creator"`
			Author      string   `xml:"author"`
			PubDate     string   `xml:"pubDate"`
			Date        string   `xml:"date"`
			Categories  []string `xml:"category"`
		} `xml:"item"`
	} `xml:"channel"`

	// Atom
	Entries []struct {
		Title      string     `xml:"title"`
		Links      []atomLink `xml:"link"`
		Summary    string     `xml:"summary"`
		Content    string     `xml:"content"`
		Published  string     `xml:"published"`
		Updated    string     `xml:"updated"`
		Categories []struct {
			Term  string `xml:"term,attr"`
			Label string `xml:"label,attr"`
		} `xml:"category"`
		Author struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

// newXMLDecoder reads XML the way it is published rather than the way it is specified.
// Feeds and sitemaps in the wild are frequently not well-formed, and many declare an
// encoding they do not use, so the declared charset is ignored and the bytes are taken
// as they come.
func newXMLDecoder(raw []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	return decoder
}

// parseFeed reads RSS or Atom into a common item list.
func parseFeed(raw []byte) ([]feedItem, error) {
	var doc feedDoc
	if err := newXMLDecoder(raw).Decode(&doc); err != nil {
		return nil, err
	}

	var items []feedItem

	for _, it := range doc.Channel.Items {
		items = append(items, feedItem{
			Title:      cleanText(it.Title),
			Link:       strings.TrimSpace(it.Link),
			Author:     cleanText(firstNonEmpty(it.Creator, it.Author)),
			Summary:    cleanText(it.Description),
			Content:    firstNonEmpty(it.Encoded, it.Description),
			Categories: it.Categories,
			Published:  parseTime(firstNonEmpty(it.PubDate, it.Date)),
		})
	}

	for _, e := range doc.Entries {
		categories := make([]string, 0, len(e.Categories))
		for _, c := range e.Categories {
			categories = append(categories, firstNonEmpty(c.Label, c.Term))
		}
		items = append(items, feedItem{
			Title:      cleanText(e.Title),
			Link:       entryLink(e.Links),
			Author:     cleanText(e.Author.Name),
			Summary:    cleanText(e.Summary),
			Content:    firstNonEmpty(e.Content, e.Summary),
			Categories: categories,
			Published:  parseTime(firstNonEmpty(e.Published, e.Updated)),
		})
	}

	return items, nil
}

// entryLink prefers the alternate HTML link, which is the human-readable post.
func entryLink(links []atomLink) string {
	for _, l := range links {
		if l.Rel == "alternate" || l.Rel == "" {
			return strings.TrimSpace(l.Href)
		}
	}
	if len(links) > 0 {
		return strings.TrimSpace(links[0].Href)
	}
	return ""
}

// Feeds use several date formats and many get them subtly wrong.
var timeFormats = []string{
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func parseTime(v string) time.Time {
	v = strings.TrimSpace(v)
	for _, f := range timeFormats {
		if t, err := time.Parse(f, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// cleanText strips markup and normalises whitespace for short display fields.
func cleanText(v string) string {
	return normaliseSpace(html.UnescapeString(stripTags(v)))
}

// Categories that file a post without saying anything about it.
var uninformativeCategories = map[string]bool{
	"uncategorized": true, "uncategorised": true, "general": true, "other": true,
	"misc": true, "miscellaneous": true, "blog": true, "blogs": true,
	"post": true, "posts": true, "article": true, "articles": true,
	"writing": true, "all": true, "featured": true, "default": true,
}

const (
	// Feed categories are an uncontrolled vocabulary, so a post carrying a dozen of
	// them would otherwise dominate the topic list on its own.
	maxFeedCategories = 3
	maxArticleTopics  = 8

	// Long enough for "distributed-systems", short enough to reject a sentence used as
	// a category.
	maxTopicLength = 40

	// Words a topic may have. Real subjects are one or two words and occasionally
	// three; anything longer is a phrase the author filed a post under, which nobody
	// will pick out of a filter list.
	maxTopicWords = 3
)

// articleTopics is what a post is about: the categories its author filed it under, on
// top of what the blog as a whole writes about.
//
// The source's tags are the same for every post it publishes, so on their own they
// cannot tell two posts apart. A feed category is the only per-post subject signal
// available without reading the post, and it is the author's own label.
func articleTopics(sourceTags, categories []string) []string {
	topics := make([]string, 0, len(sourceTags)+maxFeedCategories)
	seen := make(map[string]bool, len(sourceTags)+maxFeedCategories)

	for _, t := range sourceTags {
		if t != "" && !seen[t] {
			seen[t] = true
			topics = append(topics, t)
		}
	}

	added := 0
	for _, c := range categories {
		if added >= maxFeedCategories || len(topics) >= maxArticleTopics {
			break
		}
		slug := topicSlug(c)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		topics = append(topics, slug)
		added++
	}

	return topics
}

// topicSlug puts a category into the same lowercase kebab-case the source list uses, so
// "Software Engineering" and "software-engineering" are one topic. Returns empty for
// anything that would not be worth filtering by.
func topicSlug(v string) string {
	var sb strings.Builder
	sb.Grow(len(v))
	dash, mangled := false, false
	for _, r := range strings.ToLower(strings.TrimSpace(html.UnescapeString(v))) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			dash = false
		default:
			// A letter this loop cannot keep is a letter the slug ends up missing, and
			// what survives is no longer the word the author wrote: "Grupo de Usuários"
			// came through as "grupo-de-usu-rios". A mangled word is not a subject
			// anyone will filter by, and nothing removes it once it is in the
			// vocabulary.
			if unicode.IsLetter(r) {
				mangled = true
			}
			if sb.Len() > 0 && !dash {
				sb.WriteByte('-')
				dash = true
			}
		}
	}

	slug := strings.Trim(sb.String(), "-")
	switch {
	case len(slug) < 2, len(slug) > maxTopicLength:
		return ""
	case mangled, strings.Count(slug, "-") >= maxTopicWords:
		return ""
	case uninformativeCategories[slug]:
		return ""
	}
	return slug
}
