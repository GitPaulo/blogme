#!/usr/bin/env python3
"""Repairs article summaries mangled by two extractor bugs.

Both bugs were fixed in api/internal/discovery/extract.go, and neither fix reaches a
document already indexed: toArticle calls skipStored before it builds an article, so a
re-crawl skips every post the store already holds and never rewrites its summary. The
same trap repair_authors.py documents.

Two repairs, one per bug, selected with --mode:

  mdx       collectText handed MDX comments through as prose. A site that renders MDX
            to HTML for its pages but syndicates something closer to the source leaves
            them in the feed, and the place a lint pragma goes is the top of the file,
            so the paragraph carrying one is the first -- which is the one the card
            shows. Cards led with "{/* eslint-disable react/jsx-no-undef */}". Exact:
            the marker pair is unambiguous and is never prose.

  spacing   collectText wrote a space after every text node, so an inline tag left one
            in front of whatever punctuation followed it: "<em>big</em>." indexed as
            "big .", "<a>Create</a>:" as "Create :". A heuristic, because the tag
            boundaries are gone from the stored text and orthography is all that is
            left to go on. Deliberately narrow, and it will still close up the space
            French and friends put before a colon on purpose.

  all       both, in one pass.

Only summary is repaired. content carries the same spacing damage, but its analyzer
tokenises punctuation away before a query is ever matched against it, so the damage was
never visible there -- and rewriting a million content fields to fix nothing that shows
is not a pass worth paying for.

A repaired card reads correctly but stays shorter than a re-crawled one would: dropping
the comment here cannot recover the prose that the 55-word cap pushed off the end when
the article was indexed. Only a re-crawl gets that back.

Both modes send a "merge" action carrying only id and summary, never the "mergeOrUpload"
that Index.Upsert uses: that writes the whole document, and url, title and sourceId are
not omitempty, so a partial upload would blank them.

Dry run by default. Before writing, each document's current summary is appended to
--rollback as gzipped JSON Lines, streamed rather than accumulated: at 46k sources the
in-memory lists repair_authors.py keeps would not fit in a Codespace. Safe to re-run and
safe to interrupt -- a repaired summary no longer changes under the transform, so a
second pass finds nothing, and --resume skips the slices already finished.
"""

from __future__ import annotations

import argparse
import gzip
import json
import os
import re
import sys
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path

API_VERSION = "2024-07-01"

# Azure caps $top at 1000 and an indexing batch at 1000 documents. $skip cannot go past
# 100,000, so a slice has to be smaller than that before it can be walked at all; the
# ceiling below leaves room for documents a discovery pass adds while this one runs.
PAGE = 1000
BATCH = 1000
SKIP_LIMIT = 100_000
SLICE_MAX = 90_000

# The corpus predates none of this and carries a few dates from the future, so the
# window is wide on both sides. An empty range costs one count and is dropped.
RANGE_START = datetime(1990, 1, 1, tzinfo=timezone.utc)
RANGE_END = datetime(2036, 1, 1, tzinfo=timezone.utc)

MDX_OPEN, MDX_CLOSE = "{/*", "*/}"

# The space an inline tag left in front of the punctuation that followed it. Matched
# only between something that ends a word and a mark that closes a clause, and then
# only where that mark ends the text or is followed by a space leading to something
# that is not another mark. The two guards are what keep it off punctuation an author
# spaced on purpose: "1 .5" stays a split decimal, and "well . . ." stays an ellipsis
# rather than collapsing to "well. . .". Percent is left out of the class for the same
# reason -- "50 %" is a style, not damage.
INJECTED_BEFORE = re.compile(r"(\w|[)\]}])\s+([.,;:!?)\]}»])(?=$|\s(?![.,;:!?]))")

# The mirror case, where the tag opened the run: "(<em>x</em>)" came out "( x )".
INJECTED_AFTER = re.compile(r"([(\[{«])\s+(\w)")

# "<em>Go</em>'s" came out "Go 's". The \b keeps it off a quotation opening with an s.
INJECTED_POSSESSIVE = re.compile(r"(\w)\s+(['’]s)\b")


def strip_mdx_comments(s: str) -> str:
    """Mirrors stripMDXComments in api/internal/discovery/extract.go.

    A comment becomes a space rather than nothing, so the text either side of one does
    not weld; an unclosed marker takes the rest with it, which is what it means.
    """
    out: list[str] = []
    while True:
        open_at = s.find(MDX_OPEN)
        if open_at < 0:
            break
        # Searched past the opening marker so its own '*' cannot close it.
        body = open_at + len(MDX_OPEN)
        end = s.find(MDX_CLOSE, body)
        if end < 0:
            s = s[:open_at]
            break
        out.append(s[:open_at])
        out.append(" ")
        s = s[end + len(MDX_CLOSE):]
    out.append(s)
    return "".join(out)


