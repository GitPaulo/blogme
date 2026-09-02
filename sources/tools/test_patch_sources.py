"""Tests for applying overrides without a rebuild.

Stdlib unittest and temporary files, so it needs nothing the extractor's venv does not
already have:

    cd sources/tools && .venv/bin/python -m unittest test_patch_sources -v

What is pinned here is the behaviour CI depends on: that patching twice changes nothing
the second time, and that --check can tell a correction nobody delivered from a drop
that has already done its work. Getting the second one wrong makes the check useless in
opposite directions — it either never fires or never stops firing.
"""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import yaml

from extractor.output import render_sources_yaml
from extractor.overrides import ordered_entry
from patch_sources import describe, load_entries, main

ENTRIES = [
    {"id": "alpha", "name": "Alpha", "site": "https://alpha.example/",
     "feed": "https://alpha.example/rss", "tags": ["tech"]},
    {"id": "hub", "name": "Relay", "site": "http://hub.example/", "tags": ["tech"]},
    {"id": "hub-2", "name": "Relay", "site": "https://hub.example/", "tags": ["tech"]},
    {"id": "zeta", "name": "Comments for Zeta", "site": "https://zeta.example/",
     "tags": ["tech"]},
]


class Harness(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.sources = self.dir / "blogs.yml"
        self.overrides = self.dir / "blogs-overrides.yml"
        self.write_sources(ENTRIES)
        self.write_overrides([])

    def write_sources(self, entries):
        # Through ordered_entry, so the fixture is in the form the build writes: field
        # order and inline lists included. Skipping it would leave every test failing
        # on formatting before it reached the behaviour under test.
        text = render_sources_yaml([ordered_entry(e) for e in entries])
        self.sources.write_text(text, encoding="utf-8", newline="\n")

    def write_overrides(self, overrides):
        self.overrides.write_text(
            yaml.safe_dump({"sources": overrides}), encoding="utf-8", newline="\n")

    def run_tool(self, *flags):
        return main(["--sources", str(self.sources), "--overrides", str(self.overrides), *flags])

    def sites(self):
        return [e["site"] for e in load_entries(self.sources)]

    def named(self, site):
        return next(e["name"] for e in load_entries(self.sources) if e["site"] == site)


class Applying(Harness):
    def test_a_name_is_replaced(self):
        self.write_overrides([{"site": "https://zeta.example/", "name": "Zeta"}])
        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(self.named("https://zeta.example/"), "Zeta")

    def test_one_drop_removes_every_spelling_of_the_site(self):
        # site_key ignores the scheme, so the http and https entries are one blog. The
        # overrides file cannot name both — that is rejected as a duplicate.
        self.write_overrides([{"site": "https://hub.example/", "drop": True}])
        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(self.sites(), ["https://alpha.example/", "https://zeta.example/"])

    def test_patching_twice_changes_nothing_the_second_time(self):
        # The property CI leans on: --check may only fail on work left undone.
        self.write_overrides([{"site": "https://zeta.example/", "name": "Zeta"}])
        self.assertEqual(self.run_tool(), 0)
        after_first = self.sources.read_text(encoding="utf-8")

        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(self.sources.read_text(encoding="utf-8"), after_first)
        self.assertEqual(self.run_tool("--check"), 0)

    def test_a_rename_lands_in_its_new_place_in_the_sort(self):
        # Renaming re-files the entry, which is what keeps the file a readable diff.
        self.write_overrides([{"site": "https://zeta.example/", "name": "Aardvark"}])
        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(self.sites()[0], "https://zeta.example/")

    def test_an_override_can_add_a_source_the_list_lacks(self):
        self.write_overrides([{"site": "https://new.example/", "name": "New",
                               "tags": ["tech"]}])
        self.assertEqual(self.run_tool(), 0)
        self.assertIn("https://new.example/", self.sites())

    def test_nothing_to_apply_leaves_the_file_alone(self):
        before = self.sources.read_text(encoding="utf-8")
        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(self.sources.read_text(encoding="utf-8"), before)


class Checking(Harness):
    def test_an_unapplied_override_fails(self):
        self.write_overrides([{"site": "https://zeta.example/", "name": "Zeta"}])
        self.assertEqual(self.run_tool("--check"), 1)
        # And changed nothing on the way to saying so.
        self.assertEqual(self.named("https://zeta.example/"), "Comments for Zeta")

    def test_a_patch_matching_nothing_fails(self):
        # `site` is matched exactly, so this correction will never be delivered.
        self.write_overrides([{"site": "https://typo.example/", "name": "Typo"}])
        self.assertEqual(self.run_tool("--check"), 1)

    def test_a_drop_that_has_already_run_passes(self):
        # The resting state of every drop that worked. Failing here would red the build
        # permanently from the moment the drop succeeded.
        self.write_overrides([{"site": "https://hub.example/", "drop": True}])
        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(self.run_tool("--check"), 0)

    def test_a_malformed_overrides_file_fails(self):
        # Two spellings of one site: the build would reject this too, but only hours in.
        self.write_overrides([{"site": "http://hub.example/", "drop": True},
                              {"site": "https://hub.example/", "drop": True}])
        self.assertEqual(self.run_tool("--check"), 1)

    def test_an_empty_source_list_fails_rather_than_being_patched(self):
        self.sources.write_text("sources: []\n", encoding="utf-8", newline="\n")
        self.assertEqual(self.run_tool("--check"), 1)


class Describe(unittest.TestCase):
    def test_counts_each_entry_that_leaves_not_each_blog(self):
        # Both spellings of hub.example go, and a reviewer is checking that number.
        after = [e for e in ENTRIES if "hub.example" not in e["site"]]
        self.assertEqual(len(describe(ENTRIES, after)), 2)

    def test_names_the_fields_a_patch_changed(self):
        after = [{**e, "name": "Zeta"} if e["id"] == "zeta" else e for e in ENTRIES]
        self.assertEqual(describe(ENTRIES, after),
                         ["  patched  https://zeta.example/  (name)"])

    def test_says_nothing_when_nothing_moved(self):
        self.assertEqual(describe(ENTRIES, ENTRIES), [])


if __name__ == "__main__":
    unittest.main()
