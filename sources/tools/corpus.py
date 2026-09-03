"""What blogs.yml means, shared by the two generators that read it.

`build_popular.py` picks the twelve blogs the landing page recommends; `build_trending.py`
picks the four posts Hacker News is reading this week. They disagree about almost
everything - one ranks lifetime standing, the other this week's - and agree about what
counts as a blog, what a usable name is, and how to ask the index a question.

That agreement is the point. Two copies of the deny list would drift, and the front page
would offer under one heading what it refuses under the other.
"""

from __future__ import annotations

import json
import os
import time
from http.client import HTTPException
from pathlib import Path
from typing import Any, Iterable
from urllib.error import HTTPError
from urllib.parse import urlencode, urlparse
from urllib.request import Request, urlopen

import yaml

ROOT = Path(__file__).resolve().parents[2]
BLOGS = ROOT / "sources" / "blogs.yml"

# A source has to claim one of these to be eligible. Every mainstream news domain in the
# corpus has no kind at all, so this one line is the whole newspaper filter.
BLOG_KINDS = frozenset({
    "personal-blogs",
    "company-blogs",
    "independent-web",
    "small-web",
})

# Platforms, archives and standards bodies the extractor mis-kinded as blogs. Kept here
# rather than in blogs-overrides.yml because `drop` there would take them out of the
# crawl entirely, and they are worth indexing - they are just not what this page is for.
DENY_HOSTS = frozenset({
    "web.archive.org",
    "w3.org",
    "youtube.com",
    "linkedin.com",
    "github.com",
    "gitlab.com",
    "medium.com",
    "substack.com",
    "news.ycombinator.com",
    "reddit.com",
    "wikipedia.org",
    # Documentation portals and corporate newsrooms. They pass the kind filter, they
    # are worth indexing, and they are not what a reader looking for blogs came for.
    "developer.apple.com",
    "deepmind.google",
    "docs.google.com",
})

# Names too generic to be searched for. Clicking a row searches for the blog's name, so
# a blog called "Essays" would return the whole corpus rather than gwern.
GENERIC_NAMES = frozenset({
    "articles", "blog", "blogs", "essays", "home", "journal", "news", "notes",
    "posts", "thoughts", "updates", "writing", "writings", "index", "feed", "rss",
})

# Feed titles that describe the feed rather than the blog. A WordPress comments feed is
# the common one: steveblank.com's name in the corpus is "Comments for Steve Blank".
FEED_TITLE_PREFIXES = ("comments for ", "comments on ")

# A dash with spaces around it joins a name to a tagline, which is a feed title rather
# than a name: overreacted.io calls itself "overreacted <em dash> A blog by Dan Abramov"
# and idiallo.com appends its own domain. The tail is never the name, and a dash is not
# something this page should print either.
TAGLINE_SEPARATORS = (" " + chr(8212) + " ", " " + chr(8211) + " ", " - ", " | ", " :: ")

# A name longer than this is a tagline, not a name - idiallo.com's is "Software and Tech
# stories from an Insider". Too long to sit in a two-column row, and too long to be a
# sensible query.
MAX_NAME = 40
MIN_NAME = 3

# Mirrors maxSourceIDs in api/internal/httpapi. A blog listed more times than the API
# will filter on cannot be reached completely, so it is not offered at all.
MAX_IDS = 8

# The index REST version both generators query.
# See: https://learn.microsoft.com/en-us/rest/api/searchservice/search-documents
API_VERSION = "2024-07-01"

# Retries for one index question. Covers a search service restart or a throttle; anything
# longer-lived should stop the run rather than silently reshuffle the front page.
SEARCH_ATTEMPTS = 3
SEARCH_BACKOFF = 2.0


class Refused(RuntimeError):
    """A list could not be vouched for, so nothing is written.

    An exception rather than a return code, so each check reads as one line where the
    fault is known and each generator has a single place that reports them.
    """


def host_of(url: str) -> str:
    """The host a blog's writing lives under, matching siteOf in api/internal/quality.

    The full host rather than the registrable domain, and the same key the scorer writes
    popularity.json with: thousands of sources here are subdomains of a handful of
    blogging platforms, and folding them together would hand every blog on bearblog.dev
    the standing of the most popular one on it.
    """
    try:
        host = (urlparse(url).hostname or "").lower()
    except ValueError:
        return ""
    return host.removeprefix("www.")


def load_sources(path: Path = BLOGS) -> list[dict[str, Any]]:
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    sources = [s for s in (data.get("sources") or []) if isinstance(s, dict) and s.get("site")]
    if not sources:
        raise Refused(f"{path} lists no usable sources")
    return sources


