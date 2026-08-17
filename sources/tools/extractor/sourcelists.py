"""Reading the GitHub blog lists and pulling every link out of them.

A seed is either a repository (github.com/owner/repo) or a topic
(github.com/topics/name), which expands to its most-starred repositories.
"""

from __future__ import annotations

import asyncio
import re
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any, Iterator
from urllib.parse import quote, urlparse

import httpx

from .models import Candidate
from .naming import clean_name
from .progress import log
from .tags import fallback_tags_for_seed, provenance_tags_for_seed
from .urls import NON_PAGE_EXTENSIONS, canonical_site, clean_url

USER_AGENT = "blog-source-extractor"

# Rate limits and transient server errors are worth another try; anything else is an
# answer, including a 404.
RETRY_STATUSES = {403, 429, 500, 502, 503, 504}
MAX_ATTEMPTS = 4

URL_RE = re.compile(r"""https?://[^\s<>"'`\]\)\}]+""", re.IGNORECASE)
MD_LINK_RE = re.compile(r"""\[([^\]]{1,160})\]\((https?://[^\s)]+)\)""", re.IGNORECASE)

LIST_FILE_EXTENSIONS = {
    ".md", ".markdown", ".txt", ".rst", ".csv", ".tsv", ".json", ".jsonl",
    ".yml", ".yaml", ".xml", ".opml", ".html", ".htm",
}

LIST_FILE_BASENAMES = {
    "readme", "dataset", "blogs", "blog", "sources", "source", "feeds",
    "feed", "sites", "urls", "links", "list",
}

# Repository boilerplate that only ever contributes non-blog links.
SKIPPED_BASENAMES = {
    "license", "licence", "copying", "contributing", "code_of_conduct",
    "code-of-conduct", "changelog", "security", "funding", "pull_request_template",
    "issue_template",
}

# Formats where every line is a record, so every link is a curated entry.
STRUCTURED_EXTENSIONS = {
    ".csv", ".tsv", ".json", ".jsonl", ".yml", ".yaml", ".xml", ".opml", ".html", ".htm",
}

# A curated entry: a bullet, a numbered item, a table row, or a line that is just a link.
LIST_ENTRY_RE = re.compile(r"""^\s*(?:[-*+]\s|\d+[.)]\s|\||https?://)""")


def parse_github_repo(url: str) -> tuple[str, str] | None:
    parsed = urlparse(url.replace("\\:", ":"))
    if parsed.hostname != "github.com":
        return None
    parts = [p for p in parsed.path.split("/") if p]
    if len(parts) >= 2 and parts[0] != "topics":
        return parts[0], parts[1]
    return None


def parse_github_topic(url: str) -> str | None:
    parsed = urlparse(url.replace("\\:", ":"))
    if parsed.hostname != "github.com":
        return None
    parts = [p for p in parsed.path.split("/") if p]
    if len(parts) == 2 and parts[0] == "topics":
        return parts[1]
    return None


def is_list_file(path: str, size: int | None, max_size: int) -> bool:
    """True for repository files that might hold blog links."""
    p = Path(path)
    suffix = p.suffix.lower()
    basename = p.stem.lower()

    if size is not None and size > max_size:
        return False

    if any(part.startswith(".git") for part in p.parts):
        return False

    if basename in SKIPPED_BASENAMES or suffix in NON_PAGE_EXTENSIONS:
        return False

    if suffix in LIST_FILE_EXTENSIONS:
        return True

    return basename in LIST_FILE_BASENAMES or p.name.lower() in LIST_FILE_BASENAMES


def candidates_from_opml(text: str) -> list[Candidate]:
    """OPML and similar XML list formats carry site and feed URLs as attributes."""
    out: list[Candidate] = []
    try:
        root = ET.fromstring(text)
    except Exception:
        return out

    for elem in root.iter():
        attrs = {k.lower(): v for k, v in elem.attrib.items()}
        xml_url = attrs.get("xmlurl") or attrs.get("feed") or attrs.get("rss")
        html_url = attrs.get("htmlurl") or attrs.get("url") or attrs.get("site")
        title = attrs.get("title") or attrs.get("text") or attrs.get("name")

        site = clean_url(html_url) if html_url else None
        feed = clean_url(xml_url) if xml_url else None
        canonical = canonical_site(site or feed) if (site or feed) else None
        if canonical:
            out.append(Candidate(site=canonical, name=clean_name(title), feed=feed))
    return out


def entry_lines(text: str, path: str) -> Iterator[str]:
    """The lines of a list file that hold entries, rather than prose about the list.

    Prose mentions a blog in passing; a list entry is a deliberate recommendation, and
    only those are worth checking.
    """
    if Path(path).suffix.lower() in STRUCTURED_EXTENSIONS:
        yield from text.splitlines()
        return

    for line in text.splitlines():
        if LIST_ENTRY_RE.match(line):
            yield line


