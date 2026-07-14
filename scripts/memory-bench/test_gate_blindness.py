#!/usr/bin/env python3
"""Self-checks for the three ways the recall gate could stop enforcing anything.

    python scripts/memory-bench/test_gate_blindness.py

The gate is a REQUIRED check, which makes silence its most dangerous output. A
regression here does not turn CI red — it turns CI green while measuring nothing,
and the "required" badge then certifies a run that never happened. Each test below
pins one way that has actually occurred:

  1. PATH COVERAGE — a memory file missing from MEMORY_PATHS means the gate
     no-ops to green on a PR that changed memory. (#347 rewrote the authz on
     memory DELETE and the gate never ran.)
  2. TRANSIENT RESTART — a push to main runs this bench *and* the backend deploy
     concurrently, so mesh-api restarts underneath the run. Un-retried, every
     question in that window errors and the gate reports INCONCLUSIVE: the safety
     net switches itself off exactly on the commits that changed memory.
  3. LEGIBLE CAUSE — an anyio TaskGroup reports failures as "unhandled errors in
     a TaskGroup (1 sub-exception)". If that is the string the gate prints when
     it goes blind, nobody can tell why it went blind.
"""

from __future__ import annotations

import re
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mesh_client_stdio as mc  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github/workflows/memory-bench.yml"


def _memory_paths() -> list[str]:
    """The MEMORY_PATHS block from the workflow, as the gate's scope step reads it."""
    text = WORKFLOW.read_text(encoding="utf-8")
    block = re.search(r"^  MEMORY_PATHS: \|\n((?:    .*\n)+)", text, re.M)
    assert block, "MEMORY_PATHS block not found in memory-bench.yml"
    return [ln.strip() for ln in block.group(1).splitlines() if ln.strip()]


class TestMemoryPathCoverage(unittest.TestCase):
    """Every memory source file must be in the gate's scope.

    Self-maintaining on purpose: a NEW memory file added tomorrow fails this test
    until it is listed, rather than silently widening the blind spot. The gate's
    scope step prefix-matches, so a directory entry covers everything under it.

    KNOWN BOUNDARY, stated rather than papered over: this fences the code the
    bench can actually detect a regression in — the remember / recall / forget
    path. Other files consume MemoryService without matching the name heuristic
    (`cmd/api/main.go`, `internal/handler/canonical_updates_handler.go`,
    `internal/service/event_bus_service.go`, `internal/service/interfaces.go`).
    They are deliberately NOT gated: the bench never calls `ListMemories`, so
    gating them would spend 16 minutes of CI to measure nothing — and a gate that
    is slow and pointless is one people switch off, which costs more than it
    protects. If the bench ever exercises those paths, gate them then.
    """

    def test_every_memory_source_file_is_gated(self):
        paths = _memory_paths()
        uncovered = []
        for f in REPO_ROOT.rglob("*.go"):
            if any(part in {"vendor", "node_modules", ".git"} for part in f.parts):
                continue
            if f.name.endswith("_test.go") or "memor" not in f.name.lower():
                continue
            rel = f.relative_to(REPO_ROOT).as_posix()
            if not any(rel.startswith(p) for p in paths):
                uncovered.append(rel)
        self.assertEqual(
            [], sorted(uncovered),
            "These memory files are NOT in MEMORY_PATHS, so a PR touching only them "
            "would report the required recall gate as GREEN without running it. "
            "Add them to MEMORY_PATHS in .github/workflows/memory-bench.yml.",
        )

    def test_the_handler_that_slipped_through_is_gated(self):
        # Regression pin for #347 specifically: memory DELETE authorization.
        self.assertIn("internal/handler/memory_handler.go", _memory_paths())


class TestFlattenExc(unittest.TestCase):
    def test_taskgroup_wrapper_is_unwrapped_to_the_real_cause(self):
        inner = RuntimeError("Connection closed")
        group = ExceptionGroup("unhandled errors in a TaskGroup", [inner])
        out = mc.flatten_exc(group)
        self.assertIn("Connection closed", out)
        self.assertNotIn("sub-exception", out)
        self.assertNotIn("TaskGroup", out)

    def test_nested_groups_are_flattened(self):
        deep = ExceptionGroup("outer", [ExceptionGroup("inner", [ValueError("boom")])])
        self.assertIn("boom", mc.flatten_exc(deep))

    def test_plain_exception_is_named(self):
        self.assertEqual("ValueError: nope", mc.flatten_exc(ValueError("nope")))


