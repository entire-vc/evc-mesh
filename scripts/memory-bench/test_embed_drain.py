"""Tests for the embedSem-backlog drain wait (#ebd9dc1c).

`remember()` returns before its embed lands (memory_service.go: `go
s.embedAndStore(...)`), and that embed shares a bounded semaphore with the
recall query-embed (#3d10774e). A harness that fires many writes with no
pacing can queue a backlog deeper than the embedder drains per second; once
deep enough, a LATER question's recall queues behind it long enough to trip
the REST client's own timeout — measured live on PR #739 (17-20/24 questions,
120s+ each) even after the timeout itself had already been raised.

`_wait_for_embed_drain_sync` polls `mesh_memory_embed_inflight` (a real
backlog-depth signal, pkg/metrics/metrics.go) instead of guessing a sleep, and
must fail OPEN — proceed rather than hang — whenever it cannot observe that
signal. Every case here drives it directly against a stubbed
`urllib.request.urlopen`; no network, matching this directory's `test_*.py`
convention (see test_search_settle.py's docstring).
"""

from __future__ import annotations

import unittest
import urllib.error
from unittest import mock

import mesh_client_stdio as mcs


def _metrics_body(inflight: float | None) -> bytes:
    lines = [
        "# HELP mesh_http_requests_total total\n",
        "mesh_http_requests_total 1\n",
    ]
    if inflight is not None:
        lines.append(f"mesh_memory_embed_inflight {inflight}\n")
    return "".join(lines).encode("utf-8")


class _FakeResponse:
    def __init__(self, body: bytes):
        self._body = body

    def read(self):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class TestReadEmbedInflight(unittest.TestCase):
    def setUp(self):
        mcs._embed_drain_warned = False

    def test_parses_the_gauge_line(self):
        with mock.patch.object(
            mcs.urllib.request, "urlopen", return_value=_FakeResponse(_metrics_body(3.0))
        ):
            self.assertEqual(mcs._read_embed_inflight("http://127.0.0.1:8005"), 3.0)

    def test_zero_is_a_real_value_not_a_failure(self):
        with mock.patch.object(
            mcs.urllib.request, "urlopen", return_value=_FakeResponse(_metrics_body(0.0))
        ):
            self.assertEqual(mcs._read_embed_inflight("http://127.0.0.1:8005"), 0.0)

    def test_unreachable_returns_none(self):
        with mock.patch.object(
            mcs.urllib.request, "urlopen", side_effect=urllib.error.URLError("refused")
        ):
            self.assertIsNone(mcs._read_embed_inflight("http://127.0.0.1:8005"))

    def test_metric_absent_returns_none(self):
        """A server predating #ebd9dc1c serves /metrics fine with no such gauge
        — this must be indistinguishable from "can't observe it", not treated
        as a parse crash or as a false 0."""
        with mock.patch.object(
            mcs.urllib.request, "urlopen", return_value=_FakeResponse(_metrics_body(None))
        ):
            self.assertIsNone(mcs._read_embed_inflight("http://127.0.0.1:8005"))

    def test_base_url_trailing_slash_does_not_double_up(self):
        seen = {}

        def _capture(url, timeout):
            seen["url"] = url
            return _FakeResponse(_metrics_body(0.0))

        with mock.patch.object(mcs.urllib.request, "urlopen", side_effect=_capture):
            mcs._read_embed_inflight("http://127.0.0.1:8005/")
        self.assertEqual(seen["url"], "http://127.0.0.1:8005/metrics")


class TestWaitForEmbedDrainSync(unittest.TestCase):
    def setUp(self):
        mcs._embed_drain_warned = False

    def test_already_zero_returns_immediately_no_sleep(self):
        slept = []
        with (
            mock.patch.object(mcs, "_read_embed_inflight", return_value=0.0),
            mock.patch.object(mcs.time, "sleep", side_effect=lambda s: slept.append(s)),
        ):
            mcs._wait_for_embed_drain_sync("http://127.0.0.1:8005", "q1")
        self.assertEqual(slept, [])

    def test_drains_after_a_few_polls_then_returns(self):
        """3 -> 1 -> 0: two sleeps, then a clean return — not the max-wait path."""
        depths = iter([3.0, 1.0, 0.0])
        slept = []
        with (
            mock.patch.object(mcs, "_read_embed_inflight", side_effect=lambda u: next(depths)),
            mock.patch.object(mcs.time, "sleep", side_effect=lambda s: slept.append(s)),
        ):
            mcs._wait_for_embed_drain_sync("http://127.0.0.1:8005", "q1")
        self.assertEqual(slept, [mcs.EMBED_DRAIN_POLL_INTERVAL_SECS] * 2)

    def test_never_observing_it_returns_immediately_fail_open(self):
        """`_read_embed_inflight` returning None (unreachable / metric absent)
        must never enter the poll loop — that would be waiting on nothing."""
        slept = []
        with (
            mock.patch.object(mcs, "_read_embed_inflight", return_value=None),
            mock.patch.object(mcs.time, "sleep", side_effect=lambda s: slept.append(s)),
        ):
            mcs._wait_for_embed_drain_sync("http://127.0.0.1:8005", "q1")
        self.assertEqual(slept, [])

    def test_backlog_that_never_drains_is_bounded_not_infinite(self):
        """A permanently non-zero backlog must not hang the job — it spends the
        EMBED_DRAIN_MAX_WAIT_SECS budget and returns, exactly like
        CONNECT_RETRIES/SEARCH_SETTLE_ATTEMPTS elsewhere in this file."""
        clock = [0.0]

        def _fake_monotonic():
            return clock[0]

        def _fake_sleep(s):
            clock[0] += s

        with (
            mock.patch.object(mcs, "_read_embed_inflight", return_value=5.0),
            mock.patch.object(mcs.time, "monotonic", side_effect=_fake_monotonic),
            mock.patch.object(mcs.time, "sleep", side_effect=_fake_sleep),
        ):
            mcs._wait_for_embed_drain_sync("http://127.0.0.1:8005", "q1")
        # Terminated at all (no timeout/hang) and did not wait past the budget
        # by more than one poll interval.
        self.assertLessEqual(
            clock[0], mcs.EMBED_DRAIN_MAX_WAIT_SECS + mcs.EMBED_DRAIN_POLL_INTERVAL_SECS
        )
        self.assertGreaterEqual(clock[0], mcs.EMBED_DRAIN_MAX_WAIT_SECS)


if __name__ == "__main__":
    unittest.main()
