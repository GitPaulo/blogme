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
	got := extractSummary(noisyPost, summaryWords)

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
	got := extractText(noisyPost)

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

	got := extractSummary(doc, summaryWords)
	if !strings.HasPrefix(got, "The opening paragraph") {
		t.Errorf("summary = %q, want it to start with the first paragraph", got)
	}
}
