#!/usr/bin/env python3
"""Self-check for per-RUN fixture isolation in the recall gate.

    python scripts/memory-bench/test_fixture_isolation.py   # or: python -m unittest

Same convention as the sibling self-checks: stdlib `unittest`, no Mesh, no
network, no dependencies.

Background. Fixture names were a pure function of the question id, while
`remember` UPSERTs on key and cleanup deletes by tag. Two gate runs against one
workspace therefore wrote the same rows and swept each other's haystacks
mid-measurement. The damage is invisible by construction: a question whose
haystack was deleted under it retrieves nothing and scores a clean miss, which
is byte-identical to a real recall failure. And the gate is a REQUIRED check, so
a corrupted number sits on the merge path looking like a verdict (#eb1c5617).

Concurrency here is ordinary, not exotic: two open PRs, a push to main
overlapping an open PR, the nightly landing on either.

The properties pinned here:

  * Two runs never share a fixture namespace — not in the tag (the cleanup and
    recall handle) and not in the key (the UPSERT handle). Both, or the fix is
    half-done: disjoint tags alone still let one run's `remember` overwrite the
    other's rows.
  * A run's tag sweep cannot reach another run's rows.
  * The orphan collector selects on AGE, never on ownership. "Not mine" would
    delete a live peer's fixtures — the original bug with a wider blast radius.
  * An unreadable `created_at` is a refusal to delete, not an epoch-0 row.
  * A malformed nonce override degrades to a fresh namespace, never back to the
    shared one.
"""

from __future__ import annotations

import asyncio
import sys
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mesh_client_stdio  # noqa: E402
from mesh_client_stdio import MeshMemoryClient, SHARED_TAG  # noqa: E402


def _hours_ago(h: float) -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - h * 3600))


class FakeWorkspace:
    """A minimal stand-in for the shared Mesh workspace.

    Models only what the isolation properties depend on: rows carry tags, recall
    filters by `tags_any`, forget deletes by id.
    """

    def __init__(self):
        self.rows: dict[str, dict] = {}
        self.forgotten: list[str] = []

    def store(self, mid, tags, created_at=None):
        self.rows[mid] = {"id": mid, "tags": list(tags),
                          "created_at": created_at or _hours_ago(0)}

    async def call_tool(self, name, args):
        if name == "recall":
            wanted = set(args.get("tags_any") or [])
            return {"items": [r for r in self.rows.values()
                              if wanted & set(r["tags"])],
                    "search_mode": "hybrid"}
        if name == "forget":
            mid = args["memory_id"]
            self.forgotten.append(mid)
            self.rows.pop(mid, None)
            return {"ok": True}
        raise AssertionError(f"unexpected tool {name}")


def _sem():
    return asyncio.Semaphore(1)


class TestTwoRunsDoNotShareAFixtureNamespace(unittest.TestCase):

    def test_tag_and_key_are_both_scoped_by_the_run(self):
        qid = "9a4b1c2d"
        a = MeshMemoryClient(question_id=qid, run_nonce="aaaaaaaa")
        b = MeshMemoryClient(question_id=qid, run_nonce="bbbbbbbb")

        # Tag: the cleanup + recall handle.
        self.assertNotEqual(a.bench_tag, b.bench_tag)
        # Key: the UPSERT handle. Disjoint tags alone would still let run B's
        # `remember` land on run A's rows and replace their content.
        self.assertNotEqual(a.key_prefix, b.key_prefix)
        # And the question id is still recoverable from both.
        self.assertIn(qid, a.bench_tag)
        self.assertIn(qid, b.bench_tag)

    def test_nonce_is_stable_within_one_process(self):
        a = MeshMemoryClient(question_id="q1")
        b = MeshMemoryClient(question_id="q2")
        self.assertEqual(a.run_nonce, b.run_nonce)
        self.assertEqual(a.run_nonce, mesh_client_stdio.RUN_NONCE)

    def test_key_stays_server_valid(self):
        # `remember` validates the key against ^[a-z0-9][a-z0-9-]*[a-z0-9]$.
        # The nonce is prepended, so a folded id keeps its trailing digest and
        # `sanitize_key_component`'s two branches stay disjoint.
        c = MeshMemoryClient(question_id="gpt4_4929293a", run_nonce="deadbeef")
        key = f"{c.key_prefix}-s0"
        self.assertRegex(key, r"^[a-z0-9][a-z0-9-]*[a-z0-9]$")
        self.assertTrue(key.startswith("bench-deadbeef-"))


