package discovery

import (
	"encoding/xml"
	"html"
	"io"
	"strings"
	"time"
)

// feedItem is one post as described by a feed, before any page fetch.
type feedItem struct {
	Title     string
	Link      string
	Author    string
	Summary   string
	Content   string
	Published time.Time
}

// feedDoc covers RSS 2.0 and Atom in one shape. The two formats overlap enough that
// separate parsers would be redundant.
type feedDoc struct {
	XMLName xml.Name `xml:"-"`

	// RSS
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Encoded     string `xml:"encoded"`
			Creator     string `xml:"creator"`
			Author      string `xml:"author"`
			PubDate     string `xml:"pubDate"`
			Date        string `xml:"date"`
		} `xml:"item"`
	} `xml:"channel"`

	// Atom
	Title   string `xml:"title"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
			Type string `xml:"type,attr"`
		} `xml:"link"`
		Summary   string `xml:"summary"`
		Content   string `xml:"content"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
		Author    struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

// parseFeed reads RSS or Atom into a common item list.
func parseFeed(raw []byte) ([]feedItem, error) {
	var doc feedDoc
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	// Feeds in the wild are frequently not well-formed or are latin-1 encoded.
	decoder.Strict = false
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}

	var items []feedItem

	for _, it := range doc.Channel.Items {
		items = append(items, feedItem{
			Title:     cleanText(it.Title),
			Link:      strings.TrimSpace(it.Link),
			Author:    cleanText(firstNonEmpty(it.Creator, it.Author)),
			Summary:   cleanText(it.Description),
			Content:   firstNonEmpty(it.Encoded, it.Description),
			Published: parseTime(firstNonEmpty(it.PubDate, it.Date)),
		})
	}

	for _, e := range doc.Entries {
		items = append(items, feedItem{
			Title:     cleanText(e.Title),
			Link:      atomLink(e.Links),
			Author:    cleanText(e.Author.Name),
			Summary:   cleanText(e.Summary),
			Content:   firstNonEmpty(e.Content, e.Summary),
			Published: parseTime(firstNonEmpty(e.Published, e.Updated)),
		})
	}

	return items, nil
}

// atomLink prefers the alternate HTML link, which is the human-readable post.
func atomLink(links []struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}) string {
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
