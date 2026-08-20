#!/usr/bin/env python3
"""Build sources/blogs.yml from the blog lists in source_lists.txt.

The pipeline, one step per stage:

  1. read the seed URLs: GitHub repositories, GitHub topics and web pages
  2. pull every link out of their list files
  3. collapse each link to the blog it belongs to
  4. check each blog for reachability, a name and a feed
  5. apply blogs-overrides.yml, the corrections kept by hand
  6. write blogs.yml, plus link-audit.csv covering every link that was checked

Reachability decides membership. A feed is recorded when the blog has one, but is
not required unless --require-feed is passed.

Usage:
    pip install -r requirements.txt
    GITHUB_TOKEN=... python build_sources.py
"""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
import time
from collections import defaultdict
from pathlib import Path

import httpx

from extractor.checks import never_answered
from extractor.models import Candidate
from extractor.output import (
    build_entries,
    committed_sources,
    validate_entries,
    write_audit_csv,
    write_sources_yaml,
)
from extractor.overrides import apply_overrides, load_overrides
from extractor.progress import log
from extractor.sourcelists import (
    GitHubClient,
    merge_into,
    parse_github_repo,
    parse_github_topic,
    scan_page,
    scan_repo,
)
from extractor.workers import check_all, default_processes

TOOLS_DIR = Path(__file__).resolve().parent
SOURCES_DIR = TOOLS_DIR.parent

DEFAULT_INPUT = TOOLS_DIR / "source_lists.txt"
DEFAULT_OUTPUT = SOURCES_DIR / "blogs.yml"
DEFAULT_OVERRIDES = SOURCES_DIR / "blogs-overrides.yml"
DEFAULT_AUDIT = TOOLS_DIR / "link-audit.csv"

USER_AGENT = "blog-source-extractor"


def read_seeds(path: Path) -> list[str]:
    return [
        line.strip().replace("\\:", ":")
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.strip().startswith("#")
    ]


def split_seeds(seeds: list[str]) -> tuple[list[str], list[str]]:
    """Seeds by kind: GitHub repositories and topics, and plain list pages."""
    github: list[str] = []
    pages: list[str] = []

    for seed in seeds:
        if parse_github_repo(seed) or parse_github_topic(seed):
            github.append(seed)
        elif seed.startswith(("http://", "https://")):
            pages.append(seed)
        else:
            log(f"warning: unsupported seed skipped: {seed}")

    return github, pages


async def resolve_repos(gh: GitHubClient, seeds: list[str], topic_repo_limit: int) -> dict[tuple[str, str], set[str]]:
    """Expand GitHub seeds to repositories, remembering which seeds each one came from."""
    repos: list[tuple[str, str, str]] = []

    for seed in seeds:
        repo = parse_github_repo(seed)
        topic = parse_github_topic(seed)
        if repo:
            repos.append((repo[0], repo[1], seed))
        elif topic:
            topic_repos = await gh.topic_repositories(topic, topic_repo_limit)
            log(f"topic:{topic}: discovered {len(topic_repos)} repos")
            repos.extend((owner, name, seed) for owner, name in topic_repos)

    by_repo: dict[tuple[str, str], set[str]] = defaultdict(set)
    for owner, repo_name, seed in repos:
        by_repo[(owner, repo_name)].add(seed)
    return by_repo


async def collect_candidates(gh: GitHubClient, args: argparse.Namespace) -> dict[str, Candidate]:
    """Every link found across every seed, keyed by blog."""
    github_seeds, page_seeds = split_seeds(read_seeds(args.input))
    by_repo = await resolve_repos(gh, github_seeds, args.topic_repo_limit)
    limit = asyncio.Semaphore(args.repo_concurrency)

    async def run_repo(owner: str, repo: str, seeds: set[str]) -> dict[str, Candidate]:
        async with limit:
            try:
                return await scan_repo(gh, owner, repo, seeds, args.max_file_size)
            except Exception as exc:
                log(f"warning: failed repo {owner}/{repo}: {exc}")
                return {}

    async def run_page(url: str) -> dict[str, Candidate]:
        async with limit:
            try:
                found = await scan_page(gh, url)
                log(f"{url}: found {len(found)} linked blogs")
                return found
            except Exception as exc:
                log(f"warning: failed page {url}: {exc}")
                return {}

    results = await asyncio.gather(
        *(run_repo(owner, repo, seeds)
          for (owner, repo), seeds in sorted(by_repo.items())),
        *(run_page(url) for url in page_seeds),
    )

    all_candidates: dict[str, Candidate] = {}
    for found in results:
        for candidate in found.values():
            merge_into(all_candidates, candidate)
    return all_candidates