class TestTransientDetection(unittest.TestCase):
    def test_restart_symptoms_are_transient(self):
        # The literal strings mesh-mcp / the MCP client actually emit on a restart.
        for msg in ("Connection closed", "Bad Gateway", "API error 502"):
            self.assertTrue(mc._is_transient(RuntimeError(msg)), msg)

    def test_transient_leaf_inside_a_group_is_seen(self):
        group = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        self.assertTrue(mc._is_transient(group))

    def test_a_harness_bug_is_not_transient(self):
        # Must NOT be retried: retrying a real bug just burns the job's clock.
        self.assertFalse(mc._is_transient(KeyError("haystack_sessions")))

    def test_incidental_digits_are_not_mistaken_for_a_gateway_error(self):
        # The fixtures carry "502"/"503"/"504" as ordinary digits >130 times, and
        # ids are hex. A loose substring match would read a REAL bug as a restart:
        # retry it, delay the run, then report it under a cause that never was.
        for msg in (
            "ValueError: expected 502 tokens, got 41",
            "KeyError: 'bench-9f504a2c-s3'",
            "RuntimeError: remember failed: quota 503 of 1000 rows",
        ):
            self.assertFalse(mc._is_transient(RuntimeError(msg)), msg)

    def test_real_gateway_errors_are_still_caught(self):
        for msg in (
            "Agent authentication failed: Bad Gateway: API error 502",
            "http 503 service unavailable",
            "status: 504 gateway timeout",
        ):
            self.assertTrue(mc._is_transient(RuntimeError(msg)), msg)


class _Harness(unittest.TestCase):
    def setUp(self):
        mc.MeshMemoryClient._exhausted_questions = 0
        # Keep the tests fast: the backoff itself is not what's under test.
        patcher = mock.patch.object(mc.time, "sleep")
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)
        # `_run` is a coroutine function; stub it so the tests never build a
        # coroutine object that the mocked asyncio.run would leave un-awaited.
        # MagicMock, not the default AsyncMock: patching a coroutine function
        # would hand asyncio.run a coroutine nobody awaits.
        run_patcher = mock.patch.object(
            mc.MeshMemoryClient, "_run", new=mock.MagicMock(return_value=None)
        )
        run_patcher.start()
        self.addCleanup(run_patcher.stop)
        self.addCleanup(setattr, mc.MeshMemoryClient, "_exhausted_questions", 0)

    def _call(self, client):
        return client.ingest_and_search(
            sessions=[], dates=[], format_session_text=lambda t, date: "", query="q", top_k=10
        )


class TestRetryRidesOutARestart(_Harness):
    def test_question_recovers_when_the_api_comes_back(self):
        client = mc.MeshMemoryClient(question_id="q1")
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(
            mc.asyncio, "run", side_effect=[closed, closed, ["hit"]]
        ) as run:
            self.assertEqual(["hit"], self._call(client))
        self.assertEqual(3, run.call_count)

    def test_a_nontransient_failure_is_not_retried(self):
        client = mc.MeshMemoryClient(question_id="q1")
        with mock.patch.object(mc.asyncio, "run", side_effect=KeyError("bug")) as run:
            with self.assertRaises(KeyError):
                self._call(client)
        self.assertEqual(1, run.call_count)

    def test_breaker_stops_paying_backoff_once_the_api_is_really_down(self):
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=closed) as run:
            for _ in range(mc.BREAKER_TRIP_AFTER):
                with self.assertRaises(BaseException):
                    self._call(mc.MeshMemoryClient(question_id="down"))
            tripped = run.call_count
            # Breaker is open now: further questions fail fast, one attempt each.
            with self.assertRaises(BaseException):
                self._call(mc.MeshMemoryClient(question_id="after"))
            self.assertEqual(tripped + 1, run.call_count)

    def test_a_recovery_rearms_the_breaker(self):
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=[closed, ["hit"]]):
            self._call(mc.MeshMemoryClient(question_id="blip"))
        self.assertEqual(0, mc.MeshMemoryClient._exhausted_questions)


class _FakeSession:
    """A scripted stand-in for an MCP ClientSession.

    `die_after_stores`: the connection drops once this many `remember` calls have
    landed — i.e. the rows EXIST in the store, but every later call on this
    session (including the `forget`s that would clean them up) raises. That is
    the mesh-api-restarts-mid-ingest case, and the whole reason cleanup cannot
    live on the connection that failed.
    """

    seq = 0

    def __init__(
        self,
        log: list[tuple[str, str]],
        die_after_stores: int | None,
        store: set[str] | None = None,
    ):
        self.log = log
        self.die_after_stores = die_after_stores
        # The server-side rows, shared across sessions — a restart does not empty
        # the database, which is the entire point.
        self.store = store if store is not None else set()
        self.stores = 0
        self.dead = False

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_exc):
        return False

    async def initialize(self):
        if self.dead:
            raise RuntimeError("Connection closed")

    async def call_tool(self, name, args):
        if self.dead:
            raise RuntimeError("Connection closed")
        if name == "remember":
            self.stores += 1
            # A FRESH id per store, as Mesh does. Deriving the id from the key
            # would let a re-store of the same session collide with — and so
            # silently reclaim — the row a previous attempt orphaned, hiding the
            # very leak these tests exist to catch.
            _FakeSession.seq += 1
            mid = f"mem-{_FakeSession.seq}"
            self.store.add(mid)          # the row is COMMITTED server-side
            self.log.append(("remember", mid))
            if self.die_after_stores is not None and self.stores >= self.die_after_stores:
                self.dead = True
                # Committed, THEN the pipe dropped. The row is live and its id
                # never reaches the client — unreachable by id, findable by tag.
                raise RuntimeError("Connection closed")
            return {"memory": {"id": mid, "key": args["key"]}}
        if name == "forget":
            self.store.discard(args["memory_id"])
            self.log.append(("forget", args["memory_id"]))
            return {}
        if name == "recall":
            self.log.append(("recall", ""))
            # The tag sweep: return whatever this question actually left behind.
            return {
                "items": [{"id": mid, "tags": []} for mid in sorted(self.store)],
                "search_mode": "bm25-only",
            }
        raise AssertionError(f"unexpected tool {name}")


