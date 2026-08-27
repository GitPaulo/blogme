package discovery

import (
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// parseHTML parses a page into the tree the extractors below all read from.
//
// Every extractor takes the tree rather than the source, because a page body can be
// megabytes and this is the crawler's hottest path: parsing once per field cost three
// times as much for the same result.
func parseHTML(doc string) *html.Node {
	node, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		// Unreachable with a strings.Reader, which cannot fail a read. An empty
		// document keeps every caller free of nil checks.
		return &html.Node{Type: html.DocumentNode}
	}
	return node
}

// extractText pulls readable prose out of a parsed page.
//
// Deliberately not a full readability implementation: it drops the elements that
// reliably contain no prose, prefers <article>/<main> when present, and takes the
// document body otherwise. Good enough to index, cheap to run.
func extractText(node *html.Node) string {
	if main := findContentRoot(node); main != nil {
		node = main
	}

	var sb strings.Builder
	collectText(node, &sb)
	return cleanProse(normaliseSpace(sb.String()))
}

// findContentRoot returns <article> or <main> if the page has one.
func findContentRoot(n *html.Node) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && (n.DataAtom == atom.Article || n.DataAtom == atom.Main) {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

// Elements whose contents are never article prose. Code blocks are excluded because
// source listings are not readable description text and they skew relevance.
var skipElements = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Nav: true, atom.Header: true,
	atom.Footer: true, atom.Aside: true, atom.Form: true, atom.Button: true,
	atom.Noscript: true, atom.Iframe: true, atom.Svg: true, atom.Select: true,
	atom.Pre: true, atom.Code: true, atom.Figcaption: true,
}

// Classes marking content that exists for anchors or assistive technology rather
// than for reading: heading permalinks and visually-hidden labels.
var hiddenClassHints = []string{
	"anchor", "headerlink", "header-link", "heading-link", "permalink", "hash-link",
	"sr-only", "visually-hidden", "visuallyhidden", "screen-reader", "screenreader",
}

func skipNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if skipElements[n.DataAtom] {
		return true
	}

	for _, attr := range n.Attr {
		switch attr.Key {
		case "aria-hidden":
			if attr.Val == "true" {
				return true
			}
		case "hidden":
			return true
		case "class":
			class := strings.ToLower(attr.Val)
			for _, hint := range hiddenClassHints {
				if strings.Contains(class, hint) {
					return true
				}
			}
		}
	}
	return false
}

func collectText(n *html.Node, sb *strings.Builder) {
	if skipNode(n) {
		return
	}

	if n.Type == html.TextNode {
		sb.WriteString(n.Data)
		sb.WriteByte(' ')
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, sb)
	}
}

