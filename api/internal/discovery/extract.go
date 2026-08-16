package discovery

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// extractText pulls readable prose out of an HTML document.
//
// This is deliberately not a full readability implementation: it drops the elements
// that reliably contain no prose, prefers <article>/<main> when present, and takes
// the document body otherwise. Good enough to index, cheap to run.
func extractText(doc string) string {
	node, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return normaliseSpace(stripTags(doc))
	}

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
func extractSummary(doc string, words int) string {
	node, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return ""
	}
	if root := findContentRoot(node); root != nil {
		node = root
	}

	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if skipNode(n) || wordCount(sb.String()) >= words {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.P {
			var para strings.Builder
			collectText(n, &para)
			if text := cleanProse(para.String()); text != "" {
				sb.WriteString(text)
				sb.WriteByte(' ')
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
func stripTags(s string) string {
	var sb strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			sb.WriteRune(r)
		}
	}
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