class TestCleanupSurvivesTheConnectionDying(unittest.TestCase):
    """The retry must not leak the fixtures of the attempt it is retrying.

    Cleanup used to run its deletes down the same connection whose death caused
    the failure, swallowing the errors — so an attempt that died mid-store left
    its haystack behind. `_pending` lives on the CLIENT, not the connection, so
    the next attempt's fresh session finishes the job.

    Drives the real `_run`/`_sweep` through a fake MCP transport. Mocking
    `asyncio.run` instead would make this test pass with the fix reverted.
    """

    def setUp(self):
        mc.MeshMemoryClient._exhausted_questions = 0
        self.addCleanup(setattr, mc.MeshMemoryClient, "_exhausted_questions", 0)
        patcher = mock.patch.object(mc.time, "sleep")
        patcher.start()
        self.addCleanup(patcher.stop)

    def _install_transport(self, sessions: list[_FakeSession]):
        """Inject fake `mcp` modules; `_run` imports them at call time."""
        import contextlib
        import types

        @contextlib.asynccontextmanager
        async def _stdio_client(_params):
            yield (None, None)

        it = iter(sessions)
        mcp_mod = types.ModuleType("mcp")
        mcp_mod.ClientSession = lambda _r, _w: next(it)
        mcp_mod.StdioServerParameters = lambda **kw: kw
        stdio_mod = types.ModuleType("mcp.client.stdio")
        stdio_mod.stdio_client = _stdio_client
        client_mod = types.ModuleType("mcp.client")

        for name, mod in [
            ("mcp", mcp_mod), ("mcp.client", client_mod), ("mcp.client.stdio", stdio_mod),
        ]:
            p = mock.patch.dict(sys.modules, {name: mod})
            p.start()
            self.addCleanup(p.stop)

    def _ingest(self, client, n_sessions=3):
        return client.ingest_and_search(
            sessions=[[{"role": "user", "content": "x"}]] * n_sessions,
            dates=["2026-01-01"] * n_sessions,
            format_session_text=lambda turns, date: "text",
            query="q",
            top_k=10,
        )

    def test_rows_orphaned_by_a_dead_connection_are_deleted_by_the_retry(self):
        log: list[tuple[str, str]] = []
        db: set[str] = set()   # survives the "restart", as a real database does
        dying = _FakeSession(log, die_after_stores=2, store=db)
        healthy = _FakeSession(log, die_after_stores=None, store=db)
        self._install_transport([dying, healthy])

        client = mc.MeshMemoryClient(question_id="q1")
        self._ingest(client)  # attempt 1 dies mid-ingest; attempt 2 must clean up after it

        self.assertTrue(
            any(op == "remember" for op, _ in log), "the fake must commit rows to orphan"
        )
        self.assertEqual(
            set(), db,
            "fixtures committed by the attempt that died are still in the store — "
            "the bench leaked its haystack, which is what poisons real agents' recall()",
        )
        self.assertEqual([], client._pending)

    def test_the_row_whose_id_never_came_back_is_still_reachable(self):
        # The killer case: `remember` COMMITS, then the pipe drops before the id
        # reaches us. Nothing in `_pending` refers to that row. Only the tag does.
        log: list[tuple[str, str]] = []
        db: set[str] = set()
        self._install_transport([
            _FakeSession(log, die_after_stores=1, store=db),   # dies on its 1st store
            _FakeSession(log, die_after_stores=None, store=db),
        ])
        client = mc.MeshMemoryClient(question_id="q1")
        self._ingest(client)
        self.assertEqual(
            set(), db, "a row whose id we never received was left behind forever"
        )

    def test_a_leak_that_survives_every_retry_is_not_silent(self):
        log: list[tuple[str, str]] = []
        db: set[str] = set()
        # Every session dies immediately: cleanup never gets a live connection.
        self._install_transport(
            [_FakeSession(log, die_after_stores=1, store=db) for _ in range(4)]
        )
        client = mc.MeshMemoryClient(question_id="q1")
        with self.assertLogs(mc.logger, level="ERROR") as logs:
            with self.assertRaises(BaseException):
                self._ingest(client)
        self.assertTrue(
            any("ORPHANED FIXTURES" in m for m in logs.output),
            "fixtures left in a shared store must never be abandoned quietly",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
