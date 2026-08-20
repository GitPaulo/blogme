"""Link cleaning and canonicalisation.

Three jobs:

  clean_url       drop links that can never be a blog and strip tracking noise
  canonical_site  collapse a link to the blog it belongs to, so a list containing
                  fifty posts from one site produces one source entry
  site_key        decide whether two site URLs name the same blog, which is what
                  lets a rebuild recognise a source it already has
"""

from __future__ import annotations

import html
import re
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urljoin, urlparse, urlunparse

EXCLUDED_HOSTS = {
    # Code hosting and package registries
    "github.com", "www.github.com", "raw.githubusercontent.com", "gist.github.com",
    "docs.github.com", "api.github.com", "gitlab.com", "www.gitlab.com",
    "bitbucket.org", "sourceforge.net", "npmjs.com", "www.npmjs.com", "pypi.org",
    "rubygems.org", "crates.io", "packagist.org", "hub.docker.com",
    "marketplace.visualstudio.com",
    # Social, chat and aggregators
    "twitter.com", "x.com", "facebook.com", "www.facebook.com", "instagram.com",
    "www.instagram.com", "linkedin.com", "www.linkedin.com", "youtube.com",
    "www.youtube.com", "youtu.be", "reddit.com", "www.reddit.com", "bsky.app",
    "mastodon.social", "news.ycombinator.com", "lobste.rs", "t.me", "telegram.me",
    "discord.gg", "discord.com", "slack.com", "threads.net", "pinterest.com",
    # Funding and badges
    "patreon.com", "www.patreon.com", "ko-fi.com", "buymeacoffee.com",
    "www.buymeacoffee.com", "opencollective.com", "paypal.me", "shields.io",
    "img.shields.io", "badgen.net", "badge.fury.io", "forthebadge.com",
    "travis-ci.org", "travis-ci.com", "circleci.com", "codecov.io", "coveralls.io",
    # Reference, licences, shorteners and generic services
    "wikipedia.org", "stackoverflow.com", "stackexchange.com", "creativecommons.org",
    "opensource.org", "choosealicense.com", "unlicense.org", "archive.org",
    "web.archive.org", "archive.ph", "goo.gl", "bit.ly", "t.co", "tinyurl.com",
    "amazon.com", "www.amazon.com", "play.google.com", "apps.apple.com",
    "docs.google.com", "drive.google.com", "forms.gle", "translate.google.com",
    "example.com", "www.example.com", "localhost",
}

EXCLUDED_HOST_SUFFIXES = (
    ".githubusercontent.com",
    ".wikipedia.org",
    ".stackexchange.com",
    ".slack.com",
    ".zoom.us",
    ".local",
)

# Subdomains that serve assets or account flows rather than writing.
EXCLUDED_HOST_PREFIXES = (
    "cdn.", "static.", "assets.", "img.", "images.", "media.", "status.",
    "shop.", "store.", "checkout.", "accounts.", "account.", "login.", "auth.",
)

RESERVED_TLD_SUFFIXES = (".test", ".invalid", ".internal", ".localhost", ".example")

# Hosts where the first path segment identifies a distinct blog.
MULTI_TENANT_HOSTS = {
    "medium.com", "dev.to", "hashnode.com", "substack.com", "hackernoon.com",
    "qiita.com", "zenn.dev", "note.com", "write.as", "telegra.ph", "micro.blog",
    "blog.csdn.net", "people.kernel.org", "lwn.net",
}

MULTI_TENANT_HOST_SUFFIXES = (
    ".github.io",
    ".gitlab.io",
    ".netlify.app",
    ".vercel.app",
    ".pages.dev",
    ".sourceforge.io",
)

# First path segments worth keeping on ordinary hosts, e.g. https://vendor.com/blog/.
BLOG_PATH_SEGMENTS = {
    "blog", "blogs", "posts", "writing", "writings", "articles", "essays", "notes",
    "journal", "weblog", "news", "engineering", "engineering-blog", "tech",
    "techblog", "tech-blog", "insights", "thoughts", "devblog", "dev-blog",
}

# Path segments that never identify a blog on their own.
PATH_NOISE_SEGMENTS = {
    "post", "p", "article", "entry", "archive", "archives", "tag", "tags",
    "category", "categories", "page", "pages", "about", "search", "feed", "rss",
    "atom", "index", "en", "login", "signup", "assets", "static", "images",
}

NON_PAGE_EXTENSIONS = {
    ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".ico",
    ".pdf", ".zip", ".gz", ".tgz", ".tar", ".7z",
    ".mp3", ".mp4", ".mov", ".avi", ".webm",
    ".css", ".js", ".map", ".woff", ".woff2", ".ttf",
    ".exe", ".dmg", ".pkg", ".deb", ".rpm",
}

TRACKING_QUERY_PREFIXES = ("utm_",)
TRACKING_QUERY_KEYS = {"ref", "source", "fbclid", "gclid", "mc_cid", "mc_eid"}

# Tried in order when a site does not advertise its feed.
COMMON_FEED_PATHS = (
    "feed",
    "feed.xml",
    "rss",
    "rss.xml",
    "atom.xml",
    "index.xml",
    "feeds/posts/default?alt=rss",  # Blogger
)

