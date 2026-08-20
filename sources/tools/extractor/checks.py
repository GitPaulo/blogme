"""Checking one candidate: is it reachable, what is it called, does it have a feed."""

from __future__ import annotations

import asyncio
import re

import feedparser
import httpx

from .models import Candidate, FeedInfo, FeedLookup
from .naming import clean_name, declared_feeds, page_metadata
from .tags import tags_from_content
from .urls import (canonical_site, dedupe_urls, domain_name,
                   feed_guesses_for_site, site_key)

FEED_ROOT_RE = re.compile(rb"<(rss|feed|rdf:RDF)[\s>]", re.IGNORECASE)

PAGE_CONTENT_TYPES = ("text/html", "application/xhtml", "text/plain", "xml")
MAX_HEAD_BYTES = 150_000
MAX_FEED_BYTES = 400_000

# A dead host fails on connect, so keep that budget short; a slow but live host is
# worth waiting for, so keep the read budget long.
#
# Tightening these does not make a run shorter, it makes it emptier: cutting the site
# budget to 2.5s connect made a sample 33% faster and cost it a quarter of its sources
# and 63 of its feeds. A run is limited by CPU, not by waiting; see README.md.
SITE_TIMEOUT = httpx.Timeout(12.0, connect=5.0)

FEED_TIMEOUT = httpx.Timeout(10.0, connect=5.0)

# The retry pass. Most sites that fail to connect at full concurrency are alive and were
# simply outrun: sampling the dropped links showed roughly three quarters of them
# answering when given a longer connect budget and fewer competitors for it.
RETRY_SITE_TIMEOUT = httpx.Timeout(20.0, connect=15.0)


async def fetch_site(
    client: httpx.AsyncClient,
    url: str,
    timeout: httpx.Timeout = SITE_TIMEOUT,
) -> tuple[int | None, str, str, str]:
    """Return (status, content type, page head or error message, final URL)."""
    try:
        async with client.stream("GET", url, follow_redirects=True, timeout=timeout) as response:
            chunks: list[bytes] = []
            total = 0
            async for chunk in response.aiter_bytes():
                chunks.append(chunk)
                total += len(chunk)
                # Only <head> is needed, so stop as soon as it closes.
                if total > MAX_HEAD_BYTES or b"</head" in chunk or b"</HEAD" in chunk:
                    break
            body = b"".join(chunks).decode(
                response.encoding or "utf-8", errors="replace")
            return response.status_code, response.headers.get("content-type", ""), body, str(response.url)
    except Exception as exc:
        return None, "", f"{type(exc).__name__}: {exc}"[:200], url


async def read_feed(client: httpx.AsyncClient, feed_url: str) -> FeedInfo | None:
    """Return feed details when the URL really is a feed, else None.

    Transport failures are raised rather than swallowed, because a timeout is not
    evidence that a blog has no feed. Recording one as such is how a working feed is
    lost for good: the entry is written without it, the crawler falls back to the
    slower sitemap path, and for a site with no sitemap it stops reading the blog at
    all. The caller decides what an unreachable candidate means; see find_feed.
    """
    response = await client.get(feed_url, follow_redirects=True, timeout=FEED_TIMEOUT)
    if response.status_code >= 400 or not FEED_ROOT_RE.search(response.content[:2000]):
        return None

    try:
        parsed = feedparser.parse(response.content[:MAX_FEED_BYTES])
    except Exception:
        # Malformed past what feedparser will tolerate, which is a verdict about the
        # document rather than a blip in reaching it.
        return None

    if not parsed.feed or not (parsed.entries or parsed.feed.get("title")):
        return None

    text = [str(parsed.feed.get("title", "")),
            str(parsed.feed.get("subtitle", ""))]
    categories: list[str] = []
    for entry in parsed.entries[:15]:
        text.append(str(entry.get("title", "")))
        text.append(str(entry.get("summary", ""))[:500])
        categories.extend(str(t.get("term", ""))
                          for t in entry.get("tags", []) or [])

    return FeedInfo(
        title=clean_name(parsed.feed.get("title")),
        text=" ".join(text),
        categories=categories,
    )


async def find_feed(
    client: httpx.AsyncClient,
    urls: list[str],
    batch: int = 8,
) -> FeedLookup:
    """First working feed among the given URLs, tried a batch at a time.

    Reports separately whether any candidate was unreachable, so the caller can tell
    a blog that has no feed from a run that failed to find one.
    """
    inconclusive = False

    for start in range(0, len(urls), batch):
        chunk = urls[start:start + batch]
        results = await asyncio.gather(
            *(read_feed(client, url) for url in chunk), return_exceptions=True)
        for url, result in zip(chunk, results):
            if isinstance(result, BaseException):
                inconclusive = True
            elif result is not None:
                return FeedLookup(url=url, info=result, inconclusive=inconclusive)

    return FeedLookup(inconclusive=inconclusive)


