"""Hand-maintained corrections applied on top of the generated source list.

The extractor rebuilds blogs.yml from nothing every run, so anything edited into it
by hand survives only until the next build. That is not hypothetical: a blog added
by hand with a working feed came back from a rebuild without one, which moved it to
the sitemap path — and since it publishes no sitemap, it stopped being crawled at
all while still looking present in the list.

Overrides live in their own file and are re-applied at the end of every build, so a
correction made once stays made.

An override is matched to a generated entry by `site`. Naming a field replaces it;
omitting one leaves whatever the build found. An override carrying enough to stand
on its own (a name and tags) is added when the build did not find that site at all,
which is how a blog the extractor keeps missing can be pinned into the list.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import yaml

from .output import FlowList, source_id

# Every field an override may set, in the order the generated file writes them.
# Anything else is a mistake worth stopping for rather than ignoring.
OVERRIDE_FIELDS = ("id", "name", "site", "feed", "kind", "tags")

# Rendered inline in the YAML, matching the generated entries.
FLOW_FIELDS = ("kind", "tags")

# What an override needs before it can be added as a source in its own right. Below
# this it can only patch something the build already found, so a `site` that matches
# nothing is reported rather than quietly becoming a new and largely empty entry.
STANDALONE_FIELDS = ("name", "tags")


def match_key(site: str) -> str:
    """The form of a site URL two entries are matched on.

    Host case and a trailing slash are the two ways the same blog gets written
    two ways. Matched loosely so an override lands on the entry it meant, because
    the alternative is not a miss but a duplicate: a second source for one blog,
    under a second id, indexing every post twice.
    """
    parts = urlsplit(site)
    return f"{parts.scheme.lower()}://{parts.netloc.lower()}{parts.path.rstrip('/')}"


def load_overrides(path: Path | None) -> list[dict[str, Any]]:
    """Read and check the overrides file. Absent or empty, there is nothing to do."""
    if path is None or not path.exists():
        return []

    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    overrides = data.get("sources") or []

    if not isinstance(overrides, list):
        raise ValueError("overrides: 'sources' must be a list")

    seen: set[str] = set()
    for idx, override in enumerate(overrides):
        if not isinstance(override, dict):
            raise ValueError(f"overrides[{idx}] is not a mapping")

        unknown = sorted(set(override) - set(OVERRIDE_FIELDS))
        if unknown:
            raise ValueError(
                f"overrides[{idx}] has unknown field(s): {', '.join(unknown)}")

        site = override.get("site")
        if not site:
            raise ValueError(f"overrides[{idx}] is missing 'site'")
        # Compared the same way entries are matched, so two spellings of one blog
        # are caught here rather than fighting over the same entry later.
        if match_key(site) in seen:
            raise ValueError(f"duplicate site in overrides: {site}")
        seen.add(match_key(site))

    return overrides


def ordered_entry(entry: dict[str, Any]) -> dict[str, Any]:
    """Rebuild an entry in the field order the generated file uses.

    Patching through dict.update would append a newly set field after the ones
    already there, so a source that gained a feed would write it below its tags and
    read as a different shape from every neighbour.
    """
    out: dict[str, Any] = {}
    for key in OVERRIDE_FIELDS:
        value = entry.get(key)
        if value is None or value == "":
            continue
        out[key] = FlowList(value) if key in FLOW_FIELDS else value
    return out


def apply_overrides(
    entries: list[dict[str, Any]],
    overrides: list[dict[str, Any]],
    known_ids: dict[str, str] | None = None,
) -> tuple[list[dict[str, Any]], list[str]]:
    """Merge overrides into the generated entries.

    Returns the merged list and the sites of any override that could neither patch
    an entry nor stand on its own. Those are reported rather than dropped silently,
    because a mistyped site and a blog that moved both look like nothing happening.
    """
    if not overrides:
        return entries, []

    # Keyed the same way entries are, so an override that spells a site differently
    # still recovers the id that site already had.
    known_by_key = {match_key(site): entry_id
                    for site, entry_id in (known_ids or {}).items()}

    # Copied so the merge cannot reach back into what the build produced.
    merged = [dict(entry) for entry in entries]
    by_site = {match_key(entry["site"]): entry for entry in merged}

    # Every id the build handed out, plus every id the last list did, so an added
    # source cannot take a name that already belongs to a blog.
    used_ids = {entry["id"] for entry in merged} | set(known_by_key.values())

    unmatched: list[str] = []

    for override in overrides:
        site = override["site"]
        entry = by_site.get(match_key(site))

        if entry is not None:
            # `site` is deliberately not patched: the generated one is where the
            # build actually landed after redirects, and is the canonical spelling.
            entry.update({k: v for k, v in override.items() if k != "site"})
            continue

        if not all(override.get(field) for field in STANDALONE_FIELDS):
            unmatched.append(site)
            continue

        # A site the last list carried keeps its id even when this build missed it,
        # for the same reason a generated entry does: article ids are built from it.
        entry_id = override.get("id") or known_by_key.get(match_key(site))
        if entry_id:
            used_ids.add(entry_id)
        else:
            entry_id = source_id(site, used_ids)

        entry = {**override, "id": entry_id}
        by_site[match_key(site)] = entry
        merged.append(entry)

    # Sorted as build_entries sorts, so a patched name or an added source lands in
    # its proper place and the file stays a readable diff rather than growing a tail.
    ordered = [ordered_entry(entry) for entry in merged]
    ordered.sort(key=lambda e: ((e.get("name") or "").lower(), e["site"]))

    return ordered, unmatched
