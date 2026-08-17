package discovery

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

const (
	maxSitemapBytes = 8 << 20 // 8 MB

	// A sitemap index fans out into more documents; two levels covers the common
	// layouts without letting one source spend a whole run fetching sitemaps.
	maxSitemapDocs = 4

	// Enough to reach the recent end of a large archive without holding a huge list.
	maxSitemapEntries = 20_000

	// Sitemaps list every page, so a fetched page has to prove it is long-form prose
	// rather than a landing, tag or index page.
	minSitemapWords = 250
)

// A sitemap is XML, and plenty of sites answer any unknown path with their homepage,
// so the document has to look like a sitemap before it is parsed.
var sitemapRootRe = regexp.MustCompile(`(?i)<(urlset|sitemapindex)[\s>]`)

// Paths that exist to organise a site rather than to be read.
var nonArticleSegments = map[string]bool{
	"tag": true, "tags": true, "category": true, "categories": true,
	"topic": true, "topics": true, "author": true, "authors": true,
	"page": true, "pages": true, "search": true, "archive": true, "archives": true,
	"feed": true, "rss": true, "atom": true, "comments": true,
	"about": true, "contact": true, "privacy": true, "terms": true,
	"login": true, "signup": true, "register": true, "cart": true, "shop": true,
}

// Extensions that are never an article page.
var nonArticleExtensions = map[string]bool{
	".pdf": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".svg": true, ".ico": true, ".css": true, ".js": true,
	".json": true, ".xml": true, ".zip": true, ".gz": true, ".mp3": true,
	".mp4": true, ".webm": true, ".txt": true,
}

// sitemapDoc covers both a URL set and a sitemap index, which share a shape.
type sitemapDoc struct {
	URLs []sitemapEntry `xml:"url"`
	Maps []sitemapEntry `xml:"sitemap"`
}

type sitemapEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// sitemapLink is one candidate article URL taken from a sitemap.
type sitemapLink struct {
	url     *url.URL
	lastMod time.Time
}

// crawlSitemap covers the third of the corpus that publishes no feed. It is the
// slower path by design: a feed describes its posts, whereas here every candidate
// page must be fetched before it can be judged.
func (d *Discoverer) crawlSitemap(ctx context.Context, s sources.Source) ([]article.Article, error) {
	site, err := url.Parse(s.Site)
	if err != nil || !isHTTP(site) {
		return nil, fmt.Errorf("invalid site url %q", s.Site)
	}

	links, err := d.sitemapLinks(ctx, site)
	if err != nil {
		return nil, err
	}

	articles := make([]article.Article, 0, min(len(links), d.maxPosts))
	for _, link := range links {
		if len(articles) >= d.maxPosts {
			break
		}
		if !d.robots.allowed(ctx, link.url) {
			continue
		}
		// A sitemap lists a whole archive but one run takes only a few pages, so
		// skipping what is already stored is what lets later runs reach further in.
		if stored, err := d.store.Has(ctx, articleID(s.ID, link.url.String())); err != nil || stored {
			continue
		}
		if a, ok := d.sitemapArticle(ctx, s, link); ok {
			articles = append(articles, a)
		}
	}

	return articles, nil
}

// sitemapLinks returns the site's candidate article URLs, newest first.
func (d *Discoverer) sitemapLinks(ctx context.Context, site *url.URL) ([]sitemapLink, error) {
	doc, base, err := d.findSitemap(ctx, site)
	if err != nil {
		return nil, err
	}

	entries := doc.URLs

	// A sitemap index points at further documents; take the ones changed most
	// recently, since older ones hold older posts.
	if len(entries) == 0 && len(doc.Maps) > 0 {
		children := doc.Maps
		sort.SliceStable(children, func(i, j int) bool {
			return parseTime(children[i].LastMod).After(parseTime(children[j].LastMod))
		})

		for _, child := range children {
			if len(entries) >= maxSitemapEntries {
				break
			}
			childURL, err := base.Parse(strings.TrimSpace(child.Loc))
			if err != nil || !isHTTP(childURL) || !d.robots.allowed(ctx, childURL) {
				continue
			}
			nested, err := d.fetchSitemap(ctx, childURL.String())
			if err != nil {
				continue
			}
			entries = append(entries, nested.URLs...)
		}
	}

	links := make([]sitemapLink, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		link, err := base.Parse(strings.TrimSpace(entry.Loc))
		if err != nil || !isArticleURL(link, site) || seen[link.String()] {
			continue
		}
		seen[link.String()] = true
		links = append(links, sitemapLink{url: link, lastMod: parseTime(entry.LastMod)})
	}

	// Undated entries sort last: with nothing to say how recent they are, they are
	// the weakest candidates for a bounded run.
	sort.SliceStable(links, func(i, j int) bool {
		return links[i].lastMod.After(links[j].lastMod)
	})

	return links, nil
}

