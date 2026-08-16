#!/usr/bin/env python3
"""Build sources/blogs.yml from the GitHub blog lists in source_lists.txt.

The pipeline, one step per stage:

  1. read the seed repository/topic URLs
  2. pull every link out of their list files
  3. collapse each link to the blog it belongs to
  4. check each blog for reachability, a name and a feed
  5. write blogs.yml, plus link-audit.csv covering every link that was checked

Reachability decides membership. A feed is recorded when the blog has one, but is
not required unless --require-feed is passed.

Usage:
    pip install -r requirements.txt
    GITHUB_TOKEN=... python build_sources.py
"""

from __future__ import annotations

import argparse
import asyncio
import concurrent.futures
import os
import sys
import time
from collections import defaultdict
from pathlib import Path

import httpx

from extractor.checks import check_candidate_limited
from extractor.models import Candidate
from extractor.output import build_entries, validate_entries, write_audit_csv, write_sources_yaml
from extractor.progress import Progress, log
from extractor.sourcelists import (
    GitHubClient,
    merge_into,
    parse_github_repo,
    parse_github_topic,
    scan_repo,
)

TOOLS_DIR = Path(__file__).resolve().parent
SOURCES_DIR = TOOLS_DIR.parent

DEFAULT_INPUT = TOOLS_DIR / "source_lists.txt"
DEFAULT_OUTPUT = SOURCES_DIR / "blogs.yml"
DEFAULT_AUDIT = TOOLS_DIR / "link-audit.csv"

USER_AGENT = "blog-source-extractor"


def read_seeds(path: Path) -> list[str]:
    return [
        line.strip().replace("\\:", ":")
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.strip().startswith("#")
    ]


async def resolve_repos(gh: GitHubClient, seeds: list[str], topic_repo_limit: int) -> dict[tuple[str, str], set[str]]:
    """Expand seeds to repositories, remembering which seeds each one came from."""
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
        else:
            log(f"warning: unsupported seed URL skipped: {seed}")

    by_repo: dict[tuple[str, str], set[str]] = defaultdict(set)
    for owner, repo_name, seed in repos:
        by_repo[(owner, repo_name)].add(seed)
    return by_repo


async def collect_candidates(gh: GitHubClient, args: argparse.Namespace) -> dict[str, Candidate]:
    """Every link found across every seed repository, keyed by blog."""
    by_repo = await resolve_repos(gh, read_seeds(args.input), args.topic_repo_limit)
    limit = asyncio.Semaphore(args.repo_concurrency)

    async def run(owner: str, repo: str, seeds: set[str]) -> dict[str, Candidate]:
        async with limit:
            try:
                return await scan_repo(gh, owner, repo, seeds, args.max_file_size)
            except Exception as exc:
                log(f"warning: failed repo {owner}/{repo}: {exc}")
                return {}

    results = await asyncio.gather(
        *(run(owner, repo, seeds) for (owner, repo), seeds in sorted(by_repo.items()))
    )

    all_candidates: dict[str, Candidate] = {}
    for found in results:
        for candidate in found.values():
            merge_into(all_candidates, candidate)
    return all_candidates


async def run(args: argparse.Namespace) -> int:
    # Host lookups run in the loop's default executor; the stock 32 threads throttle everything.
    asyncio.get_running_loop().set_default_executor(
        concurrent.futures.ThreadPoolExecutor(max_workers=max(64, args.concurrency))
    )

    client_options = {
        "headers": {"User-Agent": USER_AGENT},
        "timeout": httpx.Timeout(30.0, connect=10.0),
        "limits": httpx.Limits(max_connections=args.concurrency * 8, max_keepalive_connections=256),
    }

    async with httpx.AsyncClient(**client_options) as client:
        gh = GitHubClient(client, os.environ.get("GITHUB_TOKEN"))

        all_candidates = await collect_candidates(gh, args)
        log(f"candidate sites before validation: {len(all_candidates)}")

        items = list(all_candidates.items())
        if args.limit_candidates:
            items = items[:args.limit_candidates]
        checked_keys = {key for key, _ in items}

        progress = Progress(len(items))
        limit = asyncio.Semaphore(args.concurrency)
        results = await asyncio.gather(
            *(
                check_candidate_limited(client, candidate, args.require_feed, limit, progress)
                for _, candidate in items
            )
        )

    entries = build_entries([c for c in results if c is not None])
    validate_entries(entries)
    write_sources_yaml(args.output, entries)

    if args.audit_output:
        write_audit_csv(args.audit_output, all_candidates, checked_keys)

    with_feed = sum(1 for entry in entries if entry.get("feed"))
    log(f"links checked: {len(items)}")
    log(f"sources written: {len(entries)} ({with_feed} with a validated feed)")
    log(f"output: {args.output}")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Build the blog source list from GitHub blog lists.")
    parser.add_argument("--input", type=Path, default=DEFAULT_INPUT, help="Seed GitHub repo/topic URLs.")
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT, help="YAML source list to write.")
    parser.add_argument("--audit-output", type=Path, default=DEFAULT_AUDIT, help="CSV of every checked link. Empty string disables it.")
    parser.add_argument("--concurrency", type=int, default=200, help="Concurrent site checks. Much above 200 starts losing sites to timeouts.")
    parser.add_argument("--repo-concurrency", type=int, default=6, help="Concurrent GitHub repositories to scan.")
    parser.add_argument("--max-file-size", type=int, default=2_000_000, help="Largest repository file to read.")
    parser.add_argument("--topic-repo-limit", type=int, default=10, help="Repositories to take per GitHub topic seed.")
    parser.add_argument("--limit-candidates", type=int, default=0, help="Check at most N links. 0 means no limit.")
    parser.add_argument("--require-feed", action="store_true", help="Keep only sources with a working RSS/Atom feed.")

    args = parser.parse_args(argv)
    if str(args.audit_output) in ("", "."):
        args.audit_output = None
    return args


def main() -> int:
    args = parse_args(sys.argv[1:])
    started = time.time()
    try:
        return asyncio.run(run(args))
    finally:
        log(f"elapsed: {time.time() - started:.1f}s")


if __name__ == "__main__":
    raise SystemExit(main())
