"""Builds the short list of blogs the landing page offers before anyone has searched.

Reads the standing the scoring timer gathers into popularity.json, keeps the entries
that are actually blogs, and writes the top few to a JSON file the web app imports at
build time. See docs/plans/popular-blogs-landing-plan.md for why it is generated into Git
rather than served from an API.

Ranking by Hacker News points alone puts the BBC, TechCrunch and the Guardian on the
front page of a search engine for independent tech blogs: they are in the corpus, and
points measure news circulation. The kind filter below is what excludes them, and it
uses corpus data rather than anyone's opinion — those entries arrived from general link
lists and carry no kind at all.

    python build_popular.py --popularity /tmp/popularity.json

Run through `make popular`, which downloads the blob first.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Any, Iterable
from urllib.parse import urlencode, urlparse
from urllib.request import Request, urlopen

import yaml

ROOT = Path(__file__).resolve().parents[2]
BLOGS = ROOT / "sources" / "blogs.yml"
OUT = ROOT / "web" / "src" / "lib" / "data" / "popular.json"

# How many blogs the page shows. Two columns of six, which fills the space under the
# search box without pushing the page into a scroll, and is twelve favicon requests to
# twelve other people's servers rather than the twenty a page of results already makes.
LIMIT = 12

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
# crawl entirely, and they are worth indexing — they are just not what this page is for.
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
# than a name: overreacted.io calls itself "overreacted ' + chr(8212) + ' A blog by Dan Abramov" and
# idiallo.com appends its own domain. The tail is never the name, and a dash is not
# something this page should print either.
TAGLINE_SEPARATORS = (" " + chr(8212) + " ", " " + chr(8211) + " ", " - ", " | ", " :: ")

# A name longer than this is a tagline, not a name — idiallo.com's is "Software and Tech
# stories from an Insider". Too long to sit in a two-column row, and too long to be a
# sensible query.
MAX_NAME = 40
MIN_NAME = 3

# The fewest indexed articles a blog may have and still be offered.
#
# Clicking a blog opens onto its posts, so a blog the crawler has not reached opens onto
# nothing: utcc.utoronto.ca has one of the highest standings in the corpus and zero
# documents. A handful is also too few to be worth a click, hence a floor rather than
# merely "more than none".
MIN_ARTICLES = 5


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


def load_sources(path: Path) -> list[dict[str, Any]]:
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    return [s for s in (data.get("sources") or []) if isinstance(s, dict) and s.get("site")]


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


# Mirrors maxSourceIDs in api/internal/httpapi. A blog listed more times than the API
# will filter on cannot be reached completely, so it is not offered at all.
MAX_IDS = 8


def rank(
    sources: Iterable[dict[str, Any]],
    popularity: dict[str, dict[str, Any]],
) -> list[dict[str, Any]]:
    """Every eligible blog, most widely shared first, one row per host."""
    # Counted over distinct hosts, not over sources: one blog often appears in blogs.yml
    # several times over — tbray.org four times — and those are the same name for the
    # same writing, not two blogs competing for it.
    hosts_by_name: dict[str, set[str]] = {}
    # Every id a host is crawled under, because clicking a blog filters on them and one
    # blog is often listed more than once: righto.com has two ids, and filtering only
    # one of them returns 5 of its articles where both return 57.
    ids_by_host: dict[str, list[str]] = {}
    for source in sources:
        host = host_of(str(source["site"]))
        name = " ".join(str(source.get("name") or "").split()).lower()
        if name:
            hosts_by_name.setdefault(name, set()).add(host)
        if source.get("id") and host:
            ids = ids_by_host.setdefault(host, [])
            if source["id"] not in ids:
                ids.append(str(source["id"]))

    best: dict[str, dict[str, Any]] = {}
    for source in sources:
        kinds = set(source.get("kind") or ())
        if not kinds & BLOG_KINDS:
            continue

        host = host_of(str(source["site"]))
        if not host or host in DENY_HOSTS:
            continue

        points = int((popularity.get(host) or {}).get("points") or 0)
        if points <= 0:
            continue

        name = " ".join(str(source.get("name") or "").split())
        # Several sources can share a host; the one with a usable name wins, and points
        # are a property of the host so they cannot break the tie.
        if len(ids_by_host.get(host, [])) > MAX_IDS:
            continue

        current = best.get(host)
        if current and current["problem"] is None:
            continue

        best[host] = {
            "name": name,
            "site": str(source["site"]),
            "host": host,
            "ids": ids_by_host.get(host, []),
            "points": points,
            "problem": name_problem(name, len(hosts_by_name.get(name.lower(), ()))),
        }

    return sorted(best.values(), key=lambda row: (-row["points"], row["host"]))


def indexed(endpoint: str, key: str, ids: list[str]) -> int:
    """How many articles the index holds for one blog.

    The same filter the app sends when a reader clicks the blog, so this measures the
    page they will actually land on rather than something adjacent to it.
    """
    clause = " or ".join("sourceId eq '%s'" % i for i in ids)
    params = urlencode({
        "api-version": "2024-07-01",
        "search": "*",
        "$filter": "(%s)" % clause,
        "$top": "0",
        "$count": "true",
    })
    url = "%s/indexes/articles/docs?%s" % (endpoint.rstrip("/"), params)
    request = Request(url, headers={"api-key": key})
    with urlopen(request, timeout=30) as response:
        return int(json.load(response)["@odata.count"])


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--popularity", type=Path, required=True,
                        help="popularity.json, as downloaded from the sources container")
    parser.add_argument("--blogs", type=Path, default=BLOGS)
    parser.add_argument("--out", type=Path, default=OUT)
    parser.add_argument("--limit", type=int, default=LIMIT)
    parser.add_argument("--endpoint", default=os.environ.get("BLOGME_SEARCH_ENDPOINT", ""),
                        help="search endpoint, used to check each blog has articles to show")
    parser.add_argument("--key", default=os.environ.get("BLOGME_SEARCH_API_KEY", ""),
                        help="search query key")
    args = parser.parse_args(argv)

    popularity = json.loads(args.popularity.read_text(encoding="utf-8"))
    sources = load_sources(args.blogs)
    ranked = rank(sources, popularity)

    named = [row for row in ranked if row["problem"] is None]

    # Walk down the ranking taking blogs the index can actually show, rather than taking
    # the top twelve and hoping. Without an endpoint the check is skipped and the list is
    # the top twelve by standing alone, which is how it behaves offline.
    chosen: list[dict[str, Any]] = []
    if args.endpoint and args.key:
        for row in named:
            if len(chosen) == args.limit:
                break
            try:
                row["articles"] = indexed(args.endpoint, args.key, row["ids"])
            except Exception as err:  # noqa: BLE001 - any failure here means "cannot vouch"
                print(f"warning: could not count {row['host']}: {err}", file=sys.stderr)
                continue
            if row["articles"] >= MIN_ARTICLES:
                chosen.append(row)
            else:
                row["problem"] = f"only {row['articles']} articles indexed"
    else:
        print("warning: no search endpoint given, blogs not checked for articles",
              file=sys.stderr)
        chosen = named[: args.limit]

    if len(chosen) < args.limit:
        print(f"warning: only {len(chosen)} blogs qualified, wanted {args.limit}", file=sys.stderr)

    # Everything a better name would have promoted into the list, so the rejections
    # worth acting on are the ones printed and the rest stay quiet.
    cutoff = chosen[-1]["points"] if chosen else 0
    rejected = [row for row in ranked if row["problem"] and row["points"] >= cutoff]

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(
        json.dumps(
            {
                # What the twelve stand for, so the page can say how much it is not
                # showing without a pasted number going stale behind it.
                "corpus": len(sources),
                "blogs": [
                    {"name": r["name"], "site": r["site"], "host": r["host"], "ids": r["ids"]}
                    for r in chosen
                ],
            },
            indent="\t",
            ensure_ascii=False,
        )
        + "\n",
        encoding="utf-8",
        # LF, whatever the platform: the repo is checked out with LF and prettier
        # rewrites the file otherwise, so a generated list would fail the web lint.
        newline="\n",
    )

    print(f"{len(ranked)} eligible blogs, {len(chosen)} written to {args.out.relative_to(ROOT)}\n")
    for i, row in enumerate(chosen, 1):
        articles = row.get("articles")
        shown = f"{articles:>5} articles" if articles is not None else "not checked"
        print(f"{i:>3}. {row['points']:>6} pts  {shown}  {row['name']:<40} {row['host']}")

    if rejected:
        print("\nRejected, and popular enough to have made the list. Name them in"
              " sources/blogs-overrides.yml:")
        for row in rejected:
            print(f"     {row['points']:>6}  {row['host']:<28} {row['problem']}"
                  f"  — {row['name']!r}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
