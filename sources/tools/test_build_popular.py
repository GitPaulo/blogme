"""Tests for the landing page's blog list.

Stdlib unittest and no fixtures on disk, so it needs nothing the extractor's venv does
not already have:

    cd sources/tools && .venv/bin/python -m unittest test_build_popular -v

Run by CI, alongside the override check, since the `sources` job was added. The rules
under test are the ones that decide what appears on the front page, which is reason
enough for them to be pinned somewhere a change has to walk past.
"""

from __future__ import annotations

import unittest

from build_popular import host_of, name_problem, rank


def source(site, name="A Blog", kind=("personal-blogs",)):
    return {"site": site, "name": name, "kind": list(kind)}


def points(**hosts):
    return {host: {"points": n, "stories": 1} for host, n in hosts.items()}


class HostOf(unittest.TestCase):
    def test_matches_the_key_the_scorer_writes(self):
        # Must agree with siteOf in api/internal/quality, or every lookup misses.
        self.assertEqual(host_of("https://WWW.Example.com/post"), "example.com")
        self.assertEqual(host_of("https://example.com/"), "example.com")
        self.assertEqual(host_of("nonsense"), "")

    def test_keeps_the_subdomain(self):
        # Folding these together would hand every blog on the platform one standing.
        self.assertEqual(host_of("https://someone.bearblog.dev/"), "someone.bearblog.dev")


class Eligibility(unittest.TestCase):
    def test_a_source_with_no_kind_is_not_a_blog(self):
        # This one line is the whole newspaper filter: every mainstream news domain in
        # the corpus arrived from a general link list and carries no kind at all.
        sources = [
            source("https://bbc.com/", "BBC News", kind=()),
            source("https://antirez.com/", "antirez"),
        ]
        got = rank(sources, points(**{"bbc.com": 60093, "antirez.com": 25569}))

        self.assertEqual([r["host"] for r in got], ["antirez.com"])

    def test_denied_hosts_are_dropped(self):
        sources = [source("https://www.youtube.com/", "Youtube")]

        self.assertEqual(rank(sources, points(**{"youtube.com": 49109})), [])

    def test_a_site_nobody_has_posted_is_not_ranked(self):
        # Absence is not evidence, so an unheard-of blog is simply not on this page.
        sources = [source("https://quiet.example/", "Quiet")]

        self.assertEqual(rank(sources, {}), [])

    def test_one_row_per_host_and_the_named_one_wins(self):
        # Several sources share a host, and only one of them may carry a usable name.
        sources = [
            source("https://tbray.org/", "Essays"),
            source("https://www.tbray.org/", "ongoing by Tim Bray"),
        ]
        got = rank(sources, points(**{"tbray.org": 21210}))

        self.assertEqual(len(got), 1)
        self.assertIsNone(got[0]["problem"])
        self.assertEqual(got[0]["name"], "ongoing by Tim Bray")

    def test_ordered_by_standing(self):
        sources = [
            source("https://small.example/", "Small"),
            source("https://big.example/", "Big"),
        ]
        got = rank(sources, points(**{"small.example": 10, "big.example": 900}))

        self.assertEqual([r["host"] for r in got], ["big.example", "small.example"])


class NameGate(unittest.TestCase):
    def test_a_usable_name_passes(self):
        self.assertIsNone(name_problem("Ken Shirriff's blog", 1))

    def test_generic_names_are_refused(self):
        # Clicking a row searches for the name, and "Essays" returns the whole corpus
        # rather than gwern.
        self.assertIsNotNone(name_problem("Essays", 1))
        self.assertIsNotNone(name_problem("Blog", 1))

    def test_comment_feed_titles_are_refused(self):
        self.assertIsNotNone(name_problem("Comments for Steve Blank", 1))

    def test_a_tagline_is_refused(self):
        self.assertIsNotNone(
            name_problem("Software and Tech stories from an Insider - iDiallo.com", 1)
        )

    def test_a_name_welded_to_a_tagline_is_refused(self):
        # overreacted.io's feed title is "overreacted <dash> A blog by Dan Abramov". The tail
        # is not the name, and a dash is not something the landing page should print.
        self.assertIsNotNone(name_problem("overreacted " + chr(8212) + " A blog by Dan Abramov", 1))
        self.assertIsNotNone(name_problem("Hillel Wayne - Computer Things", 1))
        self.assertIsNone(name_problem("overreacted", 1))
        # A hyphen inside a word is not a separator.
        self.assertIsNone(name_problem("Half-Life of Software", 1))

    def test_a_name_two_blogs_answer_to_is_refused(self):
        # Ambiguous as a query whatever it says, so clicking it cannot reach either.
        self.assertIsNone(name_problem("ongoing by Tim Bray", 1))
        self.assertIsNotNone(name_problem("ongoing by Tim Bray", 2))


if __name__ == "__main__":
    unittest.main()
