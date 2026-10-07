"""Folding together sources the crawler would read identically.

An article's key is its source id and its URL together, so two entries that crawl the
same posts index every one of them twice: two documents, two blobs, and two rows on one
results page, each with its own three-per-source allowance. Measured on 7 October 2026,
about 100,000 documents (0.67 GB) were copies of this kind, and repeats filled 7% of the
top-ten places across 97 replayed queries.

Entries are folded only when the crawler would read the same thing for both — never just
because they share a host. `adactio.com/` and `adactio.com/journal/` with feeds of their
own are two streams of writing, and both are wanted (see drop_platform_roots).
"""

from __future__ import annotations

from typing import Any
from urllib.parse import urlparse

from .overrides import DROP_FIELD, ordered_entry
from .tags import ordered_tags
from .urls import MULTI_TENANT_HOSTS, site_key


def pinned_sites(overrides: list[dict[str, Any]]) -> set[str]:
    """The site_keys an override corrects, which merge_duplicate_crawls leaves alone."""
    return {site_key(o["site"]) for o in overrides if not o.get(DROP_FIELD)}


def crawl_keys(entry: dict[str, Any]) -> list[str]:
    """What the crawler will read for an entry; two entries sharing any key read alike.

    - The same site spelled twice: http and https, a `www.`, a trailing slash.
    - The same feed, which is read regardless of which site lists it.
    - A host's sitemap, for an entry without a feed. The sitemap walk keeps every page on
      the host and ignores the site's path (see isArticleURL in api/internal/discovery),
      so two feedless entries on one host walk one sitemap.
    """
    keys = [f"site:{site_key(entry['site'])}"]

    feed = entry.get("feed")
    if feed:
        # site_key drops the query, which is what tells Blogger's
        # feeds/posts/default?alt=rss from another feed on the same path.
        query = urlparse(feed).query
        keys.append(f"feed:{site_key(feed)}" + (f"?{query}" if query else ""))
        return keys

    parsed = urlparse(entry["site"])
    host = (parsed.hostname or "").lower().removeprefix("www.")
    segments = [s for s in (parsed.path or "").split("/") if s]
    # Writers on a shared host are different people, whatever their sitemap walk reads
    # today. Folding them would hand every one of them to whichever came first.
    if host in MULTI_TENANT_HOSTS or (segments and segments[0].startswith(("@", "~"))):
        return keys

    keys.append(f"sitemap:{host}")
    return keys


def survivor_rank(entry: dict[str, Any], pinned: set[str]) -> tuple:
    """Lower is kept. A corrected entry, then a feed, then https, then the oldest id.

    A feed is the crawler's fast and accurate route, so the survivor should carry one
    when any of the group does. Among the rest, `sentry` was handed out before
    `sentry-2`, and has been collecting articles for longer.
    """
    return (
        site_key(entry["site"]) not in pinned,
        not entry.get("feed"),
        not entry["site"].startswith("https://"),
        len(entry["id"]),
        entry["id"],
    )


def merge_duplicate_crawls(
    entries: list[dict[str, Any]],
    pinned: set[str] | None = None,
) -> tuple[list[dict[str, Any]], dict[str, str]]:
    """Fold entries that crawl the same posts into one.

    `pinned` holds the site_keys an override names. Those entries are never folded away,
    since an override is a decision made by hand, and they are not edited either.

    Returns the entries left, in the order given, and {folded id: surviving id}: the map
    an index clean-up needs to find the copies the folded ids left behind.
    """
    pinned = pinned or set()

    # Union-find over entries, joined through any key they share.
    parent = list(range(len(entries)))

    def root(i: int) -> int:
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    first_with: dict[str, int] = {}
    for i, entry in enumerate(entries):
        for key in crawl_keys(entry):
            j = first_with.setdefault(key, i)
            parent[root(i)] = root(j)

    groups: dict[int, list[int]] = {}
    for i in range(len(entries)):
        groups.setdefault(root(i), []).append(i)

    folded: dict[str, str] = {}
    dropped: set[int] = set()
    updated: dict[int, dict[str, Any]] = {}

    for members in groups.values():
        if len(members) == 1:
            continue

        members.sort(key=lambda i: survivor_rank(entries[i], pinned))
        keeper = members[0]
        losers = [i for i in members[1:]
                  if site_key(entries[i]["site"]) not in pinned]
        if not losers:
            continue

        survivor = dict(entries[keeper])
        for i in losers:
            folded[entries[i]["id"]] = survivor["id"]
            dropped.add(i)

        # The losers are the same blog, so what the build learned about them is still
        # true of it. A pinned survivor is left exactly as its override wrote it.
        if site_key(survivor["site"]) not in pinned:
            group = [survivor, *(entries[i] for i in losers)]
            survivor["feed"] = next((e["feed"] for e in group if e.get("feed")), None)
            kinds = [k for e in group for k in e.get("kind") or []]
            # None rather than [], which ordered_entry would write as `kind: []`.
            survivor["kind"] = list(dict.fromkeys(kinds)) or None
            survivor["tags"] = ordered_tags({t for e in group for t in e.get("tags") or []})
        updated[keeper] = survivor

    kept = [updated.get(i, entry) for i, entry in enumerate(entries) if i not in dropped]
    return [ordered_entry(e) for e in kept], folded
