#!/usr/bin/env python3
"""Fills titleSuggest on articles indexed before the field existed.

Typeahead is served by a suggester, and a suggester can only be built over a field
that is new to the index — Azure AI Search refuses to add an existing one, because
prefixes are generated at indexing time and the existing field is already tokenised.
So titleSuggest is a copy of title, and every document indexed before it existed
carries a null.

Discovery writes both fields from now on (see index.Upsert), which leaves exactly the
documents already in the index for this script.

There is no cursor and no queue. Each pass reads the head of the set of documents
carrying no suggestVersion and writes them, so the set shrinks by what was done and
asking again walks the rest of the corpus. That makes the run resumable: interrupt it
and start it again, and it picks up where it stopped. It also sidesteps $skip, which
Azure caps at 100,000 — far below this corpus.

Writes are "merge" actions carrying only id, titleSuggest and suggestVersion, never
the "mergeOrUpload" that Index.Upsert uses: that writes the whole document, and url,
title and sourceId are not omitempty, so a partial upload would blank them.

Dry run by default.

  infra/backfill-suggest.sh                       # report what would be written
  infra/backfill-suggest.sh --apply               # write it
  infra/backfill-suggest.sh --apply --since 2023-01-01
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

API_VERSION = "2024-07-01"
PAGE = 1000   # Azure caps $top at 1000,
BATCH = 1000  # and an indexing batch at 1000 documents.

# The value written to suggestVersion. Mirrors suggestVersion in
# api/internal/index/suggest.go; raising it there means raising it here.
VERSION = 1

# How long to wait when a pass reads back documents it has just written.
#
# Indexing is not immediate: a merge is durable when it is accepted but queryable a
# few seconds later, so the head of the set can still hold the batch just sent. That
# is harmless — a repeated merge writes the same values — but it would spin, so a
# pass that sees no new documents waits instead of asking again straight away.
SETTLE_SECONDS = 2

# How many passes in a row may come back with nothing new before giving up.
#
# Without this a write that is accepted but never applied would loop forever. With it
# the run stops and says so, which is the difference between a bug and a hang.
MAX_STALLED = 5


class Search:
    def __init__(self, endpoint: str, index: str, key: str) -> None:
        self.base = f"{endpoint.rstrip('/')}/indexes/{index}"
        self.key = key

    def _call(self, method: str, path: str, body: dict | None) -> dict:
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
        return self._call("POST", "/docs/search",
                          {"search": "*", "filter": flt, "top": 0, "count": True})["@odata.count"]

    def head(self, flt: str) -> list[tuple[str, str]]:
        """The next page of documents matching the filter, as (id, title).

        Always the head of the set rather than a page deeper into it: the documents
        read here leave the set as soon as they are written, so the next call returns
        the ones after them without any offset being tracked.
        """
        body = {"search": "*", "filter": flt, "select": "id,title", "top": PAGE}
        page = self._call("POST", "/docs/search", body).get("value", [])
        return [(d["id"], d.get("title") or "") for d in page]

    def merge(self, docs: list[dict]) -> int:
        """Writes one batch, returning how many documents were accepted."""
        result = self._call("POST", "/docs/index", {"value": docs})
        # 207 means some of the batch failed, and the reason is per document. Reported
        # rather than raised: a document that cannot be written now is one this run
        # leaves behind for the next, which is exactly what a resumable pass is for.
        failed = [d for d in result.get("value", []) if not d.get("status")]
        for d in failed[:3]:
            print(f"    ! {d.get('key')}: {d.get('errorMessage')}", file=sys.stderr)
        if len(failed) > 3:
            print(f"    ! and {len(failed) - 3} more", file=sys.stderr)
        return len(docs) - len(failed)


def unwritten(since: str | None) -> str:
    """The documents still needing titleSuggest.

    Built from a fixed shape and a date this script's own caller typed, never from
    anything a reader could reach.
    """
    flt = f"(suggestVersion eq null or suggestVersion lt {VERSION})"
    if since:
        flt += f" and publishedAt ge {since}T00:00:00Z"
    return flt


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--apply", action="store_true",
                        help="write the documents; without it nothing is changed")
    parser.add_argument("--since", metavar="YYYY-MM-DD",
                        help="only articles published on or after this date, which is the "
                             "lever on how much index storage the suggester takes")
    parser.add_argument("--limit", type=int, default=0, metavar="N",
                        help="stop after about N documents, for a cautious first pass")
    args = parser.parse_args()

    endpoint = os.environ.get("BLOGME_SEARCH_ENDPOINT")
    key = os.environ.get("BLOGME_SEARCH_API_KEY")
    if not endpoint or not key:
        raise SystemExit("BLOGME_SEARCH_ENDPOINT and BLOGME_SEARCH_API_KEY must be set; "
                         "run infra/backfill-suggest.sh instead")

    if args.since:
        time.strptime(args.since, "%Y-%m-%d")  # Fails loudly rather than filtering oddly.

    client = Search(endpoint, os.environ.get("BLOGME_SEARCH_INDEX", "articles"), key)
    flt = unwritten(args.since)

    before = client.count(flt)
    print(f"documents without titleSuggest: {before:,}")
    if args.since:
        print(f"scoped to articles published since {args.since}")
    if not args.apply:
        for doc_id, title in client.head(flt)[:3]:
            print(f"  would write {doc_id} -> {title[:60]!r}")
        print("\ndry run; nothing was written. Add --apply to write.")
        return

    merged = 0
    stalled = 0
    previous: set[str] = set()
    started = time.monotonic()

    # Until the set is empty, rather than until a count says so. The count is a moment
    # behind the writes, and trusting it would stop a run early on a stale zero.
    while True:
        page = client.head(flt)
        if not page:
            break

        ids = {doc_id for doc_id, _ in page}
        if ids <= previous:
            # Every document here was written by the pass before; the index has not
            # caught up yet. Waiting is the whole fix.
            stalled += 1
            if stalled >= MAX_STALLED:
                raise SystemExit(f"gave up: {len(ids)} documents kept coming back after "
                                 f"{MAX_STALLED} passes; writes are not taking effect")
            time.sleep(SETTLE_SECONDS)
            continue
        stalled = 0
        previous = ids

        for start in range(0, len(page), BATCH):
            docs = [{"@search.action": "merge", "id": doc_id,
                     "titleSuggest": title, "suggestVersion": VERSION}
                    for doc_id, title in page[start:start + BATCH]]
            merged += client.merge(docs)

        print(f"  {merged:,} written, {(time.monotonic() - started) / 60:.1f} min elapsed",
              flush=True)

        if args.limit and merged >= args.limit:
            print(f"\nstopped at the --limit of {args.limit:,}.")
            break

    # Counted rather than tallied, because the two are not the same number: the head of
    # the set still holds the batch just sent until indexing catches up, so a pass can
    # write a document the pass before it already wrote. Merges are idempotent, so that
    # costs a little work and nothing else — but it makes the tally an overstatement,
    # and the only honest figure is how far the set actually fell.
    after = client.count(flt)
    print(f"\ndone in {(time.monotonic() - started) / 60:.1f} min: "
          f"{before - after:,} documents backfilled, {after:,} still to do")
    if after:
        print("run again to continue; the set only holds what has not been written.")


if __name__ == "__main__":
    main()
