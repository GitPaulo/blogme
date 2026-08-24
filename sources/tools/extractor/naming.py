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
    # The same words in the other spellings and languages the source lists run into.
    "home page", "startseite", "accueil", "首页",
}

MAX_NAME_LENGTH = 80

# List generators write a bracketed placeholder where a title was missing, e.g.
# "[No title found]". Left alone it becomes the blog's name.
PLACEHOLDER_RE = re.compile(r"^\[.*\]$")

# Punctuation at either end of a title, which differs between renderings of the same
# interstitial: "One moment, please..." and "One moment please" are one title.
TITLE_EDGE_PUNCTUATION_RE = re.compile(r"^[\s\W_]+|[\s\W_]+$")


def fold_title(title: str) -> str:
    """Lowercase and drop surrounding punctuation, so that "One moment, please...",
    "one moment please" and "One Moment, Please" all read as the same title."""
    collapsed = re.sub(r"\s+", " ", title).strip().lower()
    return TITLE_EDGE_PUNCTUATION_RE.sub("", collapsed)


# A bot check, a WAF block or a redirect stub answers with a page whose <title>
# describes the wait rather than the blog, and that title is then read as the blog's
# name: one build named 1,181 sources "One moment, please...".
#
# Matched whole rather than by keyword, because the vocabulary is ordinary: "Security
# Checklist", "One Moment in Time" and "Just a Moment with Sarah" all read as notices
# to a keyword rule and are all plausible blogs.
INTERSTITIAL_TITLES = {fold_title(t) for t in (
    "Just a moment",                # Cloudflare
    "Attention Required",           # Cloudflare, paired with "| Cloudflare"
    "Checking your browser",        # Cloudflare
    "DDoS protection by Cloudflare",
    "Please wait",
    "Client Challenge",             # Imperva / Incapsula
    "Radware Captcha Page",
    "Bot Check",
    "Bot Verification",
    "Security check",
    "Site verification",
    "Human verification",
    "Prove you're a human",
    "Are you a robot?",
    "Welcome! But are you bot(s)?",
    "Sign in ・ Cloudflare Access",
    "Access Denied",                # Akamai's wording for the same refusal
    "403 Forbidden",
    "Forbidden",                    # what clean_name leaves of "403 Forbidden"
    "Loading",                      # a shell that never rendered the blog
    "Redirect",
    "Redirecting",
    "You are being redirected",
)}

# The few that run the host or the wait into the same line, with nothing marking where
# the notice ends. Kept long and specific: a short prefix like "one moment" or
# "security check" would take "One Moment in Time" and "Security Checklist" with it.
INTERSTITIAL_PREFIXES = (
    "one moment, please",           # Sucuri, Bad Behavior and several shared hosts
    "one moment please",
    "einen moment bitte",           # the German wording of the same page
    "checking your browser before",
    "verifying you are human",      # Cloudflare, followed by "...a few seconds"
    "redirecting to",
)

# A challenge page often pairs its notice with the product behind it: "Attention
# Required! | Cloudflare". Only these separators mark that pairing — ":" and "-" are
# deliberately absent, because a real name uses them to introduce a subtitle, and
# splitting on them would reduce "Access Denied: a security blog" to a notice.
INTERSTITIAL_SEPARATOR_RE = re.compile(r"\s*[|–—·•]\s*")


def is_interstitial(title: str) -> bool:
    """True when a title belongs to the wait rather than to the blog behind it."""
    folded = fold_title(html.unescape(title))
    if folded in INTERSTITIAL_TITLES or folded.startswith(INTERSTITIAL_PREFIXES):
        return True
    # "Attention Required! | Cloudflare" -> is the half before the product a notice?
    return fold_title(INTERSTITIAL_SEPARATOR_RE.split(folded, 1)[0]) in INTERSTITIAL_TITLES


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
    if is_interstitial(name):
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
        # Tested whole and before the split, because a challenge page pairs its notice
        # with the product blocking the request — "Attention Required! | Cloudflare" —
        # and the split would keep the half that reads like a name.
        if not is_interstitial(raw):
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
