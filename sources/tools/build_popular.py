"""Builds the short list of blogs the landing page offers before anyone has searched.

Reads the standing the scoring timer gathers into popularity.json, keeps the entries
that are actually blogs, and writes the top few to a JSON file the web app imports at
build time. See docs/plans/popular-blogs-landing-plan.md for why it is generated into Git
rather than served from an API.

Ranking by Hacker News points alone puts the BBC, TechCrunch and the Guardian on the
front page of a search engine for independent tech blogs: they are in the corpus, and
points measure news circulation. The kind filter in corpus.py is what excludes them, and
it uses corpus data rather than anyone's opinion - those entries arrived from general
link lists and carry no kind at all.

    python build_popular.py --popularity /tmp/popularity.json

Run through `make popular`, which downloads the blob first, and weekly by
.github/workflows/refresh-popular.yml, which opens a pull request with whatever changed.
Its companion is build_trending.py, which fills the section above this one.

It refuses rather than degrades. Every check below ends in either a complete list or a
non-zero exit, because the caller is now a job nobody watches: a warning on stderr that
still writes a file is how a front page ends up showing seven blogs, or none.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Any, Iterable

from corpus import (
    BLOG_KINDS,
    BLOGS,
    DENY_HOSTS,
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

OUT = ROOT / "web" / "src" / "lib" / "data" / "popular.json"

# How many blogs the page shows. Two columns of six, which fills the space under the
# search box without pushing the page into a scroll, and is twelve favicon requests to
# twelve other people's servers rather than the twenty a page of results already makes.
LIMIT = 12

# The fewest indexed articles a blog may have and still be offered.
#
# Clicking a blog opens onto its posts, so a blog the crawler has not reached opens onto
# nothing: utcc.utoronto.ca has one of the highest standings in the corpus and zero
# documents. A handful is also too few to be worth a click, hence a floor rather than
# merely "more than none".
MIN_ARTICLES = 5

# How far down the ranking the article check is willing to walk to fill the list.
#
# Without a bound, a systematic fault - a filter the index rejects, a key with no read
# access - becomes thousands of requests before anything is reported. Twelve blogs have
# never needed more than the low twenties, so reaching this is a fault rather than a
# shortfall, and it is reported as one.
MAX_SCAN = 60

# The smallest popularity map worth ranking. A truncated download, an empty blob and a
# sweep that has never completed all read as "nobody has ever been posted", which the
# ranking cannot tell from the truth. The real map carries around 9,000 sites with
# points, so this is far below anything a healthy sweep produces.
# See docs/popularity.md.
MIN_SITES_WITH_POINTS = 500


def load_popularity(path: Path) -> dict[str, dict[str, Any]]:
    """The site standing map, checked for the shapes a bad download takes.

    The same argument infra/upload-sources.sh makes before it writes: a file read from
    somewhere else can arrive empty, and every failure here is silent downstream because
    "no points" is a value the ranking accepts rather than one it can question.
    """
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as err:
        raise Refused(f"could not read {path}: {err}") from err

    if not isinstance(data, dict):
        raise Refused(f"{path} holds {type(data).__name__}, not an object of sites")

    with_points = sum(
        1 for entry in data.values()
        if isinstance(entry, dict) and int(entry.get("points") or 0) > 0
    )
    if with_points < MIN_SITES_WITH_POINTS:
        raise Refused(
            f"{path} has only {with_points} sites with any standing, under the "
            f"{MIN_SITES_WITH_POINTS} a healthy sweep produces - a truncated download "
            "is far likelier than a real collapse"
        )
    return data


def rank(
    sources: Iterable[dict[str, Any]],
    popularity: dict[str, dict[str, Any]],
) -> list[dict[str, Any]]:
    """Every eligible blog, most widely shared first, one row per host."""
    sources = list(sources)
    by_name = hosts_by_name(sources)
    ids_by_host = source_ids_by_host(sources)

    best: dict[str, dict[str, Any]] = {}
    for source in sources:
        host = host_of(str(source["site"]))
        if not is_blog(source, host):
            continue

        points = int((popularity.get(host) or {}).get("points") or 0)
        if points <= 0:
            continue

        ids = ids_by_host.get(host, [])
        # No id is no way to browse the blog, and an empty list would build a filter the
        # index rejects - a 400 that would otherwise read as "the search is down".
        if not ids or len(ids) > MAX_IDS:
            continue

        # Several sources can share a host; the one with a usable name wins, and points
        # are a property of the host so they cannot break the tie.
        current = best.get(host)
        if current and current["problem"] is None:
            continue

        name = " ".join(str(source.get("name") or "").split())
        best[host] = {
            "name": name,
            "site": str(source["site"]),
            "host": host,
            "ids": ids,
            "points": points,
            "problem": name_problem(name, len(by_name.get(name.lower(), ()))),
        }

    return sorted(best.values(), key=lambda row: (-row["points"], row["host"]))


def indexed(endpoint: str, key: str, ids: list[str]) -> int:
    """How many articles the index holds for one blog.

    The same filter the app sends when a reader clicks the blog, so this measures the
    page they will actually land on rather than something adjacent to it.
    """
    clause = " or ".join("sourceId eq '%s'" % i for i in ids)
    answer = ask_index(endpoint, key, {
        "search": "*",
        "$filter": "(%s)" % clause,
        "$top": "0",
        "$count": "true",
    })
    return int(answer["@odata.count"])


def choose(
    named: list[dict[str, Any]],
    limit: int,
    endpoint: str,
    key: str,
) -> list[dict[str, Any]]:
    """The top `limit` blogs the index can actually show.

    Walks down the ranking rather than taking the head and hoping, because standing and
    coverage are unrelated: utcc.utoronto.ca is in the top twelve on points and has no
    documents at all.
    """
    if not endpoint or not key:
        raise Refused(
            "no search endpoint or key, so no blog can be checked for articles "
            "(pass --allow-unchecked to build the list on standing alone)"
        )

    chosen: list[dict[str, Any]] = []
    for row in named[:MAX_SCAN]:
        if len(chosen) == limit:
            return chosen
        row["articles"] = indexed(endpoint, key, row["ids"])
        if row["articles"] >= MIN_ARTICLES:
            chosen.append(row)
        else:
            row["problem"] = f"only {row['articles']} articles indexed"

    if len(chosen) < limit:
        raise Refused(
            f"only {len(chosen)} of the top {MAX_SCAN} blogs had {MIN_ARTICLES} or more "
            f"articles indexed, wanted {limit} - the index is likely incomplete"
        )
    return chosen


def previous_hosts(path: Path) -> dict[str, str]:
    """Host to name, as the committed list currently stands, for reporting what moved.

    A missing or unreadable file is the first run rather than a fault, and the report
    then reads as twelve additions.
    """
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    if not isinstance(data, dict):
        return {}
    return {
        str(blog.get("host")): str(blog.get("name", ""))
        for blog in (data.get("blogs") or [])
        if isinstance(blog, dict) and blog.get("host")
    }


def cell(text: str) -> str:
    """One Markdown table cell of text nobody here wrote.

    Names come from feed titles, so a pipe in one would end the column early and shift
    every cell after it. The name gate rejects " | " as a tagline separator but not a
    bare one, and a report a reviewer cannot read is a report they merge past.
    """
    return text.replace("\\", "\\\\").replace("|", "\\|")


def render(
    chosen: list[dict[str, Any]],
    rejected: list[dict[str, Any]],
    before: dict[str, str],
    corpus: int,
    unchecked: bool,
) -> str:
    """The pull request body, in Markdown.

    Written here rather than assembled in the workflow: this is the only place holding
    the points, the article counts and the previous list at once, and a shell reaching
    back into the JSON for them would be a second copy of these rules.
    """
    after = {row["host"] for row in chosen}
    added = [row for row in chosen if row["host"] not in before]
    removed = [(host, name) for host, name in before.items() if host not in after]

    out = [
        f"Regenerated from `popularity.json`. **{len(chosen)} "
        f"blog{'' if len(chosen) == 1 else 's'}**, drawn from {corpus:,} sources.",
        "",
    ]

    if added or removed:
        out += ["### What moved", "", "| | Blog | Host |", "| --- | --- | --- |"]
        out += [f"| add | {cell(row['name'])} | `{row['host']}` |" for row in added]
        out += [f"| drop | {cell(name)} | `{host}` |" for host, name in removed]
    else:
        out += ["### What moved", "", "The same blogs as before, in a different order."]
    out += [""]

    out += ["### The list", "", "| # | Blog | Host | Points | Articles |",
            "| ---: | --- | --- | ---: | ---: |"]
    for i, row in enumerate(chosen, 1):
        articles = row.get("articles")
        out.append(
            f"| {i} | {cell(row['name'])} | `{row['host']}` | {row['points']:,} | "
            f"{'not checked' if articles is None else format(articles, ',')} |"
        )
    out += [""]

    if unchecked:
        out += ["> Built on standing alone: no search key, so no blog here was checked "
                "for articles and a row may open onto nothing.", ""]

    if rejected:
        out += [
            "### Rejected, and popular enough to have made the list",
            "",
            "Name these in [`sources/blogs-overrides.yml`](sources/blogs-overrides.yml) "
            "to let them through.",
            "",
            "| Host | Name in the corpus | Problem |",
            "| --- | --- | --- |",
        ]
        out += [
            f"| `{row['host']}` | {cell(repr(row['name']))} | {cell(row['problem'])} |"
            for row in rejected
        ]
        out += [""]

    out += [
        "---",
        "",
        "Ranked by lifetime Hacker News points per host, which measures circulation in "
        "one audience and favours blogs that have been publishing for years. See "
        "[docs/popularity.md](docs/popularity.md) and "
        "[the landing page plan](docs/plans/popular-blogs-landing-plan.md).",
    ]
    return "\n".join(out) + "\n"


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
    parser.add_argument("--summary", type=Path,
                        help="write a Markdown report here, for a pull request body")
    parser.add_argument("--allow-unchecked", action="store_true",
                        help="build on standing alone when the index cannot be reached "
                             "(offline use; a blog may then open onto no articles)")
    args = parser.parse_args(argv)

    unchecked = args.allow_unchecked and not (args.endpoint and args.key)
    try:
        # An empty list is not a page, and the rejection report reads the last row.
        if args.limit < 1:
            raise Refused(f"--limit is {args.limit}; the page needs at least one blog")
        popularity = load_popularity(args.popularity)
        sources = load_sources(args.blogs)
        ranked = rank(sources, popularity)
        named = [row for row in ranked if row["problem"] is None]

        if not unchecked:
            chosen = choose(named, args.limit, args.endpoint, args.key)
        else:
            print("warning: no search endpoint, blogs not checked for articles",
                  file=sys.stderr)
            chosen = named[: args.limit]
            if len(chosen) < args.limit:
                raise Refused(f"only {len(chosen)} blogs qualified, wanted {args.limit}")
    except Refused as err:
        print(f"error: {err}", file=sys.stderr)
        print(f"nothing written, {args.out.name} is unchanged", file=sys.stderr)
        return 1

    # Everything a better name would have promoted into the list, so the rejections
    # worth acting on are the ones reported and the rest stay quiet.
    cutoff = chosen[-1]["points"]
    rejected = [row for row in ranked if row["problem"] and row["points"] >= cutoff]

    before = previous_hosts(args.out)
    write_json(args.out, {
        # What the twelve stand for, so the page can say how much it is not showing
        # without a pasted number going stale behind it.
        "corpus": len(sources),
        "blogs": [
            {"name": row["name"], "site": row["site"], "host": row["host"], "ids": row["ids"]}
            for row in chosen
        ],
    })
    if args.summary:
        args.summary.write_text(
            render(chosen, rejected, before, len(sources), unchecked),
            encoding="utf-8",
            newline="\n",
        )

    # Relative when it is in the repo, absolute when --out points elsewhere. Crashing
    # here would report a failure for a run that had already written a good list.
    where = args.out.relative_to(ROOT) if args.out.is_relative_to(ROOT) else args.out
    print(f"{len(ranked)} eligible blogs, {len(chosen)} written to {where}\n")
    for i, row in enumerate(chosen, 1):
        articles = row.get("articles")
        shown = f"{articles:>5} articles" if articles is not None else "not checked"
        print(f"{i:>3}. {row['points']:>6} pts  {shown}  {row['name']:<40} {row['host']}")

    if rejected:
        print("\nRejected, and popular enough to have made the list. Name them in"
              " sources/blogs-overrides.yml:")
        for row in rejected:
            print(f"     {row['points']:>6}  {row['host']:<28} {row['problem']}"
                  f"  {chr(8212)} {row['name']!r}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