IP_HOST_RE = re.compile(r"[0-9.]+")


def clean_url(raw: str) -> str | None:
    """Normalise a discovered URL, or return None if it cannot be a blog."""
    raw = html.unescape(raw.strip())
    raw = raw.rstrip(".,;:!?'\">)]}")
    raw = raw.replace("\\:", ":")
    if not raw.startswith(("http://", "https://")):
        return None

    try:
        parsed = urlparse(raw)
    except Exception:
        return None

    if parsed.scheme not in {"http", "https"}:
        return None

    host = (parsed.hostname or "").lower().strip(".")
    if not host or "." not in host:
        return None

    if host in EXCLUDED_HOSTS or host.removeprefix("www.") in EXCLUDED_HOSTS:
        return None

    if host.endswith(EXCLUDED_HOST_SUFFIXES) or host.startswith(EXCLUDED_HOST_PREFIXES):
        return None

    if IP_HOST_RE.fullmatch(host) or host.endswith(RESERVED_TLD_SUFFIXES):
        return None

    if Path(parsed.path or "/").suffix.lower() in NON_PAGE_EXTENSIONS:
        return None

    query_pairs = [
        (key, value)
        for key, value in parse_qsl(parsed.query, keep_blank_values=True)
        if key.lower() not in TRACKING_QUERY_KEYS
        and not key.lower().startswith(TRACKING_QUERY_PREFIXES)
    ]

    return urlunparse(
        parsed._replace(
            scheme="https" if parsed.scheme == "http" and host.endswith(".github.io") else parsed.scheme,
            netloc=parsed.netloc.lower(),
            fragment="",
            query=urlencode(query_pairs, doseq=True),
        )
    )


def canonical_site(site: str) -> str | None:
    """Collapse a link to the blog it belongs to: an article URL becomes its blog root."""
    parsed = urlparse(site)
    host = (parsed.hostname or "").lower().strip(".")
    if not host:
        return None

    segments = [s for s in (parsed.path or "").split("/") if s]
    keep: str | None = None

    if segments:
        first = segments[0]
        low = first.lower()
        multi_tenant = host in MULTI_TENANT_HOSTS or host.endswith(MULTI_TENANT_HOST_SUFFIXES)
        if low.startswith(("@", "~")):
            keep = first
        elif low in BLOG_PATH_SEGMENTS:
            keep = first
        elif multi_tenant and low not in PATH_NOISE_SEGMENTS and "." not in low and not low.isdigit():
            keep = first

    path = f"/{keep}/" if keep else "/"
    return urlunparse((parsed.scheme, parsed.netloc.lower(), path, "", "", ""))


def dedupe_urls(urls: list[str], skip: set[str] | None = None) -> list[str]:
    """Clean and de-duplicate URLs, preserving order."""
    seen = set(skip or ())
    out = []
    for url in urls:
        cleaned = clean_url(url)
        if cleaned and cleaned not in seen:
            seen.add(cleaned)
            out.append(cleaned)
    return out


def feed_guesses_for_site(site: str) -> list[str]:
    """Common feed locations to try, relative to the site and then to its root."""
    parsed = urlparse(site)
    root = f"{parsed.scheme}://{parsed.netloc}/"
    base = site if site.endswith("/") else site + "/"

    guesses = [urljoin(base, path) for path in COMMON_FEED_PATHS]
    guesses += [urljoin(root, path) for path in COMMON_FEED_PATHS]

    seen: set[str] = set()
    return [g for g in guesses if not (g in seen or seen.add(g))]


def site_key(site: str) -> str:
    """The form two site URLs are matched on when deciding they are the same blog.

    A list writes http://www.example.com where the last build recorded
    https://example.com, because the build followed the redirect and the list never
    did. Scheme, a leading www and a trailing slash are the three ways that happens,
    and none of them makes it a different blog.
    """
    parsed = urlparse(site)
    host = (parsed.hostname or "").lower().removeprefix("www.")
    return f"{host}{(parsed.path or '').rstrip('/')}"


def domain_name(site: str) -> str:
    """Last-resort display name, e.g. https://www.example.com/ -> Example."""
    host = urlparse(site).hostname or site
    host = host.removeprefix("www.")
    labels = host.split(".")
    if len(labels) >= 2:
        return labels[-2].replace("-", " ").title()
    return host.replace("-", " ").title()


def registrable_domain(host: str) -> str:
    """The domain a host belongs to, e.g. links.example.com -> example.com.

    Used to tell a list page's own links from the blogs it lists, since the two often
    sit on different subdomains of one site. On shared hosting one more label is kept,
    because every *.github.io belongs to a different person and collapsing them would
    make a list hosted there discard every blog hosted there too.
    """
    host = host.lower().strip(".").removeprefix("www.")
    labels = host.split(".")
    keep = 3 if host.endswith(MULTI_TENANT_HOST_SUFFIXES) else 2
    return ".".join(labels[-keep:]) if len(labels) >= keep else host