// extractSummary returns the article's opening prose, taken from paragraphs only.
// Using <p> rather than all text avoids headings, navigation crumbs and code
// listings leaking into the description shown on a result card.
func extractSummary(node *html.Node, words int) string {
	if root := findContentRoot(node); root != nil {
		node = root
	}

	var sb strings.Builder
	kept := 0 // Tracked rather than recounted: sb is re-scanned at every node otherwise.
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if skipNode(n) || kept >= words {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.P {
			var para strings.Builder
			collectText(n, &para)
			if text := cleanProse(para.String()); text != "" {
				sb.WriteString(text)
				sb.WriteByte(' ')
				kept += wordCount(text)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)

	return truncateWords(sb.String(), words)
}

// noIndex reports whether the page asks not to be indexed.
//
// robots.txt governs whether a page may be fetched at all; this is the separate request
// not to keep what was fetched, and it is only visible once the page has been read.
// https://developer.mozilla.org/docs/Web/HTML/Element/meta/name#robots
func noIndex(node *html.Node) bool {
	found := false

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Meta {
			var name, content string
			for _, attr := range n.Attr {
				switch attr.Key {
				case "name":
					name = strings.ToLower(strings.TrimSpace(attr.Val))
				case "content":
					content = strings.ToLower(attr.Val)
				}
			}
			// The generic directive, and the form naming this crawler specifically.
			if name == "robots" || name == userAgentToken {
				for _, directive := range strings.Split(content, ",") {
					if strings.TrimSpace(directive) == "noindex" {
						found = true
						return
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)

	return found
}

// declaredFeeds returns the feed URLs a page advertises through <link rel="alternate">,
// in document order and exactly as written, so the caller can resolve them against
// whatever base it fetched the page from.
//
// Matched on the type containing a syndication format rather than on an exact media
// type, because that attribute is written by hand as often as by a generator. A false
// positive costs one fetch that fails to parse as a feed; a false negative costs the
// whole blog.
func declaredFeeds(node *html.Node) []string {
	var feeds []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Link {
			var rel, mediaType, href string
			for _, attr := range n.Attr {
				switch attr.Key {
				case "rel":
					rel = strings.ToLower(attr.Val)
				case "type":
					mediaType = strings.ToLower(attr.Val)
				case "href":
					href = strings.TrimSpace(attr.Val)
				}
			}
			if href != "" && strings.Contains(rel, "alternate") && isFeedType(mediaType) {
				feeds = append(feeds, href)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)

	return feeds
}

// Fragments that mark a <link type> as a syndication format.
var feedTypeHints = []string{"rss", "atom", "feed", "xml"}

// isFeedType reports whether a <link type> names a syndication format.
func isFeedType(mediaType string) bool {
	for _, name := range feedTypeHints {
		if strings.Contains(mediaType, name) {
			return true
		}
	}
	return false
}

// pageMeta is what a bare HTML page can say about itself. A sitemap entry has no
// feed record behind it, so the page's own metadata is all there is.
type pageMeta struct {
	Title     string
	Published time.Time
}

// Meta names and properties that carry a publication date, best first.
var publishedKeys = []string{
	"article:published_time", "datepublished", "article:published",
	"date", "dc.date.issued", "dc.date", "pubdate", "publish_date",
}

// Meta names and properties that carry a clean title. The <title> element usually
// has the site name bolted on, so it is the last resort.
var titleKeys = []string{"og:title", "twitter:title"}

// extractMeta reads the page's title and publication date in one walk.
func extractMeta(node *html.Node) pageMeta {
	metas := make(map[string]string)
	var title, heading, timestamp string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Meta:
				var key, content string
				for _, attr := range n.Attr {
					switch attr.Key {
					case "name", "property", "itemprop":
						key = strings.ToLower(strings.TrimSpace(attr.Val))
					case "content":
						content = attr.Val
					}
				}
				if key != "" && content != "" && metas[key] == "" {
					metas[key] = content
				}
			case atom.Title:
				if title == "" {
					var sb strings.Builder
					collectText(n, &sb)
					title = cleanText(sb.String())
				}
			case atom.H1:
				if heading == "" {
					var sb strings.Builder
					collectText(n, &sb)
					heading = cleanText(sb.String())
				}
			case atom.Time:
				for _, attr := range n.Attr {
					if attr.Key == "datetime" && timestamp == "" {
						timestamp = attr.Val
					}
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)

	meta := pageMeta{Title: firstNonEmpty(metaValue(metas, titleKeys), heading, title)}

	if raw := firstNonEmpty(metaValue(metas, publishedKeys), timestamp); raw != "" {
		meta.Published = parseTime(cleanText(raw))
	}
	return meta
}

func metaValue(metas map[string]string, keys []string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(metas[key]); v != "" {
			return v
		}
	}
	return ""
}

// cleanProse removes leftover Markdown markers, which appear when a feed publishes
// raw Markdown as its content.
func cleanProse(s string) string {
	fields := strings.Fields(s)
	kept := fields[:0]
	for _, f := range fields {
		if strings.Trim(f, "#*`~_-=") == "" {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// stripTags removes markup without parsing, for short fields where a full parse is
// not worth the cost.
//
// Only a '<' that has a matching '>' opens a tag. The text reaching this point has
// already been entity-decoded by the feed or HTML parser, so a bare '<' is content:
// titles like "Why 5 < 10" must survive rather than be truncated at the '<'.
func stripTags(s string) string {
	var sb strings.Builder
	for {
		open := strings.IndexByte(s, '<')
		if open < 0 {
			break
		}
		end := strings.IndexByte(s[open:], '>')
		if end < 0 {
			break
		}
		sb.WriteString(s[:open])
		s = s[open+end+1:]
	}
	sb.WriteString(s)
	return sb.String()
}

func normaliseSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// truncateWords caps text at n words, so the index stores a bounded amount per
// article regardless of how long the original is.
func truncateWords(s string, n int) string {
	if n <= 0 {
		return ""
	}
	fields := strings.Fields(s)
	if len(fields) <= n {
		return strings.Join(fields, " ")
	}
	return strings.Join(fields[:n], " ")
}
