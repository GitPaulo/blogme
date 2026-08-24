#!/usr/bin/env python3
"""Repairs the author of documents indexed under a bot-check page title.

The source extractor used to read a challenge page's <title> as the blog's name, and
crawl.go and sitemap.go write that name as the article's author whenever the feed
carries none. Fixing the extractor stops it recurring, but it does not reach the
documents already indexed: toArticle calls skipStored before it builds an article, so
a re-crawl skips every post the store already holds and never rewrites the author.

This patches those documents in place. It sends a "merge" action carrying only id and
author, never the "mergeOrUpload" that Index.Upsert uses: that one writes the whole
document, and url, title and sourceId are not omitempty, so a partial upload would
blank them.

Dry run by default. Corrected names come from sources/blogs.yml, so run it from a
checkout that already carries the extractor fix. Before writing, every document's
current author is recorded to --rollback.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from collections import Counter
from pathlib import Path

import yaml

API_VERSION = "2024-07-01"
PAGE = 1000           # Azure caps $top at 1000,
BATCH = 1000          # and an indexing batch at 1000 documents.
SKIP_LIMIT = 100_000  # $skip cannot go past this, so more than that needs splitting.

# The names the extractor wrote before it learned to refuse a challenge page. Listed
# rather than derived, so the script states exactly which documents it will touch.
BAD_AUTHORS = [
    ": Forbidden",
    "Accueil",
    "Bot Check",
    "Bot Verification",
    "Checking your browser",
    "Client Challenge",
    "Einen Moment bitte, die Ausgabe wird geladen...",
    "Home Page",
    "Home page",
    "Loading...",
    "One moment, please...",
    "Prove you're a human",
    "Radware Captcha Page",
    "Redirect",
    "Redirecting",
    "Redirecting to dentro.de/ai",
    "Redirecting to jqlang.github.io",
    "Redirecting to new ArviZ documentation host: ReadTheDocs",
    "Redirecting to zhenjia.org...",
    "Redirecting to: /2025",
    "Redirecting to: /en/blog",
    "Redirecting to: /rob/about/",
    "Redirecting...",
    "Redirecting…",
    "Sign in ・ Cloudflare Access",
    "Site verification",
    "Startseite",
    "Welcome! But are you bot(s)?",
    "You are being redirected...",
    "redirecting to sympa",
    "首页",
]


def odata_quote(value: str) -> str:
    """A string literal for an OData filter. A single quote is escaped by doubling it,
    which is what "Prove you're a human" needs."""
    return "'" + value.replace("'", "''") + "'"


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
        body = {"search": "*", "filter": flt, "top": 0, "count": True}
        return self._call("POST", "/docs/search", body)["@odata.count"]

    def rows(self, flt: str, total: int) -> list[tuple[str, str, str]]:
        """Every (id, sourceId, author) matching the filter, collected before anything
        is written: paging by $skip is only stable while the result set is not moving,
        and the author it replaces is what makes the write reversible."""
        found: list[tuple[str, str, str]] = []
        for skip in range(0, min(total, SKIP_LIMIT), PAGE):
            body = {"search": "*", "filter": flt, "select": "id,sourceId,author",
                    "top": PAGE, "skip": skip}
            page = self._call("POST", "/docs/search", body).get("value", [])
            if not page:
                break
            found.extend((d["id"], d.get("sourceId", ""), d.get("author") or "")
                         for d in page)
        return found

    def merge(self, docs: list[dict]) -> None:
        for start in range(0, len(docs), BATCH):
            chunk = docs[start:start + BATCH]
            result = self._call("POST", "/docs/index", {"value": chunk})
            failed = [r for r in result.get("value", []) if not r.get("status")]
            if failed:
                raise SystemExit(f"{len(failed)} documents rejected, first: {failed[0]}")
            print(f"  {min(start + BATCH, len(docs))}/{len(docs)}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--apply", action="store_true",
                    help="write the corrected authors; without it nothing is sent")
    ap.add_argument("--sources", default=Path("sources/blogs.yml"), type=Path)
    ap.add_argument("--rollback", default=Path("author-repair-rollback.json"), type=Path,
                    help="where each document's current author is recorded before writing")
    args = ap.parse_args()

    endpoint = os.environ.get("BLOGME_SEARCH_ENDPOINT", "")
    key = os.environ.get("BLOGME_SEARCH_API_KEY", "")
    index = os.environ.get("BLOGME_SEARCH_INDEX", "articles")
    if not endpoint or not key:
        print("BLOGME_SEARCH_ENDPOINT and BLOGME_SEARCH_API_KEY must be set")
        return 2

    names = {e["id"]: e["name"]
             for e in yaml.safe_load(args.sources.read_text(encoding="utf-8"))["sources"]}
    print(f"index    {index}")
    print(f"sources  {len(names)} from {args.sources}")
    print(f"mode     {'APPLY - documents will be written' if args.apply else 'dry run'}")
    print()

    client = Search(endpoint, index, key)
    docs: list[dict] = []
    rollback: list[dict] = []
    unknown: Counter[str] = Counter()
    per_author: list[tuple[str, int]] = []
    # Source ids are themselves hyphenated, so an article id cannot be split back into
    # one; the sources touched are counted as they are read instead.
    touched: set[str] = set()

    for author in BAD_AUTHORS:
        flt = f"author eq {odata_quote(author)}"
        total = client.count(flt)
        per_author.append((author, total))
        if not total:
            continue
        if total > SKIP_LIMIT:
            print(f"{author!r} has {total} documents, past the $skip limit")
            return 1
        for doc_id, source_id, was in client.rows(flt, total):
            corrected = names.get(source_id)
            if not corrected:
                # The source has left the list, so there is no name to fall back to.
                # Cleared rather than guessed: the web UI omits an empty author and
                # its separator, and no attribution beats a wrong one.
                unknown[source_id] += 1
                corrected = None
            docs.append({"@search.action": "merge", "id": doc_id, "author": corrected})
            rollback.append({"id": doc_id, "author": was})
            touched.add(source_id)

    for author, total in sorted(per_author, key=lambda x: -x[1]):
        if total:
            print(f"  {total:>6}  {author!r}")

    named = sum(1 for d in docs if d["author"] is not None)
    print()
    print(f"documents found:      {sum(t for _, t in per_author)}")
    print(f"documents to rename:  {named}")
    print(f"documents to clear:   {len(docs) - named}")
    print(f"distinct sources:     {len(touched)}")
    if unknown:
        print(f"cleared, source no longer listed: {sum(unknown.values())} "
              f"across {len(unknown)} sources, e.g. {list(unknown)[:3]}")

    if docs:
        print()
        print("sample:")
        for d in docs[:5]:
            print(f"  {d['id'][:48]:<48} -> {d['author']!r}")

    if not args.apply:
        print()
        print("Dry run. Re-run with --apply to write these.")
        return 0
    if not docs:
        print()
        print("Nothing to write.")
        return 0

    args.rollback.write_text(json.dumps(rollback, ensure_ascii=False), encoding="utf-8")
    print()
    print(f"rollback written to {args.rollback} ({len(rollback)} documents)")

    print(f"writing {len(docs)} merges")
    client.merge(docs)

    print("verifying")
    left = sum(client.count(f"author eq {odata_quote(a)}") for a in BAD_AUTHORS)
    print(f"documents still carrying a bad author: {left}")
    return 0 if left == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
