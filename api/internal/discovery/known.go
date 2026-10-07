package discovery

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
)

// Pages of a source's listing one crawl reads before it stops treating the listing as
// whole. A page is 5,000 entries, and on 7 October 2026 the largest source held 825
// articles and the widest prefix, "blog", listed 2,576 entries, so one page covers every
// source with room to spare. Four cost what 51 single lookups do, more than the 42 an
// average source used to spend, so past that the listing no longer pays for itself and
// a post it does not carry is looked up on its own.
const maxStoredPages = 4

// knownArticles is what one crawl knows a source already has: a listing of its stored
// articles, taken at the first lookup, and the articles the crawl has gathered since.
//
// It replaces a blob HEAD per candidate link per pass, which on 7 October 2026 came to
// about a million GetBlobProperties a day, 42 per source per pass and 86% of them hits,
// and GBP 9.40 in September. A listing is billed as 12.7 HEADs (GBP 0.0445 against
// 0.0035 per 10,000 in UK South), so one per crawl comes to about GBP 3 a month.
// see: https://azure.microsoft.com/en-gb/pricing/details/storage/blobs/
//
// Not safe for concurrent use: one source is walked by one goroutine.
type knownArticles struct {
	store    articleStore
	sourceID string

	listed bool
	// The listing plus every article this crawl has accepted. The listing is a snapshot
	// and an accepted article is not saved until the pass flushes it, so without the
	// second half two spellings of one post in the same walk would both be taken.
	ids map[string]bool
	// Whether the listing carried every stored article, which is what lets a link
	// missing from ids be called new without asking the store.
	complete bool
	// The listing could not be read, so every lookup counts as stored.
	failed bool
}

func newKnownArticles(st articleStore, sourceID string) *knownArticles {
	return &knownArticles{store: st, sourceID: sourceID, ids: make(map[string]bool)}
}

// skipStored reports whether the article for link has already been captured, and so
// need not be built, fetched or written again.
//
// The store is the crawler's memory. Without this the feed path rebuilt and rewrote its
// newest entries every pass, refetching the page wherever the feed carried stubs, so a
// corpus that had not changed still cost a blob write and an index upsert per post per
// pass. A post counts as captured under any of its spellings; see spellings for why.
//
// A lookup that fails counts as stored. Taking a storage blip for "not stored" would
// turn every source in the pass into a storm of refetches, where a post missed this
// time is picked up on the next pass.
func (k *knownArticles) skipStored(ctx context.Context, link string) bool {
	k.list(ctx)
	if k.failed {
		return true
	}

	for _, spelling := range spellings(link) {
		if !k.ids[articleID(k.sourceID, spelling)] {
			continue
		}
		// Debug because a site that changed its URL form meets this on every pass until
		// the old posts leave its feed. It is the line to look for when a post that
		// should have been taken was not.
		if spelling != link {
			slog.DebugContext(ctx, "post already captured under another spelling",
				"source", k.sourceID, "url", link, "captured", spelling)
		}
		return true
	}
	if k.complete {
		return false
	}

	// The link as written only. Its other spellings would cost a lookup each, which is
	// the price the listing exists to avoid.
	stored, err := k.store.Has(ctx, articleID(k.sourceID, link))
	if err != nil {
		// Said out loud, because from the outside a broken store and a blog with
		// nothing new to say look alike.
		slog.WarnContext(ctx, "store lookup failed",
			"source", k.sourceID, "url", link, "error", err)
		return true
	}
	return stored
}

// add records an article this crawl has accepted, so the rest of the walk treats it as
// captured before the pass gets round to saving it.
func (k *knownArticles) add(id string) {
	k.ids[id] = true
}

// list reads the source's stored ids once. It waits for the first lookup rather than the
// start of the crawl, so a source that fails before reaching a post costs no listing.
func (k *knownArticles) list(ctx context.Context) {
	if k.listed {
		return
	}
	k.listed = true

	// A Discoverer built without a store keeps nothing, so it has nothing to list.
	if k.store == nil {
		k.complete = true
		return
	}

	ids, complete, err := k.store.IDs(ctx, articlePrefix(k.sourceID), maxStoredPages)
	if err != nil {
		slog.WarnContext(ctx, "store listing failed", "source", k.sourceID, "error", err)
		k.failed = true
		return
	}
	// Info, because a source this large has outgrown maxStoredPages and is paying for
	// a listing and for single lookups both.
	if !complete {
		slog.InfoContext(ctx, "store listing cut short",
			"source", k.sourceID, "listed", len(ids))
	}

	for _, id := range ids {
		k.ids[id] = true
	}
	k.complete = complete
}

// spellings returns link and the other ways a site may write the same post: with or
// without a trailing slash, over http or https, with or without a leading www.
//
// A site that changes its URL form, or a source whose failed feed falls back to its
// sitemap, re-spells posts it has already given us, and each re-spelling hashes to a new
// article id. On 7 October 2026 8,399 documents, 0.38% of the index, were re-spellings
// of a post their own source already had, accruing at about 200 a day.
//
// The fragment is left alone: Discourse links each reply in a topic to an anchor on one
// page, so there it is the only thing telling two posts apart.
func spellings(link string) []string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return []string{link}
	}

	out := make([]string, 0, 8)
	out = append(out, link)
	for toggles := 1; toggles < 8; toggles++ {
		v := *u
		if toggles&1 != 0 {
			v.Path = toggleSlash(v.Path)
			// Set only when the path was written with a non-default escaping, and
			// String falls back to Path when the two disagree.
			if v.RawPath != "" {
				v.RawPath = toggleSlash(v.RawPath)
			}
		}
		if toggles&2 != 0 {
			v.Scheme = toggleScheme(v.Scheme)
		}
		if toggles&4 != 0 {
			v.Host = toggleWWW(v.Host)
		}
		out = append(out, v.String())
	}
	return out
}

func toggleSlash(path string) string {
	if trimmed, ok := strings.CutSuffix(path, "/"); ok {
		return trimmed
	}
	return path + "/"
}

func toggleScheme(scheme string) string {
	if scheme == "https" {
		return "http"
	}
	return "https"
}

func toggleWWW(host string) string {
	if len(host) > 4 && strings.EqualFold(host[:4], "www.") {
		return host[4:]
	}
	return "www." + host
}
