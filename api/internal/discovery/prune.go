package discovery

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/url"

	"github.com/GitPaulo/blogme/api/internal/sources"
)

// Sources one pass may prune. Once the backlog is gone a pass meets one or two; this
// bounds a run of them, and what a pass spends when each homepage takes the full client
// timeout to fail.
const maxPrunesPerPass = 10

// pruneUnreachable removes the articles of sources whose sites have stopped answering,
// so search stops sending readers to pages that will not load.
//
// Quarantine stops the crawling but leaves what a source gathered: on 7 October 2026,
// 486 quarantined sources still held 15,616 documents. Only a source that has failed
// every attempt for about five weeks is looked at (see Health.Unreachable), and its
// articles go only if its homepage fails as well.
//
// It rides on the quarantine probe the pass already makes, so it costs one listing per
// source, once, and deletes, which are free. Nothing here is fatal: a source it could
// not finish is looked at again on its next probe.
func (d *Discoverer) pruneUnreachable(ctx context.Context, failed []sources.Source) {
	if d.prune != "on" && d.prune != "dry" {
		return
	}
	if d.store == nil || d.index == nil {
		return
	}

	var checked, empty, alive, pruned int
	for _, s := range failed {
		if pruned >= maxPrunesPerPass || ctx.Err() != nil {
			break
		}
		if !d.health.Unreachable(s.ID) {
			continue
		}
		checked++

		// The listing comes first. Most unreachable sources never worked and hold
		// nothing, which one listing settles without a request to the site.
		ids, complete, err := d.store.IDs(ctx, articlePrefix(s.ID), maxStoredPages)
		if err != nil {
			slog.WarnContext(ctx, "prune listing failed", "source", s.ID, "error", err)
			continue
		}
		if len(ids) == 0 {
			empty++
			if d.prune == "on" {
				d.health.MarkPruned(s.ID)
			}
			continue
		}

		reason, gone := d.siteGone(ctx, s)
		if !gone {
			alive++
			continue
		}

		pruned++
		if d.prune == "dry" {
			slog.InfoContext(ctx, "would prune unreachable source",
				"source", s.ID, "site", s.Site, "articles", len(ids), "reason", reason)
			continue
		}

		if err := d.removeArticles(ctx, ids); err != nil {
			slog.WarnContext(ctx, "prune failed", "source", s.ID, "error", err)
			continue
		}
		// A listing cut short leaves articles behind, so the source stays unpruned and
		// its next probe takes the rest.
		if complete {
			d.health.MarkPruned(s.ID)
		}
		slog.InfoContext(ctx, "pruned unreachable source",
			"source", s.ID, "site", s.Site, "articles", len(ids), "reason", reason)
	}

	if checked > 0 {
		slog.InfoContext(ctx, "unreachable sources checked", "mode", d.prune,
			"checked", checked, "empty", empty, "alive", alive, "pruned", pruned)
	}
}

// siteGone reports whether a source's site has stopped answering readers, not only this
// crawler, and why. A failing feed is not enough: a blog that moved its feed is still
// up and its posts still load, so the homepage has to fail too.
func (d *Discoverer) siteGone(ctx context.Context, s sources.Source) (string, bool) {
	site, err := url.Parse(s.Site)
	if err != nil || !isHTTP(site) || !d.robots.allowed(ctx, site) {
		return "", false
	}

	_, _, err = d.fetcher.fetch(ctx, s.Site, maxPageBytes)
	switch {
	case err == nil:
		return "", false
	// The pass running out is not the site's doing.
	case ctx.Err() != nil:
		return "", false
	// Turned away, which a reader is not.
	case refusesCrawler(err):
		return "", false
	// Go does not fetch a missing intermediate certificate and browsers do, so such a
	// site often loads for a reader. Expired and wrong-host certificates fail for both.
	case errors.As(err, new(x509.UnknownAuthorityError)):
		return "", false
	}
	return err.Error(), true
}

// removeArticles takes ids out of the index and then the store. It is project's order
// reversed, for the same reason: a removal cut short leaves blobs, which the next
// listing finds and removes again, where the other order would leave documents in
// search with nothing left to find them by.
func (d *Discoverer) removeArticles(ctx context.Context, ids []string) error {
	if err := d.index.Delete(ctx, ids); err != nil {
		return err
	}
	return d.store.Delete(ctx, ids)
}
