"""Tests for `_search_settled` (task 8c55db6c) — the bounded retry that absorbs
Remember()'s async embedding write (task a2e00afd) between a store and the
immediate search that dense_arm_control.py / recency_control.py do inside one
question.

Every case drives `_search_settled` directly with `self._search` monkey-patched
to a counting stub — no MCP session, no network, matching the offline
convention the rest of this directory's `test_*.py` files use.

Red-before-fix / green-after-fix: reverting the `_any_embedding_pending` guard
(hardcode it to always True) makes `test_no_predicate_is_a_single_call_even_with_pending`
and `test_predicate_ignored_without_embedding_pending` FAIL — they assert
call_count == 1 where an unguarded retry loop would call more than once.
"""

from __future__ import annotations

import asyncio
import unittest

import mesh_client_stdio as mcs


def _client() -> mcs.MeshMemoryClient:
    return mcs.MeshMemoryClient(question_id="test-search-settle")


class TestSearchSettledIsOptIn(unittest.TestCase):
    def test_no_predicate_is_a_single_call_even_with_pending(self):
        """No predicate => exactly one _search call, regardless of the pending
        flag. This is today's exact behaviour for every caller that never asks
        for settling — the main scored bench must stay byte-identical."""
        c = _client()
        c._any_embedding_pending = True
        calls = []

        async def fake_search(session, query, top_k):
            calls.append(1)
            return ["miss"]

        c._search = fake_search
        out = asyncio.run(c._search_settled(None, "q", 3, None))
        self.assertEqual(len(calls), 1)
        self.assertEqual(out, ["miss"])

    def test_predicate_ignored_without_embedding_pending(self):
        """A predicate that would keep failing is NOT retried unless the store
        phase actually reported embedding_pending — a permanently-failing
        predicate on an ordinary (non-racy) question must not spend the retry
        budget for nothing."""
        c = _client()
        c._any_embedding_pending = False
        calls = []

        async def fake_search(session, query, top_k):
            calls.append(1)
            return []

        c._search = fake_search
        out = asyncio.run(c._search_settled(None, "q", 3, lambda r: False))
        self.assertEqual(len(calls), 1)
        self.assertEqual(out, [])


class TestSearchSettledRetriesOnlyWhenArmed(unittest.TestCase):
    def test_retries_until_predicate_passes_then_stops(self):
        """The documented race: first attempt misses (embedding not landed
        yet), a later attempt hits. The retry stops the instant `ok()` is
        satisfied — it must not keep spending the budget once settled."""
        c = _client()
        c._any_embedding_pending = True
        results_by_call = [["miss"], ["miss"], ["gold"]]
        calls = []

        async def fake_search(session, query, top_k):
            calls.append(1)
            return results_by_call[len(calls) - 1]

        c._search = fake_search

        slept = []

        async def fake_sleep(secs):
            slept.append(secs)

        real_sleep = asyncio.sleep
        asyncio.sleep = fake_sleep
        try:
            out = asyncio.run(
                c._search_settled(None, "q", 3, lambda r: r == ["gold"])
            )
        finally:
            asyncio.sleep = real_sleep

        self.assertEqual(len(calls), 3)
        self.assertEqual(out, ["gold"])
        self.assertEqual(len(slept), 2)  # one sleep between each retry pair

    def test_a_dead_arm_still_exhausts_the_budget_and_fails(self):
        """The control this exists to protect: if the predicate NEVER passes
        (a genuinely dead dense arm, not a timing race), the retry budget is
        spent in full and the LAST attempt's result is returned — the caller's
        REGRESSION verdict fires exactly as it did before this change. A fix
        that let this loop forever, or that returned an early success, would
        turn a real regression into a false pass."""
        c = _client()
        c._any_embedding_pending = True
        calls = []

        async def fake_search(session, query, top_k):
            calls.append(1)
            return ["miss"]

        c._search = fake_search

        async def fake_sleep(secs):
            pass

        real_sleep = asyncio.sleep
        asyncio.sleep = fake_sleep
        try:
            out = asyncio.run(c._search_settled(None, "q", 3, lambda r: False))
        finally:
            asyncio.sleep = real_sleep

        self.assertEqual(len(calls), mcs.SEARCH_SETTLE_ATTEMPTS)
        self.assertEqual(out, ["miss"])


class TestStoreCapturesEmbeddingPending(unittest.TestCase):
    """`_store` must read `embedding_pending` from the REST envelope's TOP
    level (sibling of `memory`, per rememberResponse in
    internal/handler/memory_handler.go) — not from inside the `memory` object,
    and it must latch True (never reset False by a later, non-pending store)."""

    def test_top_level_true_sets_the_flag(self):
        c = _client()

        class FakeSession:
            async def call_tool(self, name, args):
                # _parse_tool_payload passes a dict straight through (it only
                # unwraps the SDK's content/isError object shape) — this is
                # the same shape `_store` sees for a real REST envelope.
                return {"memory": {"id": "m1", "key": "k"}, "embedding_pending": True}

        asyncio.run(c._store(FakeSession(), "content", 0, "2026-01-01"))
        self.assertTrue(c._any_embedding_pending)

    def test_false_does_not_set_the_flag(self):
        c = _client()

        class FakeSession:
            async def call_tool(self, name, args):
                return {"memory": {"id": "m1", "key": "k"}, "embedding_pending": False}

        asyncio.run(c._store(FakeSession(), "content", 0, "2026-01-01"))
        self.assertFalse(c._any_embedding_pending)

    def test_latches_true_across_multiple_stores(self):
        c = _client()
        payloads = [
            {"memory": {"id": "m1", "key": "k1"}, "embedding_pending": True},
            {"memory": {"id": "m2", "key": "k2"}, "embedding_pending": False},
        ]

        class FakeSession:
            def __init__(self):
                self.n = 0

            async def call_tool(self, name, args):
                out = payloads[self.n]
                self.n += 1
                return out

        sess = FakeSession()
        asyncio.run(c._store(sess, "c1", 0, "2026-01-01"))
        asyncio.run(c._store(sess, "c2", 1, "2026-01-01"))
        self.assertTrue(c._any_embedding_pending)


if __name__ == "__main__":
    unittest.main()
