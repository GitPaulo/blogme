package discovery

import (
	"net/url"
	"strings"
	"testing"
)

const rssSample = `<?xml version="1.0"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"
     xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel>
    <title>Example Blog</title>
    <item>
      <title>Scaling single-threaded servers</title>
      <link>https://example.com/posts/scaling</link>
      <dc:creator>Jane Dev</dc:creator>
      <pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
      <description>&lt;p&gt;A short &lt;b&gt;summary&lt;/b&gt;.&lt;/p&gt;</description>
      <content:encoded>&lt;p&gt;The full body text.&lt;/p&gt;</content:encoded>
    </item>
  </channel>
</rss>`

const atomSample = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Blog</title>
  <entry>
    <title>Satellites and meteors</title>
    <link rel="alternate" href="https://example.org/satellites"/>
    <author><name>Alyn</name></author>
    <published>2020-04-21T10:00:00Z</published>
    <summary>Telling them apart.</summary>
  </entry>
</feed>`

func TestParseFeedRSS(t *testing.T) {
	items, err := parseFeed([]byte(rssSample))
	if err != nil {
		t.Fatalf("parseFeed() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}

	it := items[0]
	if it.Title != "Scaling single-threaded servers" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.Link != "https://example.com/posts/scaling" {
		t.Errorf("Link = %q", it.Link)
	}
	if it.Author != "Jane Dev" {
		t.Errorf("Author = %q", it.Author)
	}
	if it.Summary != "A short summary." {
		t.Errorf("Summary = %q, want markup stripped and entities decoded", it.Summary)
	}
	if it.Published.IsZero() {
		t.Error("Published was not parsed")
	}
}

func TestParseFeedAtom(t *testing.T) {
	items, err := parseFeed([]byte(atomSample))
	if err != nil {
		t.Fatalf("parseFeed() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Link != "https://example.org/satellites" {
		t.Errorf("Link = %q, want the alternate link", items[0].Link)
	}
	if items[0].Author != "Alyn" {
		t.Errorf("Author = %q", items[0].Author)
	}
	if got := items[0].Published.Format("2006-01-02"); got != "2020-04-21" {
		t.Errorf("Published = %s", got)
	}
}

func TestParseTimeFormats(t *testing.T) {
	for _, in := range []string{
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	} {
		if parseTime(in).IsZero() {
			t.Errorf("parseTime(%q) failed", in)
		}
	}
	if !parseTime("not a date").IsZero() {
		t.Error("parseTime() should return zero for junk")
	}
}

func TestExtractTextPrefersArticleAndDropsChrome(t *testing.T) {
	doc := `<html><body>
		<nav>Home About Contact</nav>
		<script>var tracking = 1;</script>
		<article><p>The real content.</p><p>More of it.</p></article>
		<footer>Copyright notice</footer>
	</body></html>`

	got := extractText(doc)
	if !strings.Contains(got, "The real content.") {
		t.Errorf("extractText() lost the article body: %q", got)
	}
	for _, unwanted := range []string{"Home About Contact", "tracking", "Copyright"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("extractText() kept %q: %q", unwanted, got)
		}
	}
}

func TestTruncateWords(t *testing.T) {
	if got := truncateWords("one two three four", 2); got != "one two" {
		t.Errorf("truncateWords() = %q", got)
	}
	if got := truncateWords("one two", 10); got != "one two" {
		t.Errorf("truncateWords() = %q", got)
	}
	if got := truncateWords("anything", 0); got != "" {
		t.Errorf("truncateWords() = %q, want empty", got)
	}
}

func TestArticleIDIsStableAndSafe(t *testing.T) {
	a := articleID("my.source", "https://example.com/a")
	b := articleID("my.source", "https://example.com/a")
	c := articleID("my.source", "https://example.com/b")

	if a != b {
		t.Error("articleID() must be stable for the same URL")
	}
	if a == c {
		t.Error("articleID() must differ for different URLs")
	}
	// Azure AI Search keys allow only letters, digits, underscore, dash, equals.
	for _, r := range a {
		ok := r == '_' || r == '-' || r == '=' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			t.Fatalf("articleID() produced an invalid key character %q in %q", r, a)
		}
	}
}

func TestRobotsDisallow(t *testing.T) {
	r := &robots{hosts: map[string]robotRules{
		"https://example.com": {disallow: []string{"/private", "/tmp"}},
	}}

	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/posts/hello", true},
		{"/private/secret", false},
		{"/tmp", false},
		{"/publicity", true},
	} {
		u := mustURL(t, "https://example.com"+tc.path)
		if got := r.allowed(t.Context(), u); got != tc.want {
			t.Errorf("allowed(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParseRobotsSelectsApplicableGroups(t *testing.T) {
	body := `
User-agent: badbot
Disallow: /

User-agent: *
Disallow: /admin
Disallow: /drafts   # comment

User-agent: blogme
Disallow: /nope

Sitemap: https://example.com/sitemap.xml
`
	rules, err := parseRobots(body)
	if err != nil {
		t.Fatalf("parseRobots() error = %v", err)
	}

	want := map[string]bool{"/admin": true, "/drafts": true, "/nope": true}
	if len(rules.disallow) != len(want) {
		t.Fatalf("disallow = %v, want %v", rules.disallow, want)
	}
	for _, got := range rules.disallow {
		if !want[got] {
			t.Errorf("unexpected rule %q (must not apply the badbot group)", got)
		}
	}

	// The scheme's colon must survive, or the sitemap URL is unusable.
	if len(rules.sitemaps) != 1 || rules.sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Errorf("sitemaps = %v, want the full URL", rules.sitemaps)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) = %v", raw, err)
	}
	return u
}
