"""Tests for folding together sources the crawler would read identically.

    cd sources/tools && .venv/bin/python -m unittest test_merge -v

What is pinned here is the line between a duplicate and a neighbour. Folding too little
leaves every post indexed twice; folding too much hands one writer's blog to another, or
stops reading a section of a site that has its own feed.
"""

from __future__ import annotations

import unittest

from extractor.merge import merge_duplicate_crawls


def entry(id_, site, feed=None, tags=("tech",), kind=None):
    e = {"id": id_, "name": id_.title(), "site": site, "tags": list(tags)}
    if feed:
        e["feed"] = feed
    if kind:
        e["kind"] = list(kind)
    return e


def ids(entries):
    return [e["id"] for e in entries]


class Folding(unittest.TestCase):
    def test_one_site_spelled_twice_is_one_source(self):
        kept, folded = merge_duplicate_crawls([
            entry("hub", "http://hub.example/"),
            entry("hub-2", "https://www.hub.example"),
        ])
        self.assertEqual(ids(kept), ["hub-2"])
        self.assertEqual(folded, {"hub": "hub-2"})

    def test_two_sites_reading_one_feed_are_one_source(self):
        kept, folded = merge_duplicate_crawls([
            entry("semaphore", "https://semaphore.example/", feed="https://semaphore.example/feed"),
            entry("semaphore-blog", "https://semaphore.example/blog/",
                  feed="http://semaphore.example/feed/"),
        ])
        self.assertEqual(ids(kept), ["semaphore"])
        self.assertEqual(folded, {"semaphore-blog": "semaphore"})

    def test_feedless_sections_of_one_host_walk_one_sitemap(self):
        # The sitemap walk keeps every page on the host, whatever path the site names.
        kept, folded = merge_duplicate_crawls([
            entry("awful", "https://awful.example/"),
            entry("awful-notes", "https://awful.example/notes/"),
            entry("awful-journal", "https://awful.example/journal/"),
        ])
        self.assertEqual(ids(kept), ["awful"])
        self.assertEqual(set(folded), {"awful-notes", "awful-journal"})

    def test_a_section_with_a_feed_of_its_own_is_kept(self):
        # adactio.com/ and adactio.com/journal/ are two streams of writing: see
        # drop_platform_roots, which keeps both on purpose.
        kept, folded = merge_duplicate_crawls([
            entry("adactio", "https://adactio.example/", feed="https://adactio.example/links/rss"),
            entry("adactio-journal", "https://adactio.example/journal/",
                  feed="https://adactio.example/journal/rss"),
        ])
        self.assertEqual(ids(kept), ["adactio", "adactio-journal"])
        self.assertEqual(folded, {})

    def test_writers_on_a_shared_host_are_never_folded(self):
        kept, folded = merge_duplicate_crawls([
            entry("tilde-ann", "https://tilde.example/~ann/"),
            entry("tilde-bob", "https://tilde.example/~bob/"),
            entry("medium-cat", "https://medium.com/@cat"),
            entry("medium-dan", "https://medium.com/@dan"),
        ])
        self.assertEqual(len(kept), 4)
        self.assertEqual(folded, {})

    def test_a_blogger_feed_is_told_apart_by_its_query(self):
        kept, _ = merge_duplicate_crawls([
            entry("a", "https://a.example/", feed="https://a.example/feeds/posts/default?alt=rss"),
            entry("b", "https://b.example/", feed="https://a.example/feeds/posts/default?alt=atom"),
        ])
        self.assertEqual(ids(kept), ["a", "b"])


class Survivor(unittest.TestCase):
    def test_the_entry_with_a_feed_survives(self):
        kept, _ = merge_duplicate_crawls([
            entry("blog", "https://blog.example/"),
            entry("blog-2", "http://blog.example/", feed="https://blog.example/rss"),
        ])
        self.assertEqual(ids(kept), ["blog-2"])

    def test_among_equals_the_oldest_id_survives(self):
        # sentry was handed out before sentry-2, so it has been collecting for longer.
        kept, _ = merge_duplicate_crawls([
            entry("sentry-3", "https://sentry.example/changelog/"),
            entry("sentry", "https://sentry.example/blog/"),
            entry("sentry-2", "https://sentry.example/"),
        ])
        self.assertEqual(ids(kept), ["sentry"])

    def test_what_the_build_learned_about_a_duplicate_is_kept(self):
        kept, _ = merge_duplicate_crawls([
            entry("blog", "https://blog.example/", feed="https://blog.example/rss"),
            entry("blog-2", "http://blog.example/", tags=("security",),
                  kind=("personal-blogs",)),
        ])
        self.assertEqual(kept[0]["kind"], ["personal-blogs"])
        self.assertEqual(set(kept[0]["tags"]), {"tech", "security"})

    def test_an_entry_an_override_names_is_neither_folded_nor_edited(self):
        # Unpinned, `blog` would win on its shorter id. The override outranks that.
        pinned = {"blog.example/notes"}
        kept, folded = merge_duplicate_crawls([
            entry("blog", "https://blog.example/", tags=("security",)),
            entry("blog-2", "https://blog.example/notes/"),
        ], pinned)
        self.assertEqual(ids(kept), ["blog-2"])
        self.assertEqual(folded, {"blog": "blog-2"})
        self.assertEqual(kept[0]["tags"], ["tech"])

    def test_folding_twice_changes_nothing_the_second_time(self):
        first, _ = merge_duplicate_crawls([
            entry("hub", "http://hub.example/"),
            entry("hub-2", "https://hub.example/"),
            entry("solo", "https://solo.example/"),
        ])
        second, folded = merge_duplicate_crawls(first)
        self.assertEqual(second, first)
        self.assertEqual(folded, {})


if __name__ == "__main__":
    unittest.main()
