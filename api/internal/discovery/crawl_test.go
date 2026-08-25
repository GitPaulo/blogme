package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/sources"
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
      <category>Distributed Systems</category>
      <category>Uncategorized</category>
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
    <category term="astronomy" label="Astronomy"/>
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
	if len(it.Categories) != 2 || it.Categories[0] != "Distributed Systems" {
		t.Errorf("Categories = %q", it.Categories)
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
	if len(items[0].Categories) != 1 || items[0].Categories[0] != "Astronomy" {
		t.Errorf("Categories = %q, want the label preferred over the term", items[0].Categories)
	}
}

func TestTopicSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Distributed Systems", "distributed-systems"},
		{"software-engineering", "software-engineering"},
		{"Uncategorized", ""},
		// Punctuation carries the meaning in "C++" and "C#", and kebab-casing loses
		// it, so both fall under the single-character floor rather than becoming "c".
		{"  C++  ", ""},
		{"a", ""},
		{"", ""},
		{"!!!", ""},
		{strings.Repeat("long", 20), ""},
		// Letters this cannot keep leave a word that is not the one the author
		// wrote. "Grupo de Usuários Python" reached the live corpus as the topic
		// "grupo-de-usu-rios", and nothing removes a topic once it is in.
		{"Grupo de Usuários", ""},
		{"Café", ""},
		// A phrase a post was filed under is not a subject anyone will filter by.
		{"How I Built My Own Thing", ""},
		// Three words is still a subject.
		{"Site Reliability Engineering", "site-reliability-engineering"},
	}
	for _, tc := range cases {
		if got := topicSlug(tc.in); got != tc.want {
			t.Errorf("topicSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestArticleTopicsAddsCategoriesToSourceTags(t *testing.T) {
	got := articleTopics([]string{"tech"}, []string{"Rust", "tech", "Uncategorized", "Compilers", "WASM", "Extra"})
	want := []string{"tech", "rust", "compilers", "wasm"}

	if len(got) != len(want) {
		t.Fatalf("articleTopics() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("articleTopics() = %q, want %q", got, want)
		}
	}
}

func TestArticleTopicsFallsBackToTheSource(t *testing.T) {
	got := articleTopics([]string{"tech", "linux"}, nil)
	if len(got) != 2 || got[0] != "tech" || got[1] != "linux" {
		t.Errorf("articleTopics() = %q, want the source's tags", got)
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

	got := extractText(parseHTML(doc))
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
		"https://example.com": {rules: []robotRule{
			{pattern: "/private"}, {pattern: "/tmp"},
		}},
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

// A pattern is not a literal prefix. Every case here was fetched before, because
// no real path begins with the characters "/*".
func TestMatchPathHandlesWildcardsAndAnchors(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/private", "/private/secret", true},
		{"/private", "/publicity", false},
		{"/", "/anything", true},
		{"/*/private", "/blog/private", true},
		{"/*/private", "/private", false},
		{"/*.php$", "/index.php", true},
		{"/*.php$", "/index.phps", false},
		{"/*.php$", "/a/b/c.php", true},
		{"/posts$", "/posts", true},
		{"/posts$", "/posts/one", false},
		{"/a*b$", "/axxbyyb", true},
		{"/a*", "/abc", true},
		{"/a*$", "/abc", true},
		{"/*?", "/search?q=go", true},
		{"/*?", "/search", false},
	} {
		if got := matchPath(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// The longest matching pattern wins, so a site can close a tree and reopen one
// branch of it. Previously every Allow line was discarded and the branch was lost.
func TestRobotsAllowReopensABranch(t *testing.T) {
	rules, err := parseRobots("User-agent: *\nDisallow: /\nAllow: /blog/\n")
	if err != nil {
		t.Fatalf("parseRobots() error = %v", err)
	}
	r := &robots{hosts: map[string]robotRules{"https://example.com": rules}}

	for path, want := range map[string]bool{
		"/blog/a-post": true,
		"/admin":       false,
		"/":            false,
	} {
		if got := r.allowed(t.Context(), mustURL(t, "https://example.com"+path)); got != want {
			t.Errorf("allowed(%s) = %v, want %v", path, got, want)
		}
	}
}

// A file that addresses this crawler by name is talking to it specifically, so
// that group replaces the wildcard rather than adding to it.
func TestParseRobotsPrefersTheGroupAddressedToUs(t *testing.T) {
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

	if len(rules.rules) != 1 || rules.rules[0].pattern != "/nope" {
		t.Fatalf("rules = %+v, want only the blogme group's /nope", rules.rules)
	}

	// The scheme's colon must survive, or the sitemap URL is unusable.
	if len(rules.sitemaps) != 1 || rules.sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Errorf("sitemaps = %v, want the full URL", rules.sitemaps)
	}
}

// Stacked User-agent lines address one group. Reading them one at a time let the
// last name win, so a group that named us alongside another crawler stopped
// applying to us.
func TestParseRobotsMergesStackedUserAgents(t *testing.T) {
	rules, err := parseRobots("User-agent: *\nUser-agent: somebot\nDisallow: /admin\n")
	if err != nil {
		t.Fatalf("parseRobots() error = %v", err)
	}
	if len(rules.rules) != 1 || rules.rules[0].pattern != "/admin" {
		t.Fatalf("rules = %+v, want the group's rule to apply through its wildcard line", rules.rules)
	}
}

// An empty group addressed to us is a decision, not the absence of one: it says
// we may go anywhere, and must not fall through to the wildcard group.
func TestParseRobotsEmptyGroupForUsOverridesWildcard(t *testing.T) {
	rules, err := parseRobots("User-agent: *\nDisallow: /\n\nUser-agent: blogme\nDisallow:\n")
	if err != nil {
		t.Fatalf("parseRobots() error = %v", err)
	}
	if len(rules.rules) != 0 {
		t.Fatalf("rules = %+v, want none", rules.rules)
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

// newLocalDiscoverer builds a Discoverer that will talk to a test server.
//
// It does not use New, because the crawl client refuses to connect to anything off
// the public internet — the guard that stops a source list from being turned into a
// port scanner, and which a loopback test server falls foul of by design.
func newLocalDiscoverer(maxPosts int) *Discoverer {
	client := &http.Client{}
	f := newFetcher(client)

	return &Discoverer{
		client:       client,
		fetcher:      f,
		robots:       newRobots(f),
		maxPosts:     maxPosts,
		contentWords: 1000,
	}
}

// A source with no recorded feed and no sitemap is never read at all: it stays in
// the list, costs a request every pass and contributes nothing, which looks exactly
// like a blog that has not posted. Most blogs in that position do publish a feed
// that was simply never recorded, so the crawler asks the homepage before giving up.
func TestCrawlFallsBackToAFeedTheSiteAdvertises(t *testing.T) {
	var homepageHits atomic.Int64

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		homepageHits.Add(1)
		_, _ = io.WriteString(w, `<html><head>
			<link rel="alternate" type="application/rss+xml" href="/rss.xml">
		</head><body></body></html>`)
	})
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
		<rss version="2.0"><channel><title>Recovered</title><item>
			<title>A post that would never have been indexed</title>
			<link>`+srv.URL+`/posts/one</link>
		</item></channel></rss>`)
	})
	mux.HandleFunc("/posts/one", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><body><article><p>The body of the post.</p></article></body></html>`)
	})

	d := newLocalDiscoverer(5)
	// No Feed, which is what sends this down the sitemap path. Nothing here serves a
	// sitemap or a robots.txt naming one, so that path has nowhere to go.
	articles, err := d.crawl(context.Background(), sources.Source{ID: "recovered", Site: srv.URL + "/"})
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1: the advertised feed was not used", len(articles))
	}
	if got := articles[0].URL; got != srv.URL+"/posts/one" {
		t.Errorf("URL = %q, want the post the feed listed", got)
	}
	if got := homepageHits.Load(); got != 1 {
		t.Errorf("homepage fetched %d times, want 1", got)
	}
}

// The fallback must stay a last resort. A site with nothing to offer should cost one
// look and then be reported as the sitemap failure it is, rather than being retried
// into a source that quietly costs more every pass than the ones that work.
func TestCrawlWithoutFeedOrSitemapReportsTheSitemapFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := newLocalDiscoverer(5)
	articles, err := d.crawl(context.Background(), sources.Source{ID: "empty", Site: srv.URL + "/"})

	if err == nil {
		t.Fatal("crawl() error = nil, want the sitemap failure to survive the fallback")
	}
	if len(articles) != 0 {
		t.Errorf("got %d articles, want 0", len(articles))
	}
	if !strings.Contains(err.Error(), "sitemap") {
		t.Errorf("error = %v, want it to still name the sitemap as the cause", err)
	}
}