class TestOneRunsSweepCannotReachAnothers(unittest.TestCase):
    """The regression test proper: it fails on the pre-nonce naming."""

    def test_deep_sweep_leaves_a_concurrent_runs_rows_alone(self):
        ws = FakeWorkspace()
        qid = "9a4b1c2d"
        a = MeshMemoryClient(question_id=qid, run_nonce="aaaaaaaa")
        b = MeshMemoryClient(question_id=qid, run_nonce="bbbbbbbb")

        # Both runs are mid-flight on the SAME question — the collision case.
        ws.store("row-a", [a.bench_tag, SHARED_TAG, "session-0"])
        ws.store("row-b", [b.bench_tag, SHARED_TAG, "session-0"])

        # Run A finishes its question and sweeps by tag.
        asyncio.run(a._sweep(ws, _sem(), deep=True))

        self.assertNotIn("row-a", ws.rows, "A should have cleaned up after itself")
        self.assertIn("row-b", ws.rows,
                      "A's sweep deleted a concurrent run's haystack — "
                      "B would score a silent, indistinguishable miss")


class TestTheOrphanCollectorSelectsOnAge(unittest.TestCase):

    def setUp(self):
        mesh_client_stdio._orphan_gc_done = False
        self.addCleanup(setattr, mesh_client_stdio, "_orphan_gc_done", False)

    def test_it_deletes_abandoned_rows_but_spares_a_live_peer(self):
        ws = FakeWorkspace()
        me = MeshMemoryClient(question_id="q", run_nonce="aaaaaaaa")

        ws.store("abandoned", ["bench-old-q", SHARED_TAG], created_at=_hours_ago(9))
        # A peer run started minutes ago. It is NOT mine — and deleting it is
        # precisely the bug. Age is what tells the two apart.
        ws.store("live-peer", ["bench-bbbbbbbb-q", SHARED_TAG], created_at=_hours_ago(0.2))

        asyncio.run(me._gc_orphans(ws, _sem()))

        self.assertNotIn("abandoned", ws.rows)
        self.assertIn("live-peer", ws.rows,
                      "the collector deleted a concurrently running peer's "
                      "fixtures — the original defect, with a wider blast radius")

    def test_an_unreadable_timestamp_is_never_treated_as_old(self):
        ws = FakeWorkspace()
        me = MeshMemoryClient(question_id="q", run_nonce="aaaaaaaa")
        ws.store("undated", ["bench-x-q", SHARED_TAG], created_at="not-a-date")
        ws.rows["missing"] = {"id": "missing", "tags": [SHARED_TAG]}  # no created_at

        asyncio.run(me._gc_orphans(ws, _sem()))

        self.assertEqual(ws.forgotten, [],
                         "an unparseable date must refuse deletion, not read as epoch 0")

    def test_it_runs_once_per_process(self):
        ws = FakeWorkspace()
        me = MeshMemoryClient(question_id="q", run_nonce="aaaaaaaa")
        ws.store("abandoned", ["bench-old-q", SHARED_TAG], created_at=_hours_ago(9))

        asyncio.run(me._gc_orphans(ws, _sem()))
        ws.store("abandoned2", ["bench-old-q", SHARED_TAG], created_at=_hours_ago(9))
        asyncio.run(me._gc_orphans(ws, _sem()))

        self.assertIn("abandoned2", ws.rows,
                      "the collector is a once-per-process workspace pass, "
                      "not per-question work")

    def test_a_failing_collector_never_costs_the_run_a_measurement(self):
        class Broken:
            async def call_tool(self, *_a, **_kw):
                raise RuntimeError("recall exploded")

        me = MeshMemoryClient(question_id="q", run_nonce="aaaaaaaa")
        asyncio.run(me._gc_orphans(Broken(), _sem()))  # must not raise


class TestTheNonceOverrideFailsSafe(unittest.TestCase):

    def test_a_malformed_override_falls_back_to_a_fresh_namespace(self):
        for bad in ("has spaces", "-leading", "UPPER!", "x" * 40):
            with mock.patch.dict("os.environ", {"BENCH_RUN_NONCE": bad}):
                got = mesh_client_stdio._resolve_run_nonce()
            self.assertRegex(got, r"^[a-z0-9]+$")
            self.assertNotEqual(got, bad)
            # The point: it must not degrade to "no nonce at all", which is the
            # shared namespace this change exists to abolish.
            self.assertTrue(got)

    def test_a_valid_override_is_honoured(self):
        with mock.patch.dict("os.environ", {"BENCH_RUN_NONCE": "ci-30255818672"}):
            self.assertEqual(mesh_client_stdio._resolve_run_nonce(), "ci-30255818672")


if __name__ == "__main__":
    unittest.main(verbosity=2)
