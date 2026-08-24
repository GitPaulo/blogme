#!/usr/bin/env python3
"""Repairs article authors that were copied from a wrong source name.

crawl.go and sitemap.go write the source's name as the article's author whenever the
feed carries none, so a source named wrongly puts that name on every post it has.
Fixing the extractor stops it recurring but does not reach the documents already
indexed: toArticle calls skipStored before it builds an article, so a re-crawl skips
every post the store already holds and never rewrites the author.

Two repairs, one per bug that produced such a name:

  interstitial  the extractor read a bot check's <title> as the blog's name, so posts
                were authored "One moment, please...". Those strings are never a real
                author, so they are matched across the whole index.

  platform      domain_name took the second label from the right, which on a platform
                host is the platform: every *.github.io blog was named "Github". That
                one IS a plausible author, so it is matched only within the sources
                whose own host makes it a wrong answer, never index-wide.

Both send a "merge" action carrying only id and author, never the "mergeOrUpload"
that Index.Upsert uses: that writes the whole document, and url, title and sourceId
are not omitempty, so a partial upload would blank them.

Dry run by default. Corrected names come from sources/blogs.yml, so run from a
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
from urllib.parse import urlparse

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "sources/tools"))

from extractor.urls import (MULTI_TENANT_HOST_SUFFIXES,  # noqa: E402
                            MULTI_TENANT_HOSTS, REGISTRY_LABELS)

API_VERSION = "2024-07-01"
PAGE = 1000           # Azure caps $top at 1000,
BATCH = 1000          # and an indexing batch at 1000 documents.
FACET_LIMIT = 1000    # distinct sourceId values returned for one stale author.
SKIP_LIMIT = 100_000  # $skip cannot go past this, so more than that needs splitting.

# The names the extractor wrote before it learned to refuse a challenge page. Listed
# rather than derived, so the script states exactly which documents it will touch.
BAD_AUTHORS = [
    ": Forbidden", "Accueil", "Bot Check", "Bot Verification", "Checking your browser",
    "Client Challenge", "Einen Moment bitte, die Ausgabe wird geladen...", "Home Page",
    "Home page", "Loading...", "One moment, please...", "Prove you're a human",
    "Radware Captcha Page", "Redirect", "Redirecting", "Redirecting to dentro.de/ai",
    "Redirecting to jqlang.github.io",
    "Redirecting to new ArviZ documentation host: ReadTheDocs",
    "Redirecting to zhenjia.org...", "Redirecting to: /2025", "Redirecting to: /en/blog",
    "Redirecting to: /rob/about/", "Redirecting...", "Redirecting…",
    "Sign in ・ Cloudflare Access", "Site verification", "Startseite",
    "Welcome! But are you bot(s)?", "You are being redirected...",
    "redirecting to sympa", "首页",
]


def platform_fallback_name(site: str) -> str | None:
    """What domain_name used to return for a platform host, e.g. "Github".

    Only the platform hosts are reproduced, because they are the only ones this
    repair is about. None means the site was never named by that bug.
    """
    parsed = urlparse(site)
    host = (parsed.hostname or site).lower().removeprefix("www.")
    segments = [s for s in (parsed.path or "").split("/") if s]
    tenanted = host.endswith(MULTI_TENANT_HOST_SUFFIXES) or (
        host in MULTI_TENANT_HOSTS and segments)
    if not tenanted:
        return None
    labels = host.split(".")
    if len(labels) >= 3 and labels[-2] in REGISTRY_LABELS and len(labels[-1]) <= 3:
        stale = labels[-3]
    elif len(labels) >= 2:
        stale = labels[-2]
    else:
        stale = host
    return stale.replace("-", " ").title()


def platform_names() -> set[str]:
    """Every name the old fallback could produce from a platform host alone.

    Derived from the same constants rather than listed, so a platform added there is
    covered here too. Used only for documents whose source has left blogs.yml, where
    there is no site left to check: "Qiita" is the platform, never the writer.
    """
    hosts = [f"https://tenant.{s.lstrip('.')}/" for s in MULTI_TENANT_HOST_SUFFIXES]
    hosts += [f"https://{h}/tenant/" for h in MULTI_TENANT_HOSTS]
    return {name for name in map(platform_fallback_name, hosts) if name}


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

    def sources_with(self, flt: str) -> list[str]:
        """Which sources hold documents matching the filter, so a repair can be scoped
        to the ones where the author is wrong rather than applied index-wide."""
        body = {"search": "*", "filter": flt, "top": 0,
                "facets": [f"sourceId,count:{FACET_LIMIT}"]}
        facets = self._call("POST", "/docs/search", body).get("@search.facets", {})
        return [f["value"] for f in facets.get("sourceId", [])]

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


def collect_interstitial(client, names, docs, rollback, touched, unknown):
    """Documents authored by a bot check's page title, matched index-wide."""
    for author in BAD_AUTHORS:
        flt = f"author eq {odata_quote(author)}"
        total = client.count(flt)
        if not total:
            continue
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