// A recorded feed is the whole point of the source list, so it must not be spent on
// a homepage fetch first: the fallback exists for sources that have nothing else.
func TestCrawlWithARecordedFeedNeverLooksAtTheHomepage(t *testing.T) {
	var homepageHits atomic.Int64

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			homepageHits.Add(1)
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
		<rss version="2.0"><channel><title>Listed</title><item>
			<title>A post</title><link>`+srv.URL+`/posts/one</link>
		</item></channel></rss>`)
	})
	mux.HandleFunc("/posts/one", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><body><article><p>Body.</p></article></body></html>`)
	})

	d := newLocalDiscoverer(5)
	articles, err := d.crawl(context.Background(),
		sources.Source{ID: "listed", Site: srv.URL + "/", Feed: srv.URL + "/rss.xml"})
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}
	if got := homepageHits.Load(); got != 0 {
		t.Errorf("homepage fetched %d times, want 0", got)
	}
}

// A feed goes stale without the blog going quiet: the URL 404s, or the XML stops
// parsing, while the site carries on publishing and still has a sitemap. Ending the
// source on that failure left it in the list costing a request a pass and returning
// nothing — the same hole the site-HTML fallback was added to close, but hidden
// behind a source list that claims the blog has a feed.
//
// The sitemap here is valid and empty, which is what keeps the test off the article
// store: reaching the walk at all is the behaviour under test, and an empty one is
// reached and returns without a single lookup.
func TestCrawlWithABrokenFeedStillReachesTheSitemap(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0"?><urlset></urlset>`)
	})

	d := newLocalDiscoverer(5)
	articles, err := d.crawl(context.Background(),
		sources.Source{ID: "stale", Site: srv.URL + "/", Feed: srv.URL + "/rss.xml"})

	// Before the fallback the broken feed ended the source here and this was the
	// feed's own error, whatever the site still published.
	if err != nil {
		t.Fatalf("crawl() error = %v, want the sitemap to have been reached", err)
	}
	if len(articles) != 0 {
		t.Errorf("got %d articles, want 0 from an empty sitemap", len(articles))
	}
}

