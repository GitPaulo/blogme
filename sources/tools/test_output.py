"""Tests for how entries are ordered and rendered into blogs.yml.

    cd sources/tools && .venv/bin/python -m unittest test_output -v

Only the sort is pinned here, and it is pinned because getting it wrong is invisible in
a rebuild and expensive everywhere else. build_entries sorted on the name a candidate
arrived with while writing the name derived from its domain, so 97 blogs in a 46,083
entry list were filed nowhere near where their name puts them — and every later run that
re-sorted the file, including `make sources-patch`, moved 13,576 lines to put them right
for no reason a reader of the diff could see.
"""

from __future__ import annotations

import unittest

from extractor.models import Candidate
from extractor.output import build_entries, render_sources_yaml


class Ordering(unittest.TestCase):
    def entries(self, *candidates):
        return build_entries(list(candidates))

    def test_a_blog_with_no_name_is_filed_under_the_one_written_for_it(self):
        # ideobook.com has no name of its own and is written as "Ideobook", so it belongs
        # between Alpha and Zebra — not wherever the empty string it arrived with sorts.
        entries = self.entries(
            Candidate(site="https://zebra.example/", name="Zebra", tags={"tech"}),
            Candidate(site="https://ideobook.com/", tags={"tech"}),
            Candidate(site="https://alpha.example/", name="Alpha", tags={"tech"}),
        )
        self.assertEqual([e["name"] for e in entries], ["Alpha", "Ideobook", "Zebra"])

    def test_the_whole_list_is_sorted_by_the_name_it_writes(self):
        candidates = [
            Candidate(site="https://zebra.example/", name="Zebra", tags={"tech"}),
            Candidate(site="https://0mwindybug.github.io/", tags={"tech"}),
            Candidate(site="https://middle.example/", name="Middle", tags={"tech"}),
            Candidate(site="https://ideobook.com/", tags={"tech"}),
        ]
        names = [e["name"].lower() for e in self.entries(*candidates)]
        self.assertEqual(names, sorted(names))

    def test_one_site_per_entry_whatever_order_it_arrived_in(self):
        # Redirects land several candidates on one blog.
        entries = self.entries(
            Candidate(site="https://alpha.example/", name="Alpha", tags={"tech"}),
            Candidate(site="https://alpha.example/", name="Alpha", feed="https://alpha.example/rss"),
        )
        self.assertEqual(len(entries), 1)
        self.assertEqual(entries[0]["feed"], "https://alpha.example/rss")


class Rendering(unittest.TestCase):
    def test_kind_and_tags_stay_inline(self):
        # Block sequences here would rewrite all 46,000 entries on the next run.
        text = render_sources_yaml(self.entry())
        self.assertIn("tags: [tech]", text)

    def test_the_header_survives(self):
        self.assertTrue(render_sources_yaml(self.entry()).startswith("# Approved sources."))

    def entry(self):
        return build_entries([Candidate(site="https://alpha.example/", name="Alpha",
                                        tags={"tech"})])


if __name__ == "__main__":
    unittest.main()
