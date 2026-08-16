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
