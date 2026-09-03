"""Tests for the vocabulary both landing-page generators share.

    cd sources/tools && .venv/bin/python -m unittest test_corpus -v

The network tests are the point of this file. Both generators run unattended, and the
failures that actually happen to a long-lived HTTP call are not the ones the obvious
`except URLError` catches.
"""

from __future__ import annotations

import http.client
import json
import ssl
import unittest
from unittest.mock import patch

import corpus
from corpus import Refused, ask_index, hosts_by_name, is_blog, source_ids_by_host


def source(site, name="A Blog", kind=("personal-blogs",), id=None):
    return {"id": id, "site": site, "name": name, "kind": list(kind)}


class SourceIndexes(unittest.TestCase):
    def test_ids_are_gathered_per_host_without_duplicates(self):
        # righto.com has two ids, and filtering only one returns 5 articles where both
        # return 57.
        got = source_ids_by_host([
            source("https://righto.com/", id="righto"),
            source("https://www.righto.com/", id="righto-2"),
            source("https://righto.com/feed", id="righto"),
        ])

        self.assertEqual(got, {"righto.com": ["righto", "righto-2"]})

    def test_a_source_with_no_id_contributes_nothing(self):
        self.assertEqual(source_ids_by_host([source("https://x.example/", id=None)]), {})

    def test_names_are_counted_over_hosts_not_sources(self):
        # tbray.org appears four times; that is one blog, not four claims on the name.
        got = hosts_by_name([
            source("https://tbray.org/", "ongoing"),
            source("https://www.tbray.org/", "ongoing"),
        ])

        self.assertEqual(got, {"ongoing": {"tbray.org"}})


class IsBlog(unittest.TestCase):
    def test_a_kinded_source_is_a_blog(self):
        self.assertTrue(is_blog(source("https://antirez.com/"), "antirez.com"))

    def test_no_kind_is_the_whole_newspaper_filter(self):
        self.assertFalse(is_blog(source("https://bbc.com/", kind=()), "bbc.com"))

    def test_a_company_blog_is_not(self):
        # The page names independent blogs. `company-blogs` covers Stripe, Twilio,
        # JetBrains, Uber and some ninety more: worth indexing, worth searching, and not
        # what a reader looking for independent writing came to the front page for.
        self.assertFalse(
            is_blog(source("https://stripe.com/blog", kind=("company-blogs",)), "stripe.com")
        )

    def test_a_denied_host_is_not(self):
        self.assertFalse(is_blog(source("https://github.com/"), "github.com"))

    def test_the_corporate_hosts_the_kind_filter_cannot_reach_are_denied(self):
        # Both are kinded as personal blogs in the corpus. aws.amazon.com would sit
        # seventh on standing under the feed title "Recent Announcements", and
        # addons.mozilla.org is a listing of extensions rather than writing.
        for host in ("aws.amazon.com", "addons.mozilla.org"):
            with self.subTest(host=host):
                self.assertFalse(is_blog(source(f"https://{host}/"), host))

    def test_an_unparseable_site_is_not(self):
        self.assertFalse(is_blog(source("nonsense"), ""))


class AskIndex(unittest.TestCase):
    """What happens when the index answers badly, or not at all."""

    def answer(self, payload):
        class Response:
            def read(inner):
                return json.dumps(payload).encode()

            def __enter__(inner):
                return inner

            def __exit__(inner, *a):
                return False
        return Response()

    def test_a_good_answer_comes_back_parsed(self):
        with patch.object(corpus, "urlopen", lambda *a, **k: self.answer({"value": [1]})):
            self.assertEqual(ask_index("https://x", "k", {"search": "*"}), {"value": [1]})

    def test_the_failures_that_actually_happen_are_retried_then_refused(self):
        # None of these is a URLError, and every one of them escaped the handler before
        # this test existed: a reset connection, a dead TLS session, and a body that
        # stopped halfway are the three ways a long HTTP call really fails.
        for err in (ConnectionResetError("reset"),
                    http.client.RemoteDisconnected("gone"),
                    http.client.IncompleteRead(b"half"),
                    ssl.SSLError("handshake"),
                    TimeoutError("slow")):
            with self.subTest(error=type(err).__name__):
                attempts = []

                def failing(*a, **k):
                    attempts.append(1)
                    raise err

                with patch.object(corpus, "urlopen", failing), \
                        patch.object(corpus, "time") as clock:
                    clock.sleep = lambda _: None
                    with self.assertRaises(Refused):
                        ask_index("https://x", "k", {"search": "*"})

                self.assertEqual(len(attempts), corpus.SEARCH_ATTEMPTS)

    def test_a_4xx_is_an_answer_and_is_not_retried(self):
        # The query is wrong; asking again cannot change that.
        attempts = []

        def rejecting(*a, **k):
            attempts.append(1)
            raise corpus.HTTPError("https://x", 400, "Bad Request", {}, None)

        with patch.object(corpus, "urlopen", rejecting):
            with self.assertRaises(Refused):
                ask_index("https://x", "k", {"search": "*"})

        self.assertEqual(len(attempts), 1)

    def test_a_5xx_is_retried(self):
        attempts = []

        def flaky(*a, **k):
            attempts.append(1)
            if len(attempts) < 2:
                raise corpus.HTTPError("https://x", 503, "Unavailable", {}, None)
            return self.answer({"value": []})

        with patch.object(corpus, "urlopen", flaky), patch.object(corpus, "time") as clock:
            clock.sleep = lambda _: None
            self.assertEqual(ask_index("https://x", "k", {"search": "*"}), {"value": []})

        self.assertEqual(len(attempts), 2)

    def test_a_body_that_is_not_json_is_retried_then_refused(self):
        class Garbage:
            def read(inner):
                return b"<html>502</html>"

            def __enter__(inner):
                return inner

            def __exit__(inner, *a):
                return False

        with patch.object(corpus, "urlopen", lambda *a, **k: Garbage()), \
                patch.object(corpus, "time") as clock:
            clock.sleep = lambda _: None
            with self.assertRaises(Refused):
                ask_index("https://x", "k", {"search": "*"})


if __name__ == "__main__":
    unittest.main()
