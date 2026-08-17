package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
)

// crawl turns one approved blog into articles.
//
// A feed describes its own posts, so it is both cheaper and more accurate; the
// sitemap path exists for the third of the corpus that publishes no feed.
func (d *Discoverer) crawl(ctx context.Context, s sources.Source) ([]article.Article, error) {
	if s.Feed == "" {
		return d.crawlSitemap(ctx, s)
	}
	return d.crawlFeed(ctx, s)
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
		Title:       it.Title,
		Author:      firstNonEmpty(it.Author, s.Name),
		SourceID:    s.ID,
		Origin:      article.OriginFeed,
		Summary:     truncateWords(summary, summaryWords),
		Content:     truncateWords(content, d.contentWords),
		Topics:      s.Tags,
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
