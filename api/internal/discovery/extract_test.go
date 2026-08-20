package discovery

import (
	"strings"
	"testing"
)

// Reproduces junk seen on live result cards: theme anchor text, code listings and
// raw Markdown markers leaking into the description.
const noisyPost = `<html><body><article>
	<h2>objects<a class="anchor" href="#objects" aria-hidden="true">Link to heading</a></h2>
	<p>## objects are instances of classes</p>
	<pre><code>puts 'hello'.class
puts 'hello'.is_a?(String)
## output
# String
# true</code></pre>
	<h2>class instances<a class="headerlink" href="#ci">¶</a></h2>
	<pre><code># frozen_string_literal: true</code></pre>
	<p>A class is defined using the keyword class, a name, and a body.</p>
</article></body></html>`

func TestExtractSummarySkipsCodeAnchorsAndMarkdown(t *testing.T) {
	got := extractSummary(parseHTML(noisyPost), summaryWords)

	for _, unwanted := range []string{
		"Link to heading",       // anchor decoration
		"puts 'hello'",          // code listing
		"frozen_string_literal", // code listing
		"¶",                     // permalink glyph
		"##",                    // raw Markdown marker
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("summary contains %q:\n  %s", unwanted, got)
		}
	}

	if !strings.Contains(got, "A class is defined using the keyword") {
		t.Errorf("summary lost real prose:\n  %s", got)
	}
}

func TestExtractTextSkipsCodeAndAnchors(t *testing.T) {
	got := extractText(parseHTML(noisyPost))

	for _, unwanted := range []string{"Link to heading", "puts 'hello'", "frozen_string_literal"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("content contains %q:\n  %s", unwanted, got)
		}
	}
	if !strings.Contains(got, "A class is defined") {
		t.Errorf("content lost real prose:\n  %s", got)
	}
}

func TestCleanProseDropsMarkerOnlyTokens(t *testing.T) {
	got := cleanProse("## Heading -- text ``` more * text")
	for _, unwanted := range []string{"##", "```", "--"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("cleanProse() kept %q: %q", unwanted, got)
		}
	}
	if !strings.Contains(got, "Heading") || !strings.Contains(got, "more") {
		t.Errorf("cleanProse() dropped real words: %q", got)
	}
}

func TestExtractSummaryPrefersParagraphsOverHeadings(t *testing.T) {
	doc := `<article>
		<h1>A Heading That Is Not Prose</h1>
		<p>The opening paragraph of the article.</p>
	</article>`

	got := extractSummary(parseHTML(doc), summaryWords)
	if !strings.HasPrefix(got, "The opening paragraph") {
		t.Errorf("summary = %q, want it to start with the first paragraph", got)
	}
}

// A '<' reaching stripTags has already been entity-decoded, so it is content. Titles
// like "Why 5 < 10" used to lose everything from the '<' onward.
func TestStripTagsKeepsUnmatchedAngleBrackets(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"<p>A short <b>summary</b>.</p>", "A short summary."},
		{"Why 5 < 10 matters", "Why 5 < 10 matters"},
		{"Go < Rust?", "Go < Rust?"},
		{"a > b", "a > b"},
		{`<a title="x<y">text</a>`, "text"},
		{"no markup at all", "no markup at all"},
	} {
		if got := stripTags(tc.in); got != tc.want {
			t.Errorf("stripTags(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The same truncation reached article titles through the feed and HTML parsers, both
// of which hand cleanText text that is already decoded.
func TestCleanTextKeepsLessThanInTitles(t *testing.T) {
	if got := cleanText("Why 5 < 10"); got != "Why 5 < 10" {
		t.Errorf("cleanText() = %q, want the title intact", got)
	}
	if got := cleanText("&lt;p&gt;A post&lt;/p&gt;"); got != "<p>A post</p>" {
		t.Errorf("cleanText() = %q, want entities decoded after stripping", got)
	}
}

// A blog with no recorded feed and no sitemap is read only if its homepage can be
// asked where its feed is, so what counts as an advertisement decides whether the
// blog is in the corpus at all.
func TestDeclaredFeedsReadsWhatAPageAdvertises(t *testing.T) {
	const page = `<html><head>
		<link rel="alternate" type="application/rss+xml" href="/rss.xml">
		<link rel="ALTERNATE" type="application/atom+xml" href="https://cdn.example.com/atom.xml">
		<link type="application/rss+xml" rel="alternate home" href="feed.xml">
		<link rel="alternate" type="text/html" href="/index.html">
		<link rel="stylesheet" type="text/css" href="/style.css">
		<link rel="alternate" type="application/rss+xml">
		<link rel="icon" href="/favicon.ico">
	</head><body></body></html>`

	got := declaredFeeds(parseHTML(page))
	want := []string{
		"/rss.xml",
		"https://cdn.example.com/atom.xml",
		"feed.xml",
	}

	if len(got) != len(want) {
		t.Fatalf("got %d feeds %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("feed %d = %q, want %q", i, got[i], want[i])
		}
	}
}