async def harvest(args: argparse.Namespace) -> dict[str, Candidate]:
    """Every candidate the seed lists between them name."""
    client_options = {
        "headers": {"User-Agent": USER_AGENT},
        "timeout": httpx.Timeout(30.0, connect=10.0),
        "limits": httpx.Limits(max_connections=args.concurrency * 8, max_keepalive_connections=256),
    }

    async with httpx.AsyncClient(**client_options) as client:
        gh = GitHubClient(client, os.environ.get("GITHUB_TOKEN"))
        return await collect_candidates(gh, args)


def run(args: argparse.Namespace) -> int:
    committed = committed_sources(args.output)

    all_candidates = asyncio.run(harvest(args))
    log(f"candidate sites before validation: {len(all_candidates)}")

    items = list(all_candidates.items())
    if args.limit_candidates:
        items = items[:args.limit_candidates]
    checked_keys = {key for key, _ in items}
    keys = [key for key, _ in items]

    # The checks run across processes because they are bound by this interpreter
    # rather than by the network; see extractor/workers.py for the measurements.
    results = check_all(
        [candidate for _, candidate in items],
        require_feed=args.require_feed,
        concurrency=args.concurrency,
        processes=args.processes,
        known_feeds=committed.feeds,
    )
    # A worker fills in its own copy, so the copies are what the audit must report on.
    for key, (candidate, _) in zip(keys, results):
        all_candidates[key] = candidate

    checked = [c for c, ok in results if ok]

    # Sites that never answered get one more chance, slowly. At full concurrency a
    # connect timeout says as much about the run as about the site, and most of the
    # links dropped that way turn out to be live blogs.
    retries = [(key, c) for key, (c, ok) in zip(keys, results)
               if not ok and never_answered(c)]
    if retries:
        recovered = check_all(
            [c for _, c in retries],
            require_feed=args.require_feed,
            concurrency=args.retry_concurrency,
            processes=args.processes,
            known_feeds=committed.feeds,
            slow=True,
            label="retried",
        )
        for (key, _), (candidate, _) in zip(retries, recovered):
            all_candidates[key] = candidate

        checked += [c for c, ok in recovered if ok]
        log(f"recovered {sum(ok for _, ok in recovered)} of {len(retries)}")

    entries = build_entries(checked, committed.ids)

    overrides = load_overrides(args.overrides)
    entries, unmatched = apply_overrides(entries, overrides, committed.ids)
    for site in unmatched:
        log(f"warning: override matched no source and cannot stand alone: {site}")

    validate_entries(entries)
    write_sources_yaml(args.output, entries)

    if args.audit_output:
        write_audit_csv(args.audit_output, all_candidates, checked_keys)

    with_feed = sum(1 for entry in entries if entry.get("feed"))
    log(f"links checked: {len(items)}")
    log(f"sources written: {len(entries)} ({with_feed} with a validated feed)")
    if overrides:
        log(f"overrides applied: {len(overrides) - len(unmatched)} of {len(overrides)}")
    log(f"output: {args.output}")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Build the blog source list from GitHub blog lists.")
    parser.add_argument("--input", type=Path, default=DEFAULT_INPUT,
                        help="Seed GitHub repo/topic URLs.")
    parser.add_argument("--output", type=Path,
                        default=DEFAULT_OUTPUT, help="YAML source list to write.")
    parser.add_argument("--overrides", type=Path, default=DEFAULT_OVERRIDES,
                        help="Hand-maintained corrections merged into the output.")
    parser.add_argument("--audit-output", type=Path, default=DEFAULT_AUDIT,
                        help="CSV of every checked link. Empty string disables it.")
    parser.add_argument("--concurrency", type=int, default=200,
                        help="Site checks in flight across the whole run, not per process.")
    parser.add_argument("--processes", type=int, default=default_processes(),
                        help="Worker processes for the checks. 1 keeps everything in one.")
    parser.add_argument("--retry-concurrency", type=int, default=50,
                        help="Concurrent checks in the retry pass over sites that did not answer.")
    parser.add_argument("--repo-concurrency", type=int, default=6,
                        help="Concurrent GitHub repositories to scan.")
    parser.add_argument("--max-file-size", type=int,
                        default=2_000_000, help="Largest repository file to read.")
    parser.add_argument("--topic-repo-limit", type=int, default=10,
                        help="Repositories to take per GitHub topic seed.")
    parser.add_argument("--limit-candidates", type=int, default=0,
                        help="Check at most N links. 0 means no limit.")
    parser.add_argument("--require-feed", action="store_true",
                        help="Keep only sources with a working RSS/Atom feed.")

    args = parser.parse_args(argv)
    for name in ("audit_output", "overrides"):
        if str(getattr(args, name)) in ("", "."):
            setattr(args, name, None)
    return args


def main() -> int:
    args = parse_args(sys.argv[1:])
    started = time.time()
    try:
        return run(args)
    finally:
        log(f"elapsed: {time.time() - started:.1f}s")


if __name__ == "__main__":
    raise SystemExit(main())
