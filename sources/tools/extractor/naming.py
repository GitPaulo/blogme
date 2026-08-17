"""Reading a blog's name and feed links out of an HTML head.

Regexes rather than a parser: only the head matters, and this runs against tens of
thousands of pages.
"""

from __future__ import annotations

import html
import re
from urllib.parse import urljoin

from .models import PageMeta

TITLE_TAG_RE = re.compile(r"<title[^>]*>(.*?)</title>", re.IGNORECASE | re.DOTALL)
META_TAG_RE = re.compile(r"<meta\s[^>]*>", re.IGNORECASE)
LINK_TAG_RE = re.compile(r"<link\s[^>]*>", re.IGNORECASE)
ATTR_RE = re.compile(r"""([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)""")
TITLE_SEPARATOR_RE = re.compile(r"\s+[|–—·•»]\s+|\s+-\s+")

# Titles and link texts that say nothing about which blog this is.
GENERIC_NAMES = {
    "home", "homepage", "index", "welcome", "blog", "my blog", "posts", "articles",
    "link", "here", "website", "site", "rss", "feed", "untitled", "page not found",
    "404", "read more", "click here", "docs", "documentation", "github", "twitter",
    "linkedin", "twitch", "discord", "discord server", "newsletter", "about",
}

MAX_NAME_LENGTH = 80

# List generators write a bracketed placeholder where a title was missing, e.g.
# "[No title found]". Left alone it becomes the blog's name.
PLACEHOLDER_RE = re.compile(r"^\[.*\]$")


def clean_name(raw: str | None) -> str | None:
    """Tidy a candidate name, or return None if it is unusable."""
    if not raw:
        return None
    name = re.sub(r"\s+", " ", html.unescape(raw))
    name = name.strip(" \t\"'*`_|:·–—-")
    name = re.sub(r"^[\*\-•\d\.\)\s]+", "", name).strip()
    if not name or len(name) > MAX_NAME_LENGTH:
        return None
    if name.lower() in GENERIC_NAMES or PLACEHOLDER_RE.match(name):
        return None
    if "http://" in name or "https://" in name or name.startswith("!"):
        return None
    return name


def tag_attributes(tag: str) -> dict[str, str]:
    return {
        key.lower(): html.unescape(value.strip("\"'"))
        for key, value in ATTR_RE.findall(tag)
    }


def page_metadata(body: str) -> PageMeta:
    """Pull the site name, title and description out of a page head."""
    meta = PageMeta()

    for tag in META_TAG_RE.findall(body):
        attrs = tag_attributes(tag)
        key = (attrs.get("property") or attrs.get("name") or "").lower()
        content = attrs.get("content")
        if not content:
            continue
        if not meta.site_name and key in ("og:site_name", "application-name"):
            meta.site_name = clean_name(content)
        elif not meta.description and key in ("description", "og:description"):
            meta.description = re.sub(r"\s+", " ", content).strip()[:400]

    match = TITLE_TAG_RE.search(body)
    if match:
        raw = re.sub(r"\s+", " ", html.unescape(re.sub(r"<[^>]+>", " ", match.group(1)))).strip()
        # "Some Post | Site Name" -> keep the shortest meaningful part.
        parts = [p.strip() for p in TITLE_SEPARATOR_RE.split(raw) if p.strip()]
        parts = [p for p in parts if p.lower() not in GENERIC_NAMES] or parts
        meta.title = clean_name(min(parts, key=len) if parts else raw)

    return meta


def declared_feeds(body: str, page_url: str) -> list[str]:
    """Feed URLs the page advertises through <link rel="alternate">."""
    feeds: list[str] = []
    for tag in LINK_TAG_RE.findall(body):
        attrs = tag_attributes(tag)
        href = attrs.get("href")
        if not href or "alternate" not in attrs.get("rel", "").lower():
            continue
        if any(x in attrs.get("type", "").lower() for x in ("rss", "atom", "feed", "xml")):
            feeds.append(urljoin(page_url, href))
    return feeds