def close_injected_spaces(s: str) -> str:
    for pattern in (INJECTED_BEFORE, INJECTED_AFTER, INJECTED_POSSESSIVE):
        s = pattern.sub(r"\1\2", s)
    return s


def make_transform(mode: str):
    """The repair for one mode, ending in the whitespace normalisation the crawler
    applies, so a summary that comes back unchanged really is untouched."""
    def transform(s: str) -> str:
        if mode in ("mdx", "all"):
            s = strip_mdx_comments(s)
        if mode in ("spacing", "all"):
            s = close_injected_spaces(s)
        return " ".join(s.split())
    return transform


def odata_time(t: datetime) -> str:
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


class Search:
    def __init__(self, endpoint: str, index: str, key: str) -> None:
        self.base = f"{endpoint.rstrip('/')}/indexes/{index}"
        self.key = key

    def call(self, method: str, path: str, body: dict | None) -> dict:
        req = urllib.request.Request(
            f"{self.base}{path}?api-version={API_VERSION}",
            method=method,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Content-Type": "application/json", "api-key": self.key},
        )
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                raw = resp.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as err:
            detail = err.read().decode("utf-8", "replace")[:500]
            raise SystemExit(f"search {method} {path} failed: {err.code} {detail}")

    def count(self, flt: str) -> int:
        body = {"search": "*", "filter": flt, "top": 0, "count": True}
        return self.call("POST", "/docs/search", body)["@odata.count"]

    def page(self, flt: str, skip: int) -> list[dict]:
        body = {"search": "*", "filter": flt, "select": "id,summary",
                "top": PAGE, "skip": skip}
        return self.call("POST", "/docs/search", body).get("value", [])

    def merge(self, docs: list[dict]) -> None:
        result = self.call("POST", "/docs/index", {"value": docs})
        failed = [r for r in result.get("value", []) if not r.get("status")]
        if failed:
            raise SystemExit(f"{len(failed)} documents rejected, first: {failed[0]}")


def scoped(flt: str, extra: str | None) -> str:
    """A slice filter narrowed by --filter.

    Composed before the slice is counted rather than after it is planned, so a run
    scoped to one blog prunes the empty ranges on the first count instead of walking
    the whole corpus to find out it did not want it.
    """
    return f"({flt}) and ({extra})" if extra else flt


def word_count_slices(client: Search, base: str, extra: str | None, out: list) -> None:
    """Splits a slice that dates alone could not get under the $skip ceiling.

    Reached only by the undated bucket, which is where the sitemap path puts a page
    whose own metadata carried no date.
    """
    edges = [0, 200, 400, 700, 1200, 2000, 4000, None]
    for lo, hi in zip(edges, edges[1:]):
        raw = f"{base} and wordCount ge {lo}" + (f" and wordCount lt {hi}" if hi else "")
        flt = scoped(raw, extra)
        n = client.count(flt)
        if n == 0:
            continue
        if n > SLICE_MAX:
            raise SystemExit(
                f"slice still holds {n} documents and cannot be split further:\n  {flt}\n"
                "Add an edge to word_count_slices, or narrow the run with --filter.")
        out.append((flt, n))


def date_slices(client: Search, lo: datetime, hi: datetime,
                extra: str | None, out: list) -> None:
    """Halves a date range until every slice fits under the $skip ceiling.

    $skip is the only paging a plain query gets from Azure AI Search and it stops at
    100,000, so the index cannot be walked in one pass. publishedAt is the field to
    split on: filterable, and spread thinly enough to halve cheaply.
    """
    raw = f"publishedAt ge {odata_time(lo)} and publishedAt lt {odata_time(hi)}"
    flt = scoped(raw, extra)
    n = client.count(flt)
    if n == 0:
        return
    if n <= SLICE_MAX:
        out.append((flt, n))
        return
    if hi - lo <= timedelta(days=1):
        word_count_slices(client, raw, extra, out)
        return
    mid = lo + (hi - lo) / 2
    date_slices(client, lo, mid, extra, out)
    date_slices(client, mid, hi, extra, out)


