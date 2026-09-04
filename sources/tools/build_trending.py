"""Builds the four posts at the top of the landing page: what is being read this week.

The six blogs below this section rank lifetime Hacker News points, which is a stable
ordering that has barely moved in a decade - that is its virtue, and its whole problem.
A reader who comes back sees the same page forever, and nothing on it says the index
knows about anything published since.

This asks a different question, and asks it the cheap way round. The scoring timer asks
"how popular is each of my 46,000 sites?", which is 46,000 lookups rationed at 2,000 an
hour. This asks "what is popular?" - one query - and intersects the answer with
blogs.yml locally.

    python build_trending.py

Run through `make trending`, and daily by .github/workflows/refresh-trending.yml.

Two measurements shaped it, both taken on 3 September 2026:

  - A 24-hour window yields **three** eligible blogs, which cannot fill four slots. Three
    days yields 18 and seven days 65, so the window is a week and the section says so.
    "Right now" is not something this corpus can honestly claim at four rows.
  - Of the trending posts, 8 in 14 were already in the index. Requiring that costs a
    third of the candidates and there are 64 of them, so every row is a post the search
    actually holds - which is the only thing that makes this section more than a slow
    mirror of a site the reader has already read this morning.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import time
from http.client import HTTPException
from pathlib import Path
from typing import Any, Iterable
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from corpus import (
    BLOGS,
    MAX_IDS,
    ROOT,
    Refused,
    ask_index,
    host_of,
    hosts_by_name,
    is_blog,
    load_sources,
    name_problem,
    source_ids_by_host,
    write_json,
)

OUT = ROOT / "web" / "src" / "lib" / "data" / "trending.json"
# Read only to avoid offering the same blog twice on one screen. Three of a seven-day
# window's hits were already among the twelve when this was measured.
POPULAR = ROOT / "web" / "src" / "lib" / "data" / "popular.json"

# Four, against the six below: enough to read at a glance, and few enough that the
# section cannot outweigh the list it sits above.
LIMIT = 4

# The window, and the floor a story clears to count as read. Seven days is the smallest
# that reliably fills four slots - see the measurements in the module docstring - and 50
# points is roughly where a story has been seen rather than merely posted.
WINDOW_DAYS = 7
MIN_POINTS = 50

# Candidates to put to the index before giving up. A seven-day window offers about 64,
# and roughly three in five are indexed, so reaching this is a fault rather than a quiet
# week, and it is reported as one.
MAX_SCAN = 40

# The public Hacker News index. Free, unauthenticated, 10,000 requests an hour per IP,
# and this spends one a day.
# See: https://hn.algolia.com/api
HN_SEARCH = "https://hn.algolia.com/api/v1/search"
HN_ATTEMPTS = 3
HN_BACKOFF = 2.0

# Below this, the window came back too thin to be believed - Algolia answered, but with
# something no healthy week produces. Measured weeks return around 400.
MIN_STORIES = 50


def stories(window_days: int, min_points: int) -> list[dict[str, Any]]:
    """The stories Hacker News has been reading, most points first.

    One request. Retried like any other network call, and refused rather than left to
    return an empty week, because "nothing was popular" and "the API was down" produce
    the same empty section.
    """
    since = int(time.time()) - window_days * 86400
    query = urlencode({
        "tags": "story",
        "numericFilters": f"created_at_i>{since},points>{min_points}",
        "hitsPerPage": 1000,
    })
    request = Request(f"{HN_SEARCH}?{query}", headers={"User-Agent": "blogme"})

    last: Exception | None = None
    for attempt in range(1, HN_ATTEMPTS + 1):
        try:
            with urlopen(request, timeout=30) as response:
                hits = json.load(response)["hits"]
                break
        except HTTPError as err:
            if err.code < 500:
                raise Refused(f"Hacker News rejected the query: HTTP {err.code}") from err
            last = err
        # As in corpus.ask_index: the failures that actually happen mid-body are not
        # URLErrors, and were escaping the retry entirely.
        except (OSError, HTTPException, ValueError, KeyError) as err:
            last = err
        if attempt < HN_ATTEMPTS:
            time.sleep(HN_BACKOFF * attempt)
    else:
        raise Refused(f"could not reach Hacker News after {HN_ATTEMPTS} attempts: {last}")

    if not isinstance(hits, list):
        raise Refused(f"Hacker News returned {type(hits).__name__}, not a list of stories")
    if len(hits) < MIN_STORIES:
        raise Refused(
            f"only {len(hits)} stories over {min_points} points in {window_days} days, "
            f"under the {MIN_STORIES} a normal week returns - the window looks truncated"
        )
    return sorted(hits, key=lambda h: -(h.get("points") or 0))


def article_id(source_id: str, url: str) -> str:
    """The key the crawler stored this article under.

    Mirrors articleID in api/internal/discovery/crawl.go, and has to keep mirroring it:
    the whole vouch below is one lookup by this key, so a change there silently empties
    this section rather than breaking it.
    """
    digest = hashlib.sha256(url.encode()).hexdigest()[:16]
    clean = "".join(
        c if (c.isascii() and (c.isalnum() or c in "_-")) else "-" for c in source_id
    )
    return f"{clean}-{digest}"


def candidates(
    sources: Iterable[dict[str, Any]],
    hits: Iterable[dict[str, Any]],
    already_shown: set[str],
) -> list[dict[str, Any]]:
    """Every trending story that is a blog this page would be willing to name.

    The same gates the six pass, for the reason corpus.py exists: a blog the list
    below refuses for its name should not appear above it under a different heading.
    One row per host, since a blog can trend twice in a week.
    """
    sources = list(sources)
    by_name = hosts_by_name(sources)
    ids_by_host = source_ids_by_host(sources)

    blogs: dict[str, dict[str, Any]] = {}
    for source in sources:
        host = host_of(str(source["site"]))
        if not is_blog(source, host) or host in already_shown:
            continue
        ids = ids_by_host.get(host, [])
        if not ids or len(ids) > MAX_IDS:
            continue
        name = " ".join(str(source.get("name") or "").split())
        if name_problem(name, len(by_name.get(name.lower(), ()))) is not None:
            continue
        # First usable name wins, as in build_popular: several sources share a host.
        blogs.setdefault(host, {"name": name, "host": host, "ids": ids})

    best: dict[str, dict[str, Any]] = {}
    for hit in hits:
        url = str(hit.get("url") or "")
        blog = blogs.get(host_of(url))
        if not url or blog is None or blog["host"] in best:
            continue
        # Hits arrive sorted, so the first one per host is its best of the week.
        best[blog["host"]] = {**blog, "url": url, "points": int(hit.get("points") or 0)}

    return list(best.values())


def indexed_article(endpoint: str, key: str, row: dict[str, Any]) -> dict[str, Any] | None:
    """The document this trending post is stored as, or None if the crawler never got it.

    Looked up by computed key rather than searched for: the key is exact, one request,
    and needs no guess about how a title was rewritten between the feed and Hacker News.
    A post the crawler stored under a differently-normalised URL simply misses, which is
    a row this section does without rather than a row that lies.
    """
    for source_id in row["ids"]:
        answer = ask_index(endpoint, key, {
            "search": "*",
            "$filter": f"id eq '{article_id(source_id, row['url'])}'",
            "$select": "title,url",
            "$top": "1",
        })
        found = answer.get("value") or []
        if found:
            return found[0]
    return None


def choose(
    rows: list[dict[str, Any]],
    limit: int,
    endpoint: str,
    key: str,
) -> list[dict[str, Any]]:
    """The `limit` most-read posts the index can actually open."""
    if not endpoint or not key:
        raise Refused("no search endpoint or key, so no post can be vouched for")

    chosen: list[dict[str, Any]] = []
    for row in sorted(rows, key=lambda r: -r["points"])[:MAX_SCAN]:
        if len(chosen) == limit:
            return chosen
        document = indexed_article(endpoint, key, row)
        if document is None:
            continue
        title = str(document.get("title") or "").strip()
        # An untitled document renders as an empty link, which is worse than one fewer
        # row. The crawler stores these when a feed entry carries no title at all.
        if not title:
            continue
        # The indexed title, not the one on Hacker News: titles there are edited by
        # moderators, and this page should print what the blog called its own post.
        chosen.append({**row, "title": title})

    if len(chosen) < limit:
        raise Refused(
            f"only {len(chosen)} of {min(len(rows), MAX_SCAN)} trending blogs were in the "
            f"index, wanted {limit}"
        )
    return chosen


def shown_below(path: Path) -> set[str]:
    """Hosts the six already offer, so this section does not repeat them."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return set()
    if not isinstance(data, dict):
        return set()
    return {
        str(blog["host"])
        for blog in (data.get("blogs") or [])
        if isinstance(blog, dict) and blog.get("host")
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--blogs", type=Path, default=BLOGS)
    parser.add_argument("--out", type=Path, default=OUT)
    parser.add_argument("--popular", type=Path, default=POPULAR)
    parser.add_argument("--limit", type=int, default=LIMIT)
    parser.add_argument("--window-days", type=int, default=WINDOW_DAYS)
    parser.add_argument("--min-points", type=int, default=MIN_POINTS)
    parser.add_argument("--endpoint", default=os.environ.get("BLOGME_SEARCH_ENDPOINT", ""),
                        help="search endpoint, used to check each post is in the index")
    parser.add_argument("--key", default=os.environ.get("BLOGME_SEARCH_API_KEY", ""),
                        help="search query key")
    args = parser.parse_args(argv)

    try:
        if args.limit < 1:
            raise Refused(f"--limit is {args.limit}; the section needs at least one post")
        sources = load_sources(args.blogs)
        hits = stories(args.window_days, args.min_points)
        rows = candidates(sources, hits, shown_below(args.popular))
        chosen = choose(rows, args.limit, args.endpoint, args.key)
    except Refused as err:
        print(f"error: {err}", file=sys.stderr)
        print(f"nothing written, {args.out.name} is unchanged", file=sys.stderr)
        return 1

    # No timestamp in the file: it would change on every run, so a day when nothing
    # trended differently would still commit, deploy, and say nothing new.
    write_json(args.out, {
        "windowDays": args.window_days,
        "posts": [
            {"title": row["title"], "url": row["url"],
             "blog": row["name"], "host": row["host"]}
            for row in chosen
        ],
    })

    where = args.out.relative_to(ROOT) if args.out.is_relative_to(ROOT) else args.out
    print(f"{len(hits)} stories, {len(rows)} eligible blogs, "
          f"{len(chosen)} written to {where}\n")
    for i, row in enumerate(chosen, 1):
        print(f"{i:>3}. {row['points']:>5} pts  {row['name']:<28} {row['title'][:52]}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