// When every route fails, the recorded feed is the one worth naming: it points at a
// correction the source list can carry, where the sitemap failure only says the
// fallback was not there either.
func TestCrawlWithABrokenFeedAndNoSitemapReportsTheFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := newLocalDiscoverer(5)
	_, err := d.crawl(context.Background(),
		sources.Source{ID: "gone", Site: srv.URL + "/", Feed: srv.URL + "/rss.xml"})

	if err == nil {
		t.Fatal("crawl() error = nil, want the feed failure to survive the fallback")
	}
	if !strings.Contains(err.Error(), "feed") {
		t.Errorf("error = %v, want it to name the feed as the cause", err)
	}
}

// memStore is an articleStore held in memory, so the crawler's dedup can be tested
// without an Azure account behind it. One crawl walks its feed in a single
// goroutine, so nothing here needs locking.
type memStore struct {
	have map[string]bool
	err  error
}

func (m *memStore) Save(_ context.Context, a article.Article) error {
	m.have[a.ID] = true
	return nil
}

func (m *memStore) Has(_ context.Context, id string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	return m.have[id], nil
}

// dedupFeed serves two posts as stubs rather than in full, so every post the crawler
// decides to keep costs a page fetch. That is what makes the saving countable: the
// fetches that do not happen are the ones the store already answered for.
func dedupFeed(t *testing.T, pageHits *atomic.Int64) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Dedup Blog</title>
  <item><title>First post</title><link>`+srv.URL+`/posts/one</link>
    <description>A stub.</description></item>
  <item><title>Second post</title><link>`+srv.URL+`/posts/two</link>
    <description>A stub.</description></item>