def plan(client: Search, extra: str | None) -> list:
    out: list = []
    date_slices(client, RANGE_START, RANGE_END, extra, out)

    # Everything the date window cannot reach: a post with no usable date of its own.
    undated = "publishedAt eq null"
    n = client.count(scoped(undated, extra))
    if n > SLICE_MAX:
        word_count_slices(client, undated, extra, out)
    elif n:
        out.append((scoped(undated, extra), n))

    return out


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--mode", choices=("mdx", "spacing", "all"), required=True,
                    help="which bug to repair; see the module docstring")
    ap.add_argument("--apply", action="store_true",
                    help="write the repaired summaries; without it nothing is sent")
    ap.add_argument("--filter", default=None,
                    help="extra OData filter, so the repair can be tried on one blog "
                         "before it is run over the corpus")
    ap.add_argument("--max-slices", type=int, default=0,
                    help="stop after this many slices, to sample a long dry run")
    ap.add_argument("--rollback", type=Path,
                    default=Path("summary-repair-rollback.jsonl.gz"),
                    help="where each summary is recorded before it is overwritten")
    ap.add_argument("--checkpoint", type=Path,
                    default=Path("summary-repair-checkpoint.json"),
                    help="slices already finished, so an interrupted run can resume")
    ap.add_argument("--resume", action="store_true",
                    help="skip the slices the checkpoint records as done")
    args = ap.parse_args()

    endpoint = os.environ.get("BLOGME_SEARCH_ENDPOINT", "")
    key = os.environ.get("BLOGME_SEARCH_API_KEY", "")
    index = os.environ.get("BLOGME_SEARCH_INDEX", "articles")
    if not endpoint or not key:
        print("BLOGME_SEARCH_ENDPOINT and BLOGME_SEARCH_API_KEY must be set")
        return 2

    client = Search(endpoint, index, key)
    transform = make_transform(args.mode)

    done = set()
    if args.resume and args.checkpoint.exists():
        done = set(json.loads(args.checkpoint.read_text(encoding="utf-8"))["done"])

    print(f"index     {index}")
    print(f"mode      {args.mode}")
    print(f"filter    {args.filter or 'whole index'}")
    print(f"writing   {'yes - documents will be changed' if args.apply else 'no, dry run'}")
    if done:
        print(f"resuming  {len(done)} slices already done")
    print()

    print("planning slices")
    slices = plan(client, args.filter)
    if args.max_slices:
        slices = slices[:args.max_slices]
    print(f"  {len(slices)} slices, {sum(n for _, n in slices)} documents")
    print()

    rollback = gzip.open(args.rollback, "at", encoding="utf-8") if args.apply else None

    scanned = 0
    changed = 0
    samples: list = []
    try:
        for i, (flt, total) in enumerate(slices, 1):
            if flt in done:
                continue
            pending: list = []

            def flush() -> None:
                if pending and args.apply:
                    client.merge(pending)
                pending.clear()

            for skip in range(0, min(total, SKIP_LIMIT), PAGE):
                rows = client.page(flt, skip)
                if not rows:
                    break
                for doc in rows:
                    scanned += 1
                    was = doc.get("summary") or ""
                    now = transform(was)
                    if now == was:
                        continue
                    changed += 1
                    if len(samples) < 8:
                        samples.append((was, now))
                    if rollback:
                        rollback.write(json.dumps(
                            {"id": doc["id"], "summary": doc.get("summary")},
                            ensure_ascii=False) + "\n")
                    # An empty repair is cleared rather than stored as "": the web omits
                    # a missing summary, and a blank card beats one led by a lint pragma.
                    pending.append({"@search.action": "merge", "id": doc["id"],
                                    "summary": now or None})
                    if len(pending) >= BATCH:
                        flush()
            flush()

            done.add(flt)
            if args.apply:
                rollback.flush()
                args.checkpoint.write_text(
                    json.dumps({"mode": args.mode, "done": sorted(done)}),
                    encoding="utf-8")
            print(f"  [{i}/{len(slices)}] {scanned} scanned, {changed} repaired")
    finally:
        if rollback:
            rollback.close()

    print()
    print(f"documents scanned:  {scanned}")
    print(f"summaries repaired: {changed}")

    if samples:
        print()
        print("sample:")
        for was, now in samples:
            print(f"  -  {was[:110]}")
            print(f"  +  {now[:110]}")
            print()

    if not args.apply:
        print("Dry run. Re-run with --apply to write these.")
        return 0

    print(f"rollback written to {args.rollback}")
    print(f"checkpoint written to {args.checkpoint}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