def collect_platform(client, entries, names, docs, rollback, touched, unknown):
    """Documents named after their platform rather than their writer.

    "Github" is a name a real blog could carry, so this never matches on the author
    alone. A document qualifies only when its own source sits on a platform host that
    domain_name used to misname, and the source is not called that today.
    """
    stale_for = {}
    for e in entries:
        stale = platform_fallback_name(e["site"])
        if stale and stale != e["name"]:
            stale_for[e["id"]] = stale

    bare_platform = platform_names()

    for stale in sorted(set(stale_for.values()) | bare_platform):
        flt = f"author eq {odata_quote(stale)}"
        if not client.count(flt):
            continue
        for source_id in client.sources_with(flt):
            if source_id in names:
                if stale_for.get(source_id) != stale:
                    continue  # a real author of that name, or a source this never hit
                corrected = names[source_id]
            elif stale in bare_platform:
                # The source has left the list, so its site cannot be checked. The
                # name is the platform's own, never a writer's, so it is cleared
                # rather than kept. Same reasoning as the interstitial repair.
                unknown[source_id] += 1
                corrected = None
            else:
                continue
            scoped = f"sourceId eq {odata_quote(source_id)} and {flt}"
            for doc_id, sid, was in client.rows(scoped, client.count(scoped)):
                docs.append({"@search.action": "merge", "id": doc_id,
                             "author": corrected})
                rollback.append({"id": doc_id, "author": was})
                touched.add(sid)


def merge(client, docs):
    for start in range(0, len(docs), BATCH):
        chunk = docs[start:start + BATCH]
        result = client._call("POST", "/docs/index", {"value": chunk})
        failed = [r for r in result.get("value", []) if not r.get("status")]
        if failed:
            raise SystemExit(f"{len(failed)} documents rejected, first: {failed[0]}")
        print(f"  {min(start + BATCH, len(docs))}/{len(docs)}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--mode", choices=("interstitial", "platform"), required=True,
                    help="which wrong-name bug to repair; see the module docstring")
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

    entries = yaml.safe_load(args.sources.read_text(encoding="utf-8"))["sources"]
    names = {e["id"]: e["name"] for e in entries}
    print(f"index    {index}")
    print(f"mode     {args.mode}")
    print(f"sources  {len(names)} from {args.sources}")
    print(f"writing  {'yes - documents will be changed' if args.apply else 'no, dry run'}")
    print()

    client = Search(endpoint, index, key)
    docs: list[dict] = []
    rollback: list[dict] = []
    unknown: Counter[str] = Counter()
    touched: set[str] = set()

    if args.mode == "interstitial":
        collect_interstitial(client, names, docs, rollback, touched, unknown)
    else:
        collect_platform(client, entries, names, docs, rollback, touched, unknown)

    named = sum(1 for d in docs if d["author"] is not None)
    print(f"documents to rename:  {named}")
    print(f"documents to clear:   {len(docs) - named}")
    print(f"distinct sources:     {len(touched)}")
    if unknown:
        print(f"cleared, source no longer listed: {sum(unknown.values())} "
              f"across {len(unknown)} sources, e.g. {list(unknown)[:3]}")

    if docs:
        print()
        print("sample:")
        for d in docs[:6]:
            print(f"  {d['id'][:46]:<46} -> {d['author']!r}")
        by_new = Counter(d["author"] for d in docs)
        print()
        print("most affected names:")
        for name, n in by_new.most_common(6):
            print(f"  {n:>6}  {name!r}")

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
    merge(client, docs)

    print("verifying")
    if args.mode == "interstitial":
        left = sum(client.count(f"author eq {odata_quote(a)}") for a in BAD_AUTHORS)
    else:
        left = 0
        for e in entries:
            stale = platform_fallback_name(e["site"])
            if stale and stale != e["name"]:
                left += client.count(
                    f"sourceId eq {odata_quote(e['id'])} and author eq {odata_quote(stale)}")
    print(f"documents still carrying the wrong author: {left}")
    return 0 if left == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