</channel></rss>`)
	})
	mux.HandleFunc("/posts/", func(w http.ResponseWriter, _ *http.Request) {
		pageHits.Add(1)
		_, _ = io.WriteString(w,
			`<html><body><article><p>The body of the post.</p></article></body></html>`)
	})

	return srv
}

func dedupSource(srv *httptest.Server) sources.Source {
	return sources.Source{ID: "dedup", Site: srv.URL + "/", Feed: srv.URL + "/feed.xml"}
}

// The feed path used to rebuild and rewrite its newest entries every pass, refetching
// each page to do it, which made an unchanged corpus the largest recurring cost in the
// system. What the store already holds now costs nothing at all.
func TestCrawlFeedSkipsPostsTheStoreAlreadyHolds(t *testing.T) {
	var pageHits atomic.Int64
	srv := dedupFeed(t, &pageHits)

	d := newLocalDiscoverer(5)
	d.store = &memStore{have: map[string]bool{
		articleID("dedup", srv.URL+"/posts/one"): true,
		articleID("dedup", srv.URL+"/posts/two"): true,
	}}

	articles, err := d.crawl(context.Background(), dedupSource(srv))
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("got %d articles, want 0: both posts were already stored", len(articles))
	}
	if got := pageHits.Load(); got != 0 {
		t.Errorf("fetched %d pages, want 0: a stored post should cost no request", got)
	}
}

// The other half of the same behaviour: skipping must not cost the crawler a post it
// has never seen, and the entry it skips must not stand between it and the one it has.
func TestCrawlFeedKeepsPostsTheStoreDoesNotHold(t *testing.T) {
	var pageHits atomic.Int64
	srv := dedupFeed(t, &pageHits)

	d := newLocalDiscoverer(5)
	d.store = &memStore{have: map[string]bool{
		articleID("dedup", srv.URL+"/posts/one"): true,
	}}

	articles, err := d.crawl(context.Background(), dedupSource(srv))
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1: the second post was not stored", len(articles))
	}
	if got := articles[0].URL; got != srv.URL+"/posts/two" {
		t.Errorf("URL = %q, want the post the store did not hold", got)
	}
	if got := pageHits.Load(); got != 1 {
		t.Errorf("fetched %d pages, want 1: only the unstored post needs one", got)
	}
}

// A store that cannot answer is treated as holding the post. Reading a storage blip as
// "not stored" would turn every source in the pass into a storm of refetches, where a
// post missed this time is picked up on the next pass.
func TestCrawlFeedSkipsWhenTheStoreCannotAnswer(t *testing.T) {
	var pageHits atomic.Int64
	srv := dedupFeed(t, &pageHits)

	d := newLocalDiscoverer(5)
	d.store = &memStore{have: map[string]bool{}, err: errors.New("storage unavailable")}

	articles, err := d.crawl(context.Background(), dedupSource(srv))
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("got %d articles, want 0 while the store is unreadable", len(articles))
	}
	if got := pageHits.Load(); got != 0 {
		t.Errorf("fetched %d pages, want 0: an unreadable store must not cause refetches", got)
	}
}

// Skipped entries no longer count towards maxPosts, so the walk reaches further down a
// feed than it used to. What must not move with it is how much one pass writes.
func TestCrawlFeedStillCapsPostsPerPass(t *testing.T) {
	var pageHits atomic.Int64
	srv := dedupFeed(t, &pageHits)

	d := newLocalDiscoverer(1)
	d.store = &memStore{have: map[string]bool{}}

	articles, err := d.crawl(context.Background(), dedupSource(srv))
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if len(articles) != 1 {
		t.Errorf("got %d articles, want 1: maxPosts still caps a pass", len(articles))
	}
}

// A Discoverer built without a store keeps nothing, so it has nothing to skip. The
// crawl tests that are not about storage rely on this.
func TestSkipStoredWithoutAStoreSkipsNothing(t *testing.T) {
	d := newLocalDiscoverer(5)
	if d.skipStored(context.Background(), "none", "https://example.com/post") {
		t.Error("skipStored() = true with no store, want false")
	}
}
