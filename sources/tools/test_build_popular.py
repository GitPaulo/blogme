"""Tests for the landing page's blog list.

Stdlib unittest and no fixtures on disk, so it needs nothing the extractor's venv does
not already have:

    cd sources/tools && .venv/bin/python -m unittest test_build_popular -v

Run by CI, alongside the override check, since the `sources` job was added. The rules
under test are the ones that decide what appears on the front page, which is reason
enough for them to be pinned somewhere a change has to walk past.

The refusal tests matter more than the rest since the weekly job was added: it runs
unattended, so a check that degrades instead of stopping ships a broken front page and
nobody reads the warning.
"""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import build_popular
from build_popular import (
    MIN_SITES_WITH_POINTS,
    Refused,
    choose,
    host_of,
    load_popularity,
    name_problem,
    previous_hosts,
    rank,
    render,
)


def source(site, name="A Blog", kind=("personal-blogs",), id=None):
    # An id, because a blog with none cannot be browsed and rank now drops it.
    return {
        "id": id if id is not None else host_of(site).replace(".", "-"),
        "site": site,
        "name": name,
        "kind": list(kind),
    }


def points(**hosts):
    return {host: {"points": n, "stories": 1} for host, n in hosts.items()}


def a_healthy_map(**hosts):
    """A popularity map large enough to pass the truncated-download check."""
    filler = {f"filler{i}.example": {"points": 1} for i in range(MIN_SITES_WITH_POINTS)}
    return {**filler, **points(**hosts)}


def blog(host, name, articles=50, points=1000):
    row = {"name": name, "site": f"https://{host}/", "host": host,
           "ids": [host], "points": points, "problem": None}
    # Absent rather than None when unchecked, which is how choose leaves it.
    if articles is not None:
        row["articles"] = articles
    return row


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

    def test_a_blog_with_no_id_cannot_be_browsed(self):
        # Clicking a row filters on the ids. With none there is nothing to filter on,
        # and the empty filter clause it would build is a 400 the index reads as a
        # search outage rather than as a bad row.
        sources = [source("https://nameless.example/", "Nameless", id="")]

        self.assertEqual(rank(sources, points(**{"nameless.example": 900})), [])

    def test_a_blog_listed_more_times_than_the_api_filters_on_is_dropped(self):
        # Mirrors maxSourceIDs: it could never be shown completely.
        sources = [
            source("https://many.example/", "Many", id=f"many-{i}")
            for i in range(build_popular.MAX_IDS + 1)
        ]

        self.assertEqual(rank(sources, points(**{"many.example": 900})), [])

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


class PopularityMap(unittest.TestCase):
    """The blob arrives over the network, so it can arrive wrong."""

    def written(self, body: str) -> Path:
        path = Path(self.tmp.name) / "popularity.json"
        path.write_text(body, encoding="utf-8")
        return path

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def test_a_healthy_map_loads(self):
        got = load_popularity(self.written(json.dumps(a_healthy_map(**{"antirez.com": 25569}))))

        self.assertEqual(got["antirez.com"]["points"], 25569)

    def test_a_missing_file_is_refused(self):
        with self.assertRaises(Refused):
            load_popularity(Path(self.tmp.name) / "absent.json")

    def test_a_truncated_download_is_refused(self):
        with self.assertRaises(Refused):
            load_popularity(self.written('{"antirez.com": {"poi'))

    def test_a_list_is_refused(self):
        with self.assertRaises(Refused):
            load_popularity(self.written("[]"))

    def test_a_map_with_almost_no_standing_is_refused(self):
        # An empty or half-written blob reads as "nobody has ever been posted", which
        # the ranking cannot tell from the truth and would answer with an empty page.
        with self.assertRaises(Refused):
            load_popularity(self.written(json.dumps(points(**{"antirez.com": 25569}))))


class Choosing(unittest.TestCase):
    """Walking the ranking for blogs the index can actually show."""

    def named(self, count):
        return [
            {"name": f"Blog {i}", "site": f"https://b{i}.example/", "host": f"b{i}.example",
             "ids": [f"b{i}"], "points": 1000 - i, "problem": None}
            for i in range(count)
        ]

    def test_without_a_key_nothing_can_be_vouched_for(self):
        with self.assertRaises(Refused):
            choose(self.named(20), 2, endpoint="", key="")

    def test_it_walks_past_blogs_the_crawler_has_not_reached(self):
        # utcc.utoronto.ca is the real case: near the top on standing, no documents.
        counts = {"b0": 0, "b1": 2, "b2": 90, "b3": 40}
        with patch.object(build_popular, "indexed", lambda e, k, ids: counts[ids[0]]):
            got = choose(self.named(20), 2, endpoint="https://x", key="k")

        self.assertEqual([r["host"] for r in got], ["b2.example", "b3.example"])

    def test_a_short_list_is_refused_rather_than_shipped(self):
        with patch.object(build_popular, "indexed", lambda e, k, ids: 0):
            with self.assertRaises(Refused):
                choose(self.named(20), 12, endpoint="https://x", key="k")

    def test_it_stops_rather_than_scanning_the_whole_corpus(self):
        # A systematic fault should be reported, not turned into thousands of requests.
        asked = []
        with patch.object(build_popular, "indexed",
                          lambda e, k, ids: asked.append(ids) or 0):
            with self.assertRaises(Refused):
                choose(self.named(5000), 12, endpoint="https://x", key="k")

        self.assertEqual(len(asked), build_popular.MAX_SCAN)

    def test_an_unreachable_index_stops_the_run(self):
        # The alternative is a blog silently swapped off the front page for one timeout.
        def unreachable(endpoint, key, ids):
            raise Refused("could not reach the index")

        with patch.object(build_popular, "indexed", unreachable):
            with self.assertRaises(Refused):
                choose(self.named(20), 12, endpoint="https://x", key="k")


class Report(unittest.TestCase):
    """The pull request body. It is the only thing a reviewer reads before merging."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def test_it_names_what_arrived_and_what_left(self):
        body = render(
            chosen=[blog("antirez.com", "antirez"), blog("tonsky.me", "tonsky.me")],
            rejected=[],
            before={"tonsky.me": "tonsky.me", "gwern.net": "Essays"},
            corpus=46083,
            unchecked=False,
        )

        self.assertIn("| add | antirez | `antirez.com` |", body)
        self.assertIn("| drop | Essays | `gwern.net` |", body)
        self.assertIn("46,083 sources", body)

    def test_an_unchanged_list_says_so(self):
        body = render(
            chosen=[blog("antirez.com", "antirez")],
            rejected=[],
            before={"antirez.com": "antirez"},
            corpus=46083,
            unchecked=False,
        )

        self.assertIn("The same blogs as before", body)

    def test_an_unchecked_build_is_labelled(self):
        body = render([blog("antirez.com", "antirez", articles=None)], [], {}, 46083,
                      unchecked=True)

        self.assertIn("not checked", body)
        self.assertIn("no search key", body)

    def test_the_previous_list_is_read_back_from_the_committed_file(self):
        path = Path(self.tmp.name) / "popular.json"
        path.write_text(json.dumps({
            "corpus": 46083,
            "blogs": [{"name": "antirez", "site": "https://antirez.com/",
                       "host": "antirez.com", "ids": ["antirez"]}],
        }), encoding="utf-8")

        self.assertEqual(previous_hosts(path), {"antirez.com": "antirez"})

    def test_a_first_run_has_no_previous_list(self):
        self.assertEqual(previous_hosts(Path(self.tmp.name) / "absent.json"), {})


if __name__ == "__main__":
    unittest.main()