def source_ids_by_host(sources: Iterable[dict[str, Any]]) -> dict[str, list[str]]:
    """Every id a host is crawled under.

    One blog is often listed more than once: righto.com has two ids, and filtering only
    one of them returns 5 of its articles where both return 57.
    """
    ids: dict[str, list[str]] = {}
    for source in sources:
        host = host_of(str(source["site"]))
        if not source.get("id") or not host:
            continue
        seen = ids.setdefault(host, [])
        if source["id"] not in seen:
            seen.append(str(source["id"]))
    return ids


def hosts_by_name(sources: Iterable[dict[str, Any]]) -> dict[str, set[str]]:
    """Which hosts answer to each name, for the ambiguity gate.

    Counted over distinct hosts, not over sources: one blog often appears in blogs.yml
    several times over - tbray.org four times - and those are the same name for the same
    writing, not two blogs competing for it.
    """
    hosts: dict[str, set[str]] = {}
    for source in sources:
        name = " ".join(str(source.get("name") or "").split()).lower()
        if name:
            hosts.setdefault(name, set()).add(host_of(str(source["site"])))
    return hosts


def is_blog(source: dict[str, Any], host: str) -> bool:
    """Whether this source is the kind of thing the landing page exists to show."""
    if not host or host in DENY_HOSTS:
        return False
    return bool(set(source.get("kind") or ()) & BLOG_KINDS)


def name_problem(name: str, host_count: int) -> str | None:
    """Why this name cannot be shown, or None if it can.

    A rejected blog simply falls out of the list until someone names it properly in
    blogs-overrides.yml. That is the right default: this page is an editorial surface,
    and a blog nobody has bothered to name is not ready to be on it.
    """
    cleaned = " ".join(name.split())
    lowered = cleaned.lower()

    if len(cleaned) < MIN_NAME:
        return "too short"
    if len(cleaned) > MAX_NAME:
        return f"too long ({len(cleaned)} chars, a tagline rather than a name)"
    if lowered.startswith(FEED_TITLE_PREFIXES):
        return "the title of a comments feed, not the blog"
    for sep in TAGLINE_SEPARATORS:
        if sep in cleaned:
            return "a name joined to a tagline by %r" % sep.strip()
    if lowered in GENERIC_NAMES:
        return "too generic to search for"
    # Ambiguous as a query whatever it says: two blogs answer to it, so clicking one
    # cannot reliably reach either.
    if host_count > 1:
        return f"the name of {host_count} different blogs"
    return None


def ask_index(endpoint: str, key: str, params: dict[str, Any]) -> dict[str, Any]:
    """One question to the search index, retried while the answer is unknown.

    A 4xx is an answer - the query is wrong, and asking again cannot change it. Every
    other failure is the service being out of reach, which is retried and then raised,
    because a row dropped for one timeout is a row silently swapped off the front page.
    """
    query = urlencode({"api-version": API_VERSION, **params})
    request = Request(f"{endpoint.rstrip('/')}/indexes/articles/docs?{query}",
                      headers={"api-key": key})

    last: Exception | None = None
    for attempt in range(1, SEARCH_ATTEMPTS + 1):
        try:
            with urlopen(request, timeout=30) as response:
                return json.load(response)
        except HTTPError as err:
            if err.code < 500:
                raise Refused(f"the index rejected a query: HTTP {err.code}") from err
            last = err
        # OSError rather than URLError: a connection reset, an SSL failure and a
        # RemoteDisconnected are all OSErrors that urlopen does not wrap, and
        # IncompleteRead - the body dying halfway - is not an OSError at all. Catching
        # only URLError left every one of those to crash an unattended job.
        except (OSError, HTTPException, ValueError) as err:
            last = err
        if attempt < SEARCH_ATTEMPTS:
            time.sleep(SEARCH_BACKOFF * attempt)

    raise Refused(f"could not reach the index after {SEARCH_ATTEMPTS} attempts: {last}")


def write_json(path: Path, payload: dict[str, Any]) -> None:
    """Replace a generated list in one step, so a crash cannot leave half a file.

    Vite inlines these at build time, and a truncated import is a build failure whose
    cause is a week behind it.
    """
    body = json.dumps(payload, indent="\t", ensure_ascii=False) + "\n"
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    # LF, whatever the platform: the repo is checked out with LF and prettier rewrites
    # the file otherwise, so a generated list would fail the web lint.
    tmp.write_text(body, encoding="utf-8", newline="\n")
    os.replace(tmp, path)
