"""The record that travels through the pipeline."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Candidate:
    """One possible blog: discovered from a list, then filled in by the checks."""

    site: str
    name: str | None = None
    feed: str | None = None
    tags: set[str] = field(default_factory=set)
    fallback_tags: set[str] = field(default_factory=set)
    origins: set[str] = field(default_factory=set)
    status_code: int | None = None
    error: str | None = None

    @property
    def reachable(self) -> bool:
        return self.error is None and self.status_code is not None and self.status_code < 400


@dataclass
class PageMeta:
    """What the HTML head of a homepage tells us."""

    site_name: str | None = None
    title: str | None = None
    description: str | None = None


@dataclass
class FeedInfo:
    """A working feed: its title, the text used to infer tags, and its own categories."""

    title: str | None
    text: str
    categories: list[str] = field(default_factory=list)


@dataclass
class FeedLookup:
    """The outcome of looking for a feed among a set of candidate URLs."""

    url: str | None = None
    info: FeedInfo | None = None
    # Set when a candidate could not be reached at all, so "no feed" is a failure to
    # find out rather than a finding. Only a conclusive lookup is safe to record as a
    # blog having no feed: writing an absence a timeout invented moves the blog onto
    # the sitemap path, and off the crawler entirely when it has no sitemap.
    inconclusive: bool = False


@dataclass(frozen=True)
class Committed:
    """What the last generated list already knew, keyed by site."""

    ids: dict[str, str] = field(default_factory=dict)
    feeds: dict[str, str] = field(default_factory=dict)
