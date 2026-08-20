package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/sources"
)

const (
	userAgentToken = "blogme"
	userAgent      = "blogme/0.1 (+https://github.com/GitPaulo/blogme)"

	maxRobotsBytes = 64 << 10 // 64 KB
	maxFeedBytes   = 4 << 20  // 4 MB
	maxPageBytes   = 2 << 20  // 2 MB

	// A feed entry carrying at least this many words is treated as the full post,
	// so no page fetch is needed. Most truncated feeds fall well below it.
	feedContentWords = 200

	// Length of the description shown on a result card.
	summaryWords = 40

	// Below this, a paragraph-derived summary is too thin to be worth preferring.
	minSummaryWords = 12

	// Caps on the fields taken straight from a feed. The body has always been
	// truncated and these were not, so a feed could put its whole payload in a
	// title and have all of it indexed.
	maxTitleWords  = 40
	maxAuthorWords = 15

	// How many advertised feeds to try when falling back to feed discovery. A page
	// can list one per format and one per category; the site-wide feeds come first.
	maxDeclaredFeeds = 3
)

// crawl turns one approved blog into articles.
//
// A feed describes its own posts, so it is both cheaper and more accurate; the
// sitemap path exists for the third of the corpus that publishes no feed. A blog
// with neither is served by the last resort below, which asks its homepage where
// its feed is.
func (d *Discoverer) crawl(ctx context.Context, s sources.Source) ([]article.Article, error) {
	if s.Feed != "" {
		return d.crawlFeed(ctx, s)
	}

	articles, err := d.crawlSitemap(ctx, s)
	if err == nil {
		return articles, nil
	}

	// No feed recorded and no sitemap to walk means the blog is never read at all.
	// That is a hole rather than a slow path: the source stays in the list, costs a
	// request every pass, and contributes nothing, which from the outside is
	// indistinguishable from a blog that has not posted. Most sites in that position
	// do publish a feed and simply never had it recorded, so the homepage is worth
	// one look before giving up on the source.
	for _, feed := range d.siteFeeds(ctx, s) {
		s.Feed = feed
		found, feedErr := d.crawlFeed(ctx, s)
		if feedErr != nil {
			continue
		}
		// Said out loud because the source list is what should be carrying this feed:
		// a run of these is the extractor needing another pass, not a healthy state.
		slog.InfoContext(ctx, "recovered feed from site html",
			"source", s.ID, "feed", feed, "articles", len(found))
		return found, nil
	}

	return nil, err
}

// siteFeeds returns the feeds a site advertises in its homepage HTML, resolved to
// absolute URLs. Only reached when a source has neither a recorded feed nor a
// usable sitemap, so it costs a request only where the alternative is nothing.
func (d *Discoverer) siteFeeds(ctx context.Context, s sources.Source) []string {
	site, err := url.Parse(s.Site)
	if err != nil || !isHTTP(site) {
		return nil
	}
	if !d.robots.allowed(ctx, site) {
		return nil
	}

	raw, err := d.fetcher.get(ctx, s.Site, maxPageBytes)
	if err != nil {
		slog.DebugContext(ctx, "feed discovery could not read the site",
			"source", s.ID, "error", err)
		return nil
	}

	var feeds []string
	for _, href := range declaredFeeds(parseHTML(string(raw))) {
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		// Resolved against the site, so a relative href like /rss.xml is usable and an
		// absolute one on another host is kept as written — plenty of blogs syndicate
		// through a third party.
		resolved := site.ResolveReference(ref)
		if !isHTTP(resolved) {
			continue
		}
		feeds = append(feeds, resolved.String())
		if len(feeds) == maxDeclaredFeeds {
			break
		}
	}

	return feeds
}

func (d *Discoverer) crawlFeed(ctx context.Context, s sources.Source) ([]article.Article, error) {
	feedURL, err := url.Parse(s.Feed)
	if err != nil || !isHTTP(feedURL) {
		return nil, fmt.Errorf("invalid feed url %q", s.Feed)
	}

	if !d.robots.allowed(ctx, feedURL) {
		return nil, nil
	}

	raw, err := d.fetcher.get(ctx, s.Feed, maxFeedBytes)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}

	items, err := parseFeed(raw)
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	articles := make([]article.Article, 0, len(items))
	for _, it := range items {
		if len(articles) >= d.maxPosts {
			break
		}
		a, ok := d.toArticle(ctx, s, it, feedURL)
		if !ok {
			continue
		}
		articles = append(articles, a)
	}

	return articles, nil
}

func (d *Discoverer) toArticle(ctx context.Context, s sources.Source, it feedItem, feedURL *url.URL) (article.Article, bool) {
	if it.Title == "" || it.Link == "" {
		return article.Article{}, false
	}

	link, err := feedURL.Parse(it.Link)
	if err != nil || !isHTTP(link) {
		return article.Article{}, false
	}

	// Keep the parsed tree around: summaries read far better when taken from paragraphs
	// than from the flattened text.
	doc := parseHTML(it.Content)
	content := extractText(doc)

	// Fall back to fetching the page only when the feed gave a stub, which keeps the
	// common case to one request per blog rather than one per post.
	if wordCount(content) < feedContentWords && d.robots.allowed(ctx, link) {
		if body, err := d.fetcher.get(ctx, link.String(), maxPageBytes); err == nil {
			page := parseHTML(string(body))
			// A page can be fetchable and still ask not to be indexed, and the ask is
			// only readable now that the page is in hand. Dropping the article rather
			// than falling back to the feed stub: the request was about the post, not
			// about which copy of it we keep.
			if noIndex(page) {
				return article.Article{}, false
			}
			if full := extractText(page); wordCount(full) > wordCount(content) {
				content, doc = full, page
			}
		}
	}

	// The article's own opening paragraphs make a better card than a feed's
	// description, which is often a machine-generated blurb built from raw Markdown.
	// Both extracted forms have been sanitised; the feed description has not, so it
	// is the last resort.
	summary := extractSummary(doc, summaryWords)
	if wordCount(summary) < minSummaryWords && wordCount(content) > wordCount(summary) {
		summary = content
	}
	if wordCount(summary) < minSummaryWords {
		if fallback := cleanProse(it.Summary); wordCount(fallback) > wordCount(summary) {
			summary = fallback
		}
	}

	return article.Article{
		ID:          articleID(s.ID, link.String()),
		URL:         link.String(),
		Title:       truncateWords(it.Title, maxTitleWords),
		Author:      truncateWords(firstNonEmpty(it.Author, s.Name), maxAuthorWords),
		SourceID:    s.ID,
		Origin:      article.OriginFeed,
		Summary:     truncateWords(summary, summaryWords),
		Content:     truncateWords(content, d.contentWords),
		Topics:      articleTopics(s.Tags, it.Categories),
		Kind:        s.Kind,
		PublishedAt: it.Published,
		FetchedAt:   time.Now().UTC(),
	}, true
}

// articleID is stable for a URL so re-crawling updates rather than duplicates.
// Azure AI Search keys allow only letters, digits, underscore, dash and equals.
func articleID(sourceID, link string) string {
	sum := sha256.Sum256([]byte(link))
	return sanitizeKey(sourceID) + "-" + hex.EncodeToString(sum[:8])
}

func sanitizeKey(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}

func isHTTP(u *url.URL) bool {
	return u.Scheme == "http" || u.Scheme == "https"
}
