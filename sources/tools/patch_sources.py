#!/usr/bin/env python3
"""Apply blogs-overrides.yml to the committed blogs.yml, without rebuilding it.

A rebuild re-derives all 46,000 entries from the seed lists. It takes hours, and the
last two consecutive runs moved 4,388 sources between them to change the corpus by 2%.
Overrides are merged at the end of one, so until this existed a four-line name
correction could only be delivered by that whole machine — and six of them sat
committed and unapplied while the front page went on offering a blog called "Essays".

Patching is the same merge without the machine: load the list, apply the file,
validate, write. No network, no GitHub token, nothing the extractor's venv does not
already have. See docs/plans/keeping-the-curated-lists-current.md.

    python patch_sources.py            # apply the overrides and write blogs.yml back
    python patch_sources.py --check    # change nothing, exit non-zero if it is stale

--check is what CI runs, and it fails on two separate things. An override that has not
been applied, because a correction nobody has delivered is indistinguishable from one
nobody made. And an override matching no source that cannot stand alone, because `site`
is matched exactly: a mistyped one is otherwise silent until the next rebuild, which is
to say for weeks.

Only the overrides are applied. The build's own rules — dropping platform roots above
all — are deliberately not re-run: they belong to deriving a list from seeds, and one
of them would undo an override that put a platform root back on purpose.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Any

import yaml

from extractor.output import render_sources_yaml, validate_entries, write_sources_text
from extractor.overrides import (DROP_FIELD, OVERRIDE_FIELDS, apply_overrides,
                                 load_overrides)
from extractor.progress import log
from extractor.urls import site_key

TOOLS_DIR = Path(__file__).resolve().parent
SOURCES_DIR = TOOLS_DIR.parent

DEFAULT_SOURCES = SOURCES_DIR / "blogs.yml"
DEFAULT_OVERRIDES = SOURCES_DIR / "blogs-overrides.yml"


def load_entries(path: Path) -> list[dict[str, Any]]:
    """The committed list, in the shape apply_overrides expects."""
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    if not isinstance(data, dict):
        raise ValueError(f"{path} is not a mapping with a 'sources' key")

    entries = data.get("sources") or []
    if not isinstance(entries, list) or not entries:
        raise ValueError(f"{path} contains no sources; refusing to patch it")

    malformed = [e for e in entries
                 if not isinstance(e, dict) or not e.get("site") or not e.get("id")]
    if malformed:
        raise ValueError(
            f"{path} has {len(malformed)} malformed entr(ies); each needs an id and a site")

    return entries


def describe(before: list[dict[str, Any]], after: list[dict[str, Any]]) -> list[str]:
    """One line per source the patch changes.

    Reported per source rather than per line. A rename moves an entry in the sort, so
    the textual diff shows it twice and in two places, while the fact worth reading is
    simply that one blog's name changed.

    Keyed on the exact `site` rather than on site_key, which is what matched the
    override: blogs.yml carries pubsubhubbub.appspot.com under both http and https, and
    one drop removes both. Folding them together here would report one line for two
    entries leaving the list, which is the number a reviewer is checking.
    """
    was = {e["site"]: e for e in before}
    now = {e["site"]: e for e in after}

    lines = [f"  dropped  {was[key]['site']}" for key in sorted(was.keys() - now.keys())]
    lines += [f"  added    {now[key]['site']}" for key in sorted(now.keys() - was.keys())]

    for key in sorted(was.keys() & now.keys()):
        old, new = was[key], now[key]
        changed = [f for f in OVERRIDE_FIELDS if old.get(f) != new.get(f)]
        if changed:
            lines.append(f"  patched  {new['site']}  ({', '.join(changed)})")

    return lines


def report(changes: list[str], verb: str) -> str:
    """What the run says it did, or would do.

    A file can differ without any source differing — field order, inline lists, or the
    sort — and saying "0 sources would change" while refusing to pass reads as a bug in
    the tool rather than as a file that is simply not in the form the build writes.
    """
    if not changes:
        return f"blogs.yml is not in the form the build writes, and {verb} on that alone"
    return "\n".join([f"{len(changes)} source(s) {verb}:", *changes])


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Apply blogs-overrides.yml to blogs.yml without a rebuild.")
    parser.add_argument("--sources", type=Path, default=DEFAULT_SOURCES,
                        help="The generated source list to patch.")
    parser.add_argument("--overrides", type=Path, default=DEFAULT_OVERRIDES,
                        help="Hand-maintained corrections to apply.")
    parser.add_argument("--check", action="store_true",
                        help="Write nothing, and exit non-zero if the list is out of date.")
    args = parser.parse_args(argv)

    try:
        entries = load_entries(args.sources)
        overrides = load_overrides(args.overrides)
    except (OSError, ValueError) as exc:
        log(f"error: {exc}")
        return 1

    # Before rendering anything. With nothing to apply the entries are still exactly as
    # PyYAML loaded them, whose plain lists would render as block sequences and report
    # the whole file as changed.
    if not overrides:
        log("no overrides to apply")
        return 0

    # The ids the list already hands out, so an override added as a source in its own
    # right keeps the id its articles were stored under and cannot take another blog's.
    # Read off the entries in hand rather than through committed_sources, which would
    # parse the same 8.6 MB a second time to reach them.
    known_ids = {e["site"]: e["id"] for e in entries}

    merged, unmatched = apply_overrides(entries, overrides, known_ids)

    try:
        validate_entries(merged)
    except ValueError as exc:
        log(f"error: {exc}")
        return 1

    current = args.sources.read_text(encoding="utf-8")
    patched = render_sources_yaml(merged)
    changes = describe(entries, merged)

    # A drop that matches nothing is the resting state of every drop that has already
    # done its work, so it cannot be a failure — it would red the build permanently the
    # moment it succeeded. It stays in the file regardless, because a rebuild reads the
    # seed lists again and would otherwise bring the source back.
    dropping = {site_key(o["site"]) for o in overrides if o.get(DROP_FIELD)}
    in_effect = [s for s in unmatched if site_key(s) in dropping]
    stale = [s for s in unmatched if site_key(s) not in dropping]

    for site in in_effect:
        log(f"note: drop already in effect, kept so a rebuild cannot undo it: {site}")

    # A patch that matches nothing is different: `site` is matched exactly, so this is a
    # correction that will never be delivered. Only the unattended run has to stop for
    # it — an interactive one prints it where it will be read, and still lands the rest.
    for site in stale:
        log(f"{'error' if args.check else 'warning'}: "
            f"override matched no source and cannot stand alone: {site}")

    if args.check:
        if patched != current:
            log(report(changes, "would change"))
            log("run `make sources-patch` and commit the result")
        elif not stale:
            log("blogs.yml is up to date with blogs-overrides.yml")

        return 1 if stale or patched != current else 0

    if patched == current:
        log("blogs.yml already matches blogs-overrides.yml; nothing to do")
        return 0

    write_sources_text(args.sources, patched)
    log(report(changes, "changed"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
