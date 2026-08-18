"""Tagging.

Subject tags come from tags.yml, which is the only file to edit when the vocabulary
changes. A tag is kept when it scores at least MIN_SCORE:

    2 points  the blog files posts under that word (a feed category)
    1 point   each distinct keyword found in the blog's own text

Blogs whose own words say nothing useful fall back to what their source list implies.
Provenance tags (personal blog, company blog) are kept either way, since a page cannot
tell you those.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from typing import Iterable

import yaml

VOCABULARY_PATH = Path(__file__).resolve().parent.parent / "tags.yml"

MAX_TAGS = 6
MIN_SCORE = 2
CATEGORY_SCORE = 2

# What kind of blog it is, which its own pages rarely say.
PROVENANCE_TAGS = ["personal-blogs",
                   "company-blogs", "independent-web", "small-web"]

# Used only when a blog's own words match nothing in the vocabulary.
FALLBACK_TAGS = ["tech"]


@dataclass(frozen=True)
class Topic:
    keywords: re.Pattern[str]
    aliases: frozenset[str]


@lru_cache(maxsize=1)
def vocabulary() -> dict[str, Topic]:
    raw = yaml.safe_load(VOCABULARY_PATH.read_text(encoding="utf-8")) or {}
    topics: dict[str, Topic] = {}
    for tag, keywords in raw.items():
        pattern = re.compile(
            r"\b(?:" + "|".join(re.escape(k.lower())
                                for k in keywords) + r")\b",
            re.IGNORECASE,
        )
        topics[tag] = Topic(
            keywords=pattern,
            aliases=frozenset({tagify(k) for k in keywords} | {tag}),
        )
    return topics


@lru_cache(maxsize=1)
def tag_order() -> list[str]:
    order = list(vocabulary())
    order += [tag for tag in PROVENANCE_TAGS +
              FALLBACK_TAGS if tag not in order]
    return order


def tagify(value: str) -> str:
    value = re.sub(r"[^a-z0-9]+", "-", value.strip().lower())
    return re.sub(r"-+", "-", value).strip("-")


def ordered_tags(tags: set[str]) -> list[str]:
    """Vocabulary order first, anything unrecognised after, capped at MAX_TAGS."""
    tags = {tagify(t) for t in tags if tagify(t)}
    known = [t for t in tag_order() if t in tags]
    rest = sorted(tags.difference(known))
    return (known + rest)[:MAX_TAGS]


def split_provenance(tags: set[str]) -> tuple[list[str], set[str]]:
    """Separate what kind of blog this is from what it writes about.

    The two answer different questions and belong in different fields: every blog
    from a personal-blog list is a personal blog, so as a subject tag it says
    nothing, while as a kind it is the whole point.
    """
    tags = {tagify(t) for t in tags if tagify(t)}
    kind = [t for t in PROVENANCE_TAGS if t in tags]
    return kind, tags.difference(PROVENANCE_TAGS)


def tags_from_content(text: str, categories: Iterable[str] = ()) -> set[str]:
    """Subject tags a blog earns from its own words and the categories it files posts under."""
    declared = {tagify(c) for c in categories if tagify(c)}
    scores: dict[str, int] = {}

    for tag, topic in vocabulary().items():
        score = len({match.lower() for match in topic.keywords.findall(text)})
        if declared & topic.aliases:
            score += CATEGORY_SCORE
        if score >= MIN_SCORE:
            scores[tag] = score

    order = tag_order()
    ranked = sorted(scores, key=lambda tag: (-scores[tag], order.index(tag)))
    return set(ranked[:MAX_TAGS])


def _tokens(seed: str) -> set[str]:
    # Whole tokens only: substring matching would turn "abdelhai" into an AI blog.
    return {t for t in re.split(r"[^a-z0-9]+", seed.lower()) if t}


def provenance_tags_for_seed(seed: str) -> set[str]:
    """What kind of blog the list says this is.

    Matched against the seed URL, so a list whose name does not say what it collects
    is named here instead: blogscroll admits only personal sites on their own domain,
    which its URL alone does not tell you.
    """
    tokens = _tokens(seed)
    tags: set[str] = set()

    if tokens & {"personal", "smallweb", "indieweb", "blogscroll"}:
        tags.add("personal-blogs")

    if tokens & {"company", "companies"}:
        tags.add("company-blogs")

    if tokens & {"independent", "smallweb", "indieweb", "blogscroll"}:
        tags.update({"independent-web", "small-web"})

    return tags


def fallback_tags_for_seed(seed: str) -> set[str]:
    """A coarse subject for blogs that describe themselves too thinly to tag."""
    tokens = _tokens(seed)
    tags: set[str] = set()

    if tokens & {"engineering", "software", "programming", "developer", "dev", "devblogs"}:
        tags.add("software-engineering")

    if tokens & {"ml", "ai", "llm", "llms", "deeplearning"}:
        tags.update({"ai", "machine-learning"})

    if tokens & {"frontend", "webdev"}:
        tags.add("web-development")

    if "security" in tokens:
        tags.add("security")

    if "python" in tokens:
        tags.add("python")

    if "data" in tokens and "science" in tokens:
        tags.add("data-science")

    if tokens & {"astronomy", "astro"}:
        tags.add("astronomy")

    if "physics" in tokens:
        tags.add("physics")

    if "science" in tokens:
        tags.add("science")

    return tags or {"tech"}