// findSitemap returns the first document that really is a sitemap, preferring the
// ones robots.txt advertises over guessed paths.
func (d *Discoverer) findSitemap(ctx context.Context, site *url.URL) (sitemapDoc, *url.URL, error) {
	var lastErr error

	for i, candidate := range sitemapCandidates(ctx, d.robots, site) {
		if i >= maxSitemapDocs {
			break
		}

		candidateURL, err := url.Parse(candidate)
		if err != nil || !isHTTP(candidateURL) || !d.robots.allowed(ctx, candidateURL) {
			continue
		}

		doc, err := d.fetchSitemap(ctx, candidateURL.String())
		if err != nil {
			lastErr = err
			continue
		}
		return doc, candidateURL, nil
	}

	if lastErr != nil {
		return sitemapDoc{}, nil, fmt.Errorf("no usable sitemap: %w", lastErr)
	}
	return sitemapDoc{}, nil, fmt.Errorf("no usable sitemap")
}

func (d *Discoverer) fetchSitemap(ctx context.Context, rawURL string) (sitemapDoc, error) {
	raw, err := d.fetcher.get(ctx, rawURL, maxSitemapBytes)
	if err != nil {
		return sitemapDoc{}, err
	}
	return parseSitemap(raw)
}

// sitemapCandidates lists sitemap URLs to try, robots.txt first.
func sitemapCandidates(ctx context.Context, r *robots, site *url.URL) []string {
	root := site.Scheme + "://" + site.Host

	candidates := r.sitemapsFor(ctx, site)
	seen := make(map[string]bool, len(candidates)+4)
	for _, c := range candidates {
		seen[c] = true
	}

	for _, guess := range []string{"/sitemap.xml", "/sitemap_index.xml", "/sitemap-index.xml", "/wp-sitemap.xml"} {
		if url := root + guess; !seen[url] {
			seen[url] = true
			candidates = append(candidates, url)
		}
	}
	return candidates
}

func parseSitemap(raw []byte) (sitemapDoc, error) {
	if !sitemapRootRe.Match(raw[:min(len(raw), 2000)]) {
		return sitemapDoc{}, fmt.Errorf("not a sitemap")
	}

	var doc sitemapDoc
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	// Sitemaps in the wild are as loosely encoded as feeds.
	decoder.Strict = false
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	if err := decoder.Decode(&doc); err != nil {
		return sitemapDoc{}, err
	}

	if len(doc.URLs) > maxSitemapEntries {
		doc.URLs = doc.URLs[:maxSitemapEntries]
	}
	return doc, nil
}

// isArticleURL keeps a sitemap entry that could plausibly be a post: on the site
// itself, not the homepage, and not an obvious listing page or asset.
func isArticleURL(u *url.URL, site *url.URL) bool {
	if u == nil || !isHTTP(u) || u.Hostname() != site.Hostname() {
		return false
	}

	trimmed := strings.Trim(u.Path, "/")
	if trimmed == "" {
		return false
	}

	if nonArticleExtensions[strings.ToLower(path.Ext(trimmed))] {
		return false
	}

	for _, segment := range strings.Split(strings.ToLower(trimmed), "/") {
		if nonArticleSegments[segment] {
			return false
		}
	}
	return true
}

// sitemapArticle fetches a candidate page and keeps it only if it reads like a post.
func (d *Discoverer) sitemapArticle(ctx context.Context, s sources.Source, link sitemapLink) (article.Article, bool) {
	body, err := d.fetcher.get(ctx, link.url.String(), maxPageBytes)
	if err != nil {
		return article.Article{}, false
	}

	markup := string(body)
	content := extractText(markup)
	if wordCount(content) < minSitemapWords {
		return article.Article{}, false
	}

	meta := extractMeta(markup)
	if meta.Title == "" {
		return article.Article{}, false
	}

	summary := extractSummary(markup, summaryWords)
	if wordCount(summary) < minSummaryWords && wordCount(content) > wordCount(summary) {
		summary = content
	}

	return article.Article{
		ID:       articleID(s.ID, link.url.String()),
		URL:      link.url.String(),
		Title:    meta.Title,
		Author:   s.Name,
		SourceID: s.ID,
		Origin:   article.OriginSitemap,
		Summary:  truncateWords(summary, summaryWords),
		Content:  truncateWords(content, d.contentWords),
		Topics:   s.Tags,
		// Deliberately not the sitemap's lastmod: that is when the file changed, which
		// for most publishing systems is a bulk regeneration and would date every page
		// today. An unknown date stays unknown.
		PublishedAt: meta.Published,
		FetchedAt:   time.Now().UTC(),
	}, true
}
