package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/sources"
)

const sitemapSample = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/posts/older</loc><lastmod>2023-01-05</lastmod></url>
  <url><loc>https://example.com/posts/newest</loc><lastmod>2024-11-02</lastmod></url>
  <url><loc>https://example.com/tags/go</loc><lastmod>2025-01-01</lastmod></url>
  <url><loc>https://example.com/undated</loc></url>
</urlset>`

const sitemapIndexSample = `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap-posts.xml</loc><lastmod>2024-11-02</lastmod></sitemap>
</sitemapindex>`

func TestParseSitemapURLSet(t *testing.T) {
	doc, err := parseSitemap([]byte(sitemapSample))
	if err != nil {
		t.Fatalf("parseSitemap() error = %v", err)
	}
	if len(doc.URLs) != 4 || len(doc.Maps) != 0 {
		t.Fatalf("got %d urls and %d sitemaps, want 4 and 0", len(doc.URLs), len(doc.Maps))
	}
}

func TestParseSitemapIndex(t *testing.T) {
	doc, err := parseSitemap([]byte(sitemapIndexSample))
	if err != nil {
		t.Fatalf("parseSitemap() error = %v", err)
	}
	if len(doc.Maps) != 1 || doc.Maps[0].Loc != "https://example.com/sitemap-posts.xml" {
		t.Fatalf("index entries = %+v", doc.Maps)
	}
}

// Many sites answer any unknown path with their homepage, so an HTML body must not
// be mistaken for an empty sitemap.
func TestParseSitemapRejectsHTML(t *testing.T) {
	if _, err := parseSitemap([]byte("<!doctype html><html><body>Not found</body></html>")); err == nil {
		t.Error("parseSitemap() accepted an HTML page")
	}
	if _, err := parseSitemap(nil); err == nil {
		t.Error("parseSitemap() accepted an empty body")
	}
}

func TestIsArticleURL(t *testing.T) {
	site := mustURL(t, "https://example.com/")

	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://example.com/posts/hello", true},
		{"https://example.com/2024/11/a-post", true},
		{"https://example.com/", false},
		{"https://example.com/tags/go", false},
		{"https://example.com/page/2", false},
		{"https://example.com/about", false},
		{"https://example.com/feed.xml", false},
		{"https://example.com/images/cover.png", false},
		{"https://elsewhere.com/posts/hello", false},
		{"ftp://example.com/posts/hello", false},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("url.Parse(%q) = %v", tc.raw, err)
		}
		if got := isArticleURL(u, site); got != tc.want {
			t.Errorf("isArticleURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestExtractMetaReadsTitleAndDate(t *testing.T) {
	doc := `<html><head>
		<title>A post — Example Blog</title>
		<meta property="og:title" content="A post">
		<meta property="article:published_time" content="2024-11-02T08:00:00Z">
	</head><body><h1>A post</h1></body></html>`

	meta := extractMeta(parseHTML(doc))
	if meta.Title != "A post" {
		t.Errorf("Title = %q, want the og:title without the site name", meta.Title)
	}
	if got := meta.Published.Format("2006-01-02"); got != "2024-11-02" {
		t.Errorf("Published = %s", got)
	}
}

func TestExtractMetaFallsBackToHeadingAndTimeElement(t *testing.T) {
	doc := `<html><head><title>Fallback — Example</title></head>
		<body><h1>Fallback</h1><time datetime="2020-04-21">21 April</time></body></html>`

	meta := extractMeta(parseHTML(doc))
	if meta.Title != "Fallback" {
		t.Errorf("Title = %q, want the h1", meta.Title)
	}
	if got := meta.Published.Format("2006-01-02"); got != "2020-04-21" {
		t.Errorf("Published = %s", got)
	}
}

func TestExtractMetaLeavesUnknownDateZero(t *testing.T) {
	meta := extractMeta(parseHTML(`<html><head><title>No date</title></head><body></body></html>`))
	if meta.Title != "No date" {
		t.Errorf("Title = %q", meta.Title)
	}
	if !meta.Published.IsZero() {
		t.Errorf("Published = %v, want zero", meta.Published)
	}
}

// stubSitemap serves a sitemap of n candidate posts. This is the path the load in the
// logs came from: a sitemap lists a site's whole archive, and every entry on it costs a
// page fetch to judge.
func stubSitemap(t *testing.T, n int, pageHits *atomic.Int64, page http.HandlerFunc) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for i := range n {
			fmt.Fprintf(&sb, `<url><loc>%s/posts/%d</loc><lastmod>2024-11-02</lastmod></url>`,
				srv.URL, i)
		}
		sb.WriteString(`</urlset>`)
		_, _ = io.WriteString(w, sb.String())
	})
	mux.HandleFunc("/posts/", func(w http.ResponseWriter, r *http.Request) {
		pageHits.Add(1)
		page(w, r)
	})

	return srv
}

func sitemapSource(srv *httptest.Server) sources.Source {
	return sources.Source{ID: "bounded-sitemap", Site: srv.URL + "/"}
}

// The worst case in the logs, reproduced: a sitemap of thousands of pages that all
// answer and none of which qualify as articles. One source spent 10,582 requests in a
// single pass this way and kept nothing, because the only exit counted articles.
func TestSitemapWalkStopsAtTheFetchBudget(t *testing.T) {
	var pageHits atomic.Int64
	srv := stubSitemap(t, 2000, &pageHits, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w,
			`<html><head><title>Index</title></head><body><p>A listing, not a post.</p></body></html>`)
	})

	const maxPosts = 5
	d := newLocalDiscoverer(maxPosts)
	articles, err := d.crawl(context.Background(), sitemapSource(srv))
	if err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("got %d articles, want 0: none of these pages are posts", len(articles))
	}
	if got, want := pageHits.Load(), int64(maxPosts*pageBudgetPerPost); got != want {
		t.Errorf("fetched %d pages, want %d", got, want)
	}
}

// On this path a failed fetch leaves nothing to keep, so without the breaker a site
// that is down is read end to end at request speed for no result at all.
func TestSitemapWalkGivesUpAfterConsecutiveFailures(t *testing.T) {
	var pageHits atomic.Int64
	srv := stubSitemap(t, 2000, &pageHits, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	d := newLocalDiscoverer(50)
	if _, err := d.crawl(context.Background(), sitemapSource(srv)); err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if got := pageHits.Load(); got != maxConsecutiveFailures {
		t.Errorf("fetched %d pages, want %d", got, maxConsecutiveFailures)
	}
}

// The reported case, on the sitemap path: 1,887 requests become one.
func TestSitemapWalkStopsAtTheFirstRefusal(t *testing.T) {
	var pageHits atomic.Int64
	srv := stubSitemap(t, 2000, &pageHits, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	d := newLocalDiscoverer(50)
	if _, err := d.crawl(context.Background(), sitemapSource(srv)); err != nil {
		t.Fatalf("crawl() error = %v", err)
	}
	if got := pageHits.Load(); got != 1 {
		t.Errorf("fetched %d pages, want 1: a 403 is the operator saying stop", got)
	}
}
