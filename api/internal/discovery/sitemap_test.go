package discovery

import (
	"net/url"
	"testing"
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
