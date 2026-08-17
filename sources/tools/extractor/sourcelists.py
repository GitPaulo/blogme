"""Reading the blog lists and pulling every link out of them.

A seed is a GitHub repository (github.com/owner/repo), a GitHub topic
(github.com/topics/name), which expands to its most-starred repositories, or the URL of
a web page that lists blogs.
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
from .urls import NON_PAGE_EXTENSIONS, canonical_site, clean_url, registrable_domain

USER_AGENT = "blog-source-extractor"

# Rate limits and transient server errors are worth another try; anything else is an
# answer, including a 404.
RETRY_STATUSES = {403, 429, 500, 502, 503, 504}
MAX_ATTEMPTS = 4

URL_RE = re.compile(r"""https?://[^\s<>"'`\]\)\}]+""", re.IGNORECASE)
MD_LINK_RE = re.compile(r"""\[([^\]]{1,160})\]\((https?://[^\s)]+)\)""", re.IGNORECASE)

# An anchor's href together with its link text. On a linked list the text is the blog's
# own name, which a bare URL scrape throws away. The lookbehind keeps "href" from
# matching the tail of another attribute: a data-href would otherwise be read as the
# link, and the real href alongside it never seen.
ANCHOR_RE = re.compile(
    r"""<a\s[^>]*?(?<![-\w])href\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)[^>]*>(.*?)</a\s*>""",
    re.IGNORECASE | re.DOTALL,
)
INNER_TAG_RE = re.compile(r"<[^>]+>")

LIST_FILE_EXTENSIONS = {
    ".md", ".markdown", ".txt", ".rst", ".csv", ".tsv", ".json", ".jsonl",
    ".yml", ".yaml", ".xml", ".opml", ".html", ".htm",
}

HTML_EXTENSIONS = {".html", ".htm"}

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


def candidates_from_html(text: str) -> list[Candidate]:
    """Blogs linked from an HTML page, named by their link text.

    Relative hrefs fall out here because clean_url only accepts absolute URLs, which is
    what we want: on a list page a relative link is navigation, and resolving it against
    the page would emit the list's own site as a blog.

    The link text is only taken as a name when the link pointed at the blog itself. A
    list of posts links to articles, where the text is the article's title and would
    name the blog after whichever post happened to be listed first.
    """
    out: list[Candidate] = []

    for href, label in ANCHOR_RE.findall(text):
        url = clean_url(href.strip("\"'"))
        site = canonical_site(url) if url else None
        if not site:
            continue
        linked_the_blog = url.rstrip("/") == site.rstrip("/")
        name = clean_name(INNER_TAG_RE.sub(" ", label)) if linked_the_blog else None
        out.append(Candidate(site=site, name=name))

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
    own = registrable_domain(urlparse(origin).hostname or "")

    def add(site: str | None, name: str | None = None, feed: str | None = None) -> None:
        cleaned = clean_url(site) if site else None
        blog = canonical_site(cleaned) if cleaned else None
        if not blog:
            return

        # A list links to itself in its navigation and credits; that is not a blog.
        if own and registrable_domain(urlparse(blog).hostname or "") == own:
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

    # Before the bare-URL scan below, which finds the same links without their names.
    if Path(path).suffix.lower() in HTML_EXTENSIONS:
        for candidate in candidates_from_html(text):
            add(candidate.site, candidate.name)

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

    async def get_text(self, url: str, headers: dict[str, str] | None = None) -> str:
        # Defaults to no credentials: this also fetches arbitrary list pages, which must
        # never be sent a GitHub token.
        return (await self._get(url, headers or {"User-Agent": USER_AGENT})).text

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

        # raw.githubusercontent.com ignores the token and rate-limits by IP, which a run
        # of this size trips almost immediately. With a token the API is used instead,
        # since that is where the higher allowance applies. Without one the API would be
        # the stricter of the two, so the raw host stays the better choice.
        if self.token:
            return await self.get_text(
                f"https://api.github.com/repos/{owner}/{repo}/contents/{quoted_path}"
                f"?ref={quote(branch)}",
                {**self.headers, "Accept": "application/vnd.github.raw"},
            )

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


def page_path(url: str) -> str:
    """A filename hint for a fetched page, so format detection still works.

    Plenty of list pages carry no extension. Anything fetched over HTTP that cannot be
    named is treated as HTML, which is what it almost always is.
    """
    name = Path(urlparse(url).path).name
    return name if Path(name).suffix.lower() in LIST_FILE_EXTENSIONS else "page.html"


async def scan_page(gh: GitHubClient, url: str) -> dict[str, Candidate]:
    """Every blog link found on one web page.

    get_text is plain unauthenticated HTTP with retries, so an ordinary list page goes
    through the same fetch path as a repository file.
    """
    text = await gh.get_text(url)
    return candidates_from_file(
        page_path(url),
        text,
        url,
        provenance_tags_for_seed(url),
        fallback_tags_for_seed(url),
    )