async def discard(task: asyncio.Task | None) -> None:
    """Abandon a feed lookup whose answer is no longer wanted.

    Cancelling alone is not enough. A lookup that had already failed still holds its
    exception, and an exception nobody retrieves is reported by asyncio when the task
    is collected — which fills the log of a perfectly healthy run with tracebacks for
    sites that were dropped for unrelated reasons.
    """
    if task is None:
        return
    task.cancel()
    await asyncio.gather(task, return_exceptions=True)


async def settled(task: asyncio.Task | None) -> tuple[FeedInfo | None, bool]:
    """A feed task's outcome as (info, unreachable), never raising.

    Mirrors find_feed: a transport failure is reported rather than raised, because a
    feed that could not be reached is not a feed that is gone.
    """
    if task is None:
        return None, False
    try:
        return await task, False
    except Exception:
        return None, True


async def check_candidate(
    client: httpx.AsyncClient,
    candidate: Candidate,
    require_feed: bool,
    timeout: httpx.Timeout = SITE_TIMEOUT,
    known_feeds: dict[str, str] | None = None,
) -> Candidate | None:
    """Fill in a reachable candidate, or record why it was dropped."""
    known_feeds = known_feeds or {}

    # The feed already in hand: whichever a seed list handed over, or failing that the
    # one the last build recorded. It does not depend on the homepage, so the two go
    # out together and the check costs the slower rather than their sum; about 40% of
    # candidates qualify.
    #
    # Matched on urls.site_key rather than on the URL as written, because a list says
    # http://www.example.com where the last build recorded https://example.com, having
    # followed the redirect the list never did. Without that only a quarter of the
    # candidates that have a feed would be recognised as having one. A site that
    # redirects somewhere else entirely is picked up after the fetch, below.
    upfront = candidate.feed or known_feeds.get(site_key(candidate.site))
    upfront_task = (asyncio.create_task(read_feed(client, upfront))
                    if upfront else None)

    status, content_type, body_or_error, final_url = await fetch_site(client, candidate.site, timeout)
    candidate.status_code = status

    dropped = None
    if status is None:
        dropped = body_or_error
    elif status >= 400:
        dropped = f"http {status}"
    else:
        content_type = content_type.lower()
        if content_type and not any(x in content_type for x in PAGE_CONTENT_TYPES):
            dropped = f"non-page content-type: {content_type.split(';')[0]}"

    if dropped is not None:
        await discard(upfront_task)
        candidate.error = dropped
        return None

    # Record where the site actually lives, so http/www redirects collapse together.
    candidate.site = canonical_site(final_url) or candidate.site
    page = page_metadata(body_or_error)

    # The feed we already had, re-checked like any other: kept only while it works.
    feed, upfront_unreachable = await settled(upfront_task)
    feed_url = upfront if feed else None

    # A redirect can land the candidate on a site whose feed is recorded under that
    # name instead. That one has not been asked yet, so it joins the hunt below.
    moved = known_feeds.get(site_key(candidate.site))
    if moved == upfront:
        moved = None

    inconclusive = upfront_unreachable
    if feed is None:
        advertised = ([moved] if moved else []) + \
            declared_feeds(body_or_error, candidate.site)

        # Sites that advertise a feed cost one request; only the rest get path guesses.
        lookup = await find_feed(client, dedupe_urls(advertised))
        if not lookup.url:
            guesses = dedupe_urls(feed_guesses_for_site(
                candidate.site), skip=set(advertised))
            guessed = await find_feed(client, guesses)
            lookup = FeedLookup(
                url=guessed.url,
                info=guessed.info,
                inconclusive=lookup.inconclusive or guessed.inconclusive,
            )

        feed, feed_url = lookup.info, lookup.url
        inconclusive = inconclusive or lookup.inconclusive

    # Nothing found, but the looking was unreliable: keep what the last build knew
    # rather than writing an absence a timeout invented.
    candidate.feed = feed_url or ((upfront or moved) if inconclusive else None)

    if require_feed and not candidate.feed:
        candidate.error = "no valid feed found"
        return None

    # The site's own metadata beats link text from a list, which is often a post title.
    candidate.name = (
        (feed.title if feed else None)
        or page.site_name
        or page.title
        or clean_name(candidate.name)
        or domain_name(candidate.site)
    )

    described_by = [candidate.name, page.title,
                    page.description, feed.text if feed else None]
    topics = tags_from_content(
        " ".join(filter(None, described_by)), feed.categories if feed else ())
    # A blog that says nothing about itself keeps the subject its source list implies.
    candidate.tags.update(topics or candidate.fallback_tags)
    return candidate


def never_answered(candidate: Candidate) -> bool:
    """True when the check got no HTTP response at all, rather than an unwelcome one.

    A 404 or a 403 is an answer and settles the question. A connect timeout does not:
    it is as likely to describe the run as the site.
    """
    return candidate.status_code is None
