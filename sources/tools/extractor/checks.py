"""Checking one candidate: is it reachable, what is it called, does it have a feed."""

from __future__ import annotations

import asyncio
import re

import feedparser
import httpx

from .models import Candidate, FeedInfo
from .naming import clean_name, declared_feeds, page_metadata
from .progress import Progress
from .tags import tags_from_content
from .urls import canonical_site, dedupe_urls, domain_name, feed_guesses_for_site

FEED_ROOT_RE = re.compile(rb"<(rss|feed|rdf:RDF)[\s>]", re.IGNORECASE)

PAGE_CONTENT_TYPES = ("text/html", "application/xhtml", "text/plain", "xml")
MAX_HEAD_BYTES = 150_000
MAX_FEED_BYTES = 400_000

# A dead host fails on connect, so keep that budget short; a slow but live host is
# worth waiting for, so keep the read budget long.
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
    """Return feed details when the URL really is a feed, else None."""
    try:
        r = await client.get(feed_url, follow_redirects=True, timeout=FEED_TIMEOUT)
        if r.status_code >= 400 or not FEED_ROOT_RE.search(r.content[:2000]):
            return None

        parsed = feedparser.parse(r.content[:MAX_FEED_BYTES])
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
    except Exception:
        return None


async def find_feed(
    client: httpx.AsyncClient,
    urls: list[str],
    batch: int = 8,
) -> tuple[str | None, FeedInfo | None]:
    """First working feed among the given URLs, tried a batch at a time."""
    for start in range(0, len(urls), batch):
        chunk = urls[start:start + batch]
        results = await asyncio.gather(*(read_feed(client, url) for url in chunk))
        for url, info in zip(chunk, results):
            if info is not None:
                return url, info
    return None, None


async def check_candidate(
    client: httpx.AsyncClient,
    candidate: Candidate,
    require_feed: bool,
    timeout: httpx.Timeout = SITE_TIMEOUT,
) -> Candidate | None:
    """Fill in a reachable candidate, or record why it was dropped."""
    status, content_type, body_or_error, final_url = await fetch_site(client, candidate.site, timeout)
    candidate.status_code = status

    if status is None:
        candidate.error = body_or_error
        return None

    if status >= 400:
        candidate.error = f"http {status}"
        return None

    content_type = content_type.lower()
    if content_type and not any(x in content_type for x in PAGE_CONTENT_TYPES):
        candidate.error = f"non-page content-type: {content_type.split(';')[0]}"
        return None

    # Record where the site actually lives, so http/www redirects collapse together.
    candidate.site = canonical_site(final_url) or candidate.site
    page = page_metadata(body_or_error)

    advertised = ([candidate.feed] if candidate.feed else []) + \
        declared_feeds(body_or_error, candidate.site)

    # Sites that advertise a feed cost one request; only the rest get path guesses.
    feed_url, feed = await find_feed(client, dedupe_urls(advertised))
    if not feed_url:
        guesses = dedupe_urls(feed_guesses_for_site(
            candidate.site), skip=set(advertised))
        feed_url, feed = await find_feed(client, guesses)

    candidate.feed = feed_url
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


async def check_candidate_limited(
    client: httpx.AsyncClient,
    candidate: Candidate,
    require_feed: bool,
    limit: asyncio.Semaphore,
    progress: Progress,
    timeout: httpx.Timeout = SITE_TIMEOUT,
) -> Candidate | None:
    async with limit:
        try:
            return await check_candidate(client, candidate, require_feed, timeout)
        finally:
            progress.tick()


def never_answered(candidate: Candidate) -> bool:
    """True when the check got no HTTP response at all, rather than an unwelcome one.

    A 404 or a 403 is an answer and settles the question. A connect timeout does not:
    it is as likely to describe the run as the site.
    """
    return candidate.status_code is None
