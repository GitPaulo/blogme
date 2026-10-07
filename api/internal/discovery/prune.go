package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
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
// articles go only if its homepage is definitely gone as well.
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
	// The pass running out is not the site's doing.
	if err == nil || ctx.Err() != nil || !definitelyGone(err) {
		return "", false
	}
	return err.Error(), true
}

// definitelyGone reports whether a failed fetch means the site is gone for everyone.
//
// Only answers that cannot come from bot protection count. Run over the 77 sources the
// first draft would have pruned on 8 October 2026, a browser still loaded two: one
// had timed out on the crawler and one had answered it 521, CSDN's anti-bot page. So
// timeouts, 5xx and dropped connections prove nothing, and neither do a refusal or a
// certificate Go cannot verify but a browser can — one missing an intermediate.
func definitelyGone(err error) bool {
	var status *statusError
	if errors.As(err, &status) {
		return status.code == http.StatusNotFound || status.code == http.StatusGone
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsNotFound
	}
	// Refused or unroutable: nothing is listening at the address any more.
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return !op.Timeout()
	}
	// Expired or issued for another host, which a browser refuses too.
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		return !errors.As(err, new(x509.UnknownAuthorityError))
	}
	return false
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