def candidates_from_file(
    path: str,
    text: str,
    origin: str,
    provenance: set[str],
    fallback: set[str],
) -> dict[str, Candidate]:
    """Every listed blog in one file, keyed by the blog it belongs to."""
    candidates: dict[str, Candidate] = {}

    def add(site: str | None, name: str | None = None, feed: str | None = None) -> None:
        cleaned = clean_url(site) if site else None
        blog = canonical_site(cleaned) if cleaned else None
        if not blog:
            return

        candidate = candidates.setdefault(blog, Candidate(site=blog))
        if name and not candidate.name:
            candidate.name = clean_name(name)
        if feed and not candidate.feed:
            candidate.feed = clean_url(feed)
        candidate.tags.update(provenance)
        candidate.fallback_tags.update(fallback)
        candidate.origins.add(origin)

    for candidate in candidates_from_opml(text):
        add(candidate.site, candidate.name, candidate.feed)

    for line in entry_lines(text, path):
        # Markdown link text is a useful fallback name, so read those before bare URLs.
        for name, url in MD_LINK_RE.findall(line):
            add(url, name)
        for url in URL_RE.findall(line):
            add(url)

    return candidates


def merge_into(target: dict[str, Candidate], candidate: Candidate) -> None:
    existing = target.get(candidate.site)
    if not existing:
        target[candidate.site] = candidate
        return
    if candidate.name and not existing.name:
        existing.name = candidate.name
    if candidate.feed and not existing.feed:
        existing.feed = candidate.feed
    existing.tags.update(candidate.tags)
    existing.fallback_tags.update(candidate.fallback_tags)
    existing.origins.update(candidate.origins)


class GitHubClient:
    """The slice of the GitHub API this tool needs, with retries for rate limits."""

    def __init__(self, client: httpx.AsyncClient, token: str | None) -> None:
        self.client = client
        self.token = token

    @property
    def headers(self) -> dict[str, str]:
        headers = {
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": USER_AGENT,
        }
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    async def _get(self, url: str, headers: dict[str, str]) -> httpx.Response:
        """GET with exponential backoff on the statuses worth retrying."""
        for attempt in range(MAX_ATTEMPTS - 1):
            r = await self.client.get(url, headers=headers, follow_redirects=True)
            if r.status_code not in RETRY_STATUSES:
                r.raise_for_status()
                return r
            await asyncio.sleep(2 ** attempt)

        # Out of retries: whatever the last attempt returns is the answer.
        r = await self.client.get(url, headers=headers, follow_redirects=True)
        r.raise_for_status()
        return r

    async def get_json(self, url: str) -> Any:
        return (await self._get(url, self.headers)).json()

    async def get_text(self, url: str) -> str:
        return (await self._get(url, {"User-Agent": USER_AGENT})).text

    async def default_branch(self, owner: str, repo: str) -> str:
        data = await self.get_json(f"https://api.github.com/repos/{owner}/{repo}")
        return data.get("default_branch") or "main"

    async def repo_tree(self, owner: str, repo: str, branch: str) -> list[dict[str, Any]]:
        data = await self.get_json(
            f"https://api.github.com/repos/{owner}/{repo}/git/trees/{quote(branch)}?recursive=1"
        )
        if data.get("truncated"):
            log(f"warning: tree truncated for {owner}/{repo}; results may be incomplete")
        return data.get("tree", [])

    async def raw_file(self, owner: str, repo: str, branch: str, path: str) -> str:
        quoted_path = "/".join(quote(part) for part in path.split("/"))
        return await self.get_text(
            f"https://raw.githubusercontent.com/{owner}/{repo}/{quote(branch)}/{quoted_path}"
        )

    async def topic_repositories(self, topic: str, limit: int) -> list[tuple[str, str]]:
        repos: list[tuple[str, str]] = []
        page = 1
        while len(repos) < limit:
            per_page = min(100, limit - len(repos))
            url = (
                "https://api.github.com/search/repositories"
                f"?q=topic:{quote(topic)}&sort=stars&order=desc&per_page={per_page}&page={page}"
            )
            items = (await self.get_json(url)).get("items", [])
            if not items:
                break
            for item in items:
                full = item.get("full_name", "")
                if "/" in full:
                    owner, repo = full.split("/", 1)
                    repos.append((owner, repo))
            page += 1
        return repos


async def scan_repo(
    gh: GitHubClient,
    owner: str,
    repo: str,
    seeds: set[str],
    max_file_size: int,
    max_files: int = 400,
    file_concurrency: int = 8,
) -> dict[str, Candidate]:
    """Every blog link found in one repository."""
    provenance = set().union(*(provenance_tags_for_seed(seed) for seed in seeds))
    fallback = set().union(*(fallback_tags_for_seed(seed) for seed in seeds))
    found: dict[str, Candidate] = {}
    branch = await gh.default_branch(owner, repo)
    tree = await gh.repo_tree(owner, repo, branch)

    files = [
        item for item in tree
        if item.get("type") == "blob"
        and is_list_file(item.get("path", ""), item.get("size"), max_file_size)
    ][:max_files]

    log(f"{owner}/{repo}: scanning {len(files)} text/list files")
    limit = asyncio.Semaphore(file_concurrency)

    async def read_one(item: dict[str, Any]) -> tuple[str, str, str | None]:
        path = item["path"]
        origin = f"https://github.com/{owner}/{repo}/blob/{branch}/{path}"
        async with limit:
            try:
                return path, origin, await gh.raw_file(owner, repo, branch, path)
            except Exception as exc:
                log(f"warning: failed to read {origin}: {exc}")
                return path, origin, None

    for path, origin, text in await asyncio.gather(*(read_one(item) for item in files)):
        if not text:
            continue
        for candidate in candidates_from_file(path, text, origin, provenance, fallback).values():
            merge_into(found, candidate)

    return found
