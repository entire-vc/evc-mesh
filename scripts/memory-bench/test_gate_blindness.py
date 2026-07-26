#!/usr/bin/env python3
"""Self-checks for the ways the recall gate could stop enforcing anything.

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
  4. SILENT TOOL ERROR — a `call_tool` result with `isError=True` (or any non-JSON
     body a healthy mesh-mcp tool never produces) was parsed as `{"text": ...}`,
     a shape neither `_store` nor `_search` recognise as an error. A real recall
     failure then reads as "zero items, mode unknown" with no exception and no log
     line. (evc-mesh#352: one such call collapsed a 24-question run's mode to
     "unknown", turning a real -0.5 regression into mere INCONCLUSIVE gate
     blindness — worse than #1-3, because it hides a REGRESSION, not just an
     infra hiccup.)
  5. UNSTORABLE QUESTION — the memory `key` is built from the question id, and
     Mesh validates keys against `^[a-z0-9][a-z0-9-]*[a-z0-9]$`. The two
     `gpt4_*` ids in the dataset carry an `_`, so their first `remember` was
     rejected 400 and the question never ran. Both are `temporal-reasoning`, so
     that category was scored 2/4 in every run for 6 days and post-#361 the gate
     correctly refused to score it at all. Nothing was red; a seventh of the
     safety net simply did not exist. The guard is over the WHOLE dataset, so the
     next dataset refresh cannot quietly reintroduce it.
  6. MISREPORTED CAUSE — that 400 was raised inside anyio's task groups, and the
     transport teardown's own `BrokenResourceError` replaced it on the way out.
     Six days of logs named the plumbing; none named the key. A cause that is
     knowable and then discarded is how a one-line fix stays unfound.
  7. FORGIVEN FOR EVER — the retry budget a question spent is recorded here, so a
     failure that looks transient but exhausted every attempt can be told apart
     from a blip that recovered. `--max-error-rate` forgives 10% of a run on the
     premise that those questions get measured next run; a deterministic failure
     breaks that premise and is forgiven permanently. (The classification and the
     budget itself live in run_ci.py — see test_gate_modes.py.)
"""

from __future__ import annotations

import asyncio
import json
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


class TestTheRetryBudgetSpentIsRecorded(_Harness):
    """The gate classifies a transient-LOOKING failure by whether retrying ever
    helped, so the client has to say how much of its allowance it actually spent.
    Without this, four consecutive "Connection closed" deaths are indistinguishable
    from one blip that recovered — and the permanent case is the one that hides.
    """

    def test_a_question_that_burned_every_attempt_says_so(self):
        client = mc.MeshMemoryClient(question_id="down")
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=closed):
            with self.assertRaises(BaseException):
                self._call(client)
        self.assertEqual(mc.CONNECT_RETRIES, client.attempts_allowed)
        self.assertEqual(client.attempts_allowed, client.attempts_made)

    def test_a_question_that_recovered_did_not_burn_them_all(self):
        client = mc.MeshMemoryClient(question_id="blip")
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=[closed, ["hit"]]):
            self._call(client)
        self.assertLess(client.attempts_made, client.attempts_allowed)

    def test_an_open_breaker_leaves_an_allowance_of_one(self):
        """`attempts_made == attempts_allowed` must not read as "retrying did not
        help" when the breaker had already withdrawn the retries. One attempt out
        of one proves nothing, and the gate's classifier keys on exactly this."""
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=closed):
            for _ in range(mc.BREAKER_TRIP_AFTER):
                with self.assertRaises(BaseException):
                    self._call(mc.MeshMemoryClient(question_id="down"))
            after = mc.MeshMemoryClient(question_id="after")
            with self.assertRaises(BaseException):
                self._call(after)
        self.assertEqual(1, after.attempts_allowed)
        self.assertEqual(1, after.attempts_made)


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


class _ToolBlock:
    def __init__(self, text):
        self.text = text


class _ToolResult:
    """Minimal stand-in for `mcp.types.CallToolResult` (content + isError)."""

    def __init__(self, *, text=None, is_error=False):
        self.content = [_ToolBlock(text)] if text is not None else []
        self.isError = is_error


class TestSilentToolErrorIsSurfaced(unittest.TestCase):
    """evc-mesh#352: an `isError` (or non-JSON) tool result must become an
    `{"error": ...}` payload — the only shape `_store`/`_search` treat as a
    failure — never the old `{"text": ...}` that both silently ignore."""

    def test_isError_with_text_becomes_an_error_payload(self):
        result = _ToolResult(text="recall failed: workspace not found", is_error=True)
        payload = mc._parse_tool_payload(result)
        self.assertEqual(
            {"error": "recall failed: workspace not found"}, payload
        )

    def test_isError_with_no_text_still_reports_an_error(self):
        result = _ToolResult(is_error=True)
        payload = mc._parse_tool_payload(result)
        self.assertIn("error", payload)

    def test_non_json_body_without_isError_is_still_an_error(self):
        # No healthy mesh-mcp tool ever answers success with a non-JSON body —
        # a body that fails to parse is corruption/truncation, not "empty".
        result = _ToolResult(text="<html>502 Bad Gateway</html>", is_error=False)
        payload = mc._parse_tool_payload(result)
        self.assertIn("error", payload)
        self.assertNotIn("text", payload, "the old silent-swallow shape must be gone")

    def test_valid_json_dict_on_success_passes_through_unchanged(self):
        result = _ToolResult(
            text='{"items": [], "search_mode": "bm25-only", "degraded": true}'
        )
        payload = mc._parse_tool_payload(result)
        self.assertEqual(
            {"items": [], "search_mode": "bm25-only", "degraded": True}, payload
        )

    def test_valid_json_list_on_success_is_wrapped_as_items(self):
        result = _ToolResult(text='[{"id": "m1"}]')
        payload = mc._parse_tool_payload(result)
        self.assertEqual({"items": [{"id": "m1"}]}, payload)

    def test_a_plain_dict_result_passes_through_as_is(self):
        # _FakeSession (and the real MCP low-level client, in some paths) can
        # hand back an already-decoded dict; must not be re-parsed or rejected.
        self.assertEqual({"items": []}, mc._parse_tool_payload({"items": []}))

    def test_none_result_is_empty_not_an_error(self):
        self.assertEqual({}, mc._parse_tool_payload(None))

    def test_search_raises_on_the_error_payload_it_would_now_receive(self):
        """Integration guard: _search already gates on `.get("error")` — confirm
        the NEW error shape actually trips that gate (the old `{"text": ...}`
        shape did not, which is the whole bug)."""
        client = mc.MeshMemoryClient(question_id="q1")

        class _ErrorSession:
            async def call_tool(self, name, args):
                return _ToolResult(text="recall failed: 500", is_error=True)

        with self.assertRaises(RuntimeError):
            asyncio.run(client._search(_ErrorSession(), "query", 10))


DATASET = Path(__file__).resolve().parent / "data" / "lme_s_24.json"


class TestEveryQuestionCanBeStored(unittest.TestCase):
    """Every question in the dataset must produce keys Mesh will accept.

    Asserted over the whole dataset rather than over the two ids that were
    actually broken: the bug was not "these two ids are unusual", it was "nothing
    checked". A refresh that pulls in `gpt4_*`-style ids again fails here instead
    of silently deleting a category from the gate's coverage.
    """

    @classmethod
    def setUpClass(cls):
        cls.entries = json.loads(DATASET.read_text(encoding="utf-8"))
        assert isinstance(cls.entries, list) and cls.entries, "dataset is empty"

    def test_the_dataset_still_contains_the_ids_that_broke_it(self):
        """Pin the fixture the other tests depend on. If a refresh drops both
        `gpt4_*` ids, the checks below still pass while testing nothing."""
        ids = {e["question_id"] for e in self.entries}
        self.assertTrue(
            {"gpt4_4929293a", "gpt4_7f6b06db"} & ids,
            "no underscore-bearing id left in the dataset — this suite no longer "
            "exercises the sanitizer on a real failing case",
        )

    def test_every_generated_key_is_valid_and_unique(self):
        seen: dict[str, str] = {}
        for entry in self.entries:
            qid = entry["question_id"]
            client = mc.MeshMemoryClient(question_id=qid)
            n = len(entry.get("haystack_dates") or [])
            self.assertGreater(n, 0, f"{qid}: no haystack sessions to key")
            for idx in range(n):
                key = f"{client.key_prefix}-s{idx}"
                self.assertRegex(
                    key,
                    mc.MESH_KEY_RE,
                    f"{qid}: key Mesh would reject with 400",
                )
                # `remember` UPSERTs on the key, so a duplicate does not error —
                # it overwrites another question's haystack and both questions are
                # then scored against half their evidence.
                self.assertNotIn(
                    key, seen, f"key collision between {seen.get(key)} and {qid}"
                )
                seen[key] = qid
        self.assertEqual(
            len(self.entries), len({e["question_id"] for e in self.entries})
        )

    def test_the_tag_keeps_the_raw_id(self):
        """Only the key is sanitized. The tag is the recall filter and the cleanup
        handle, and nothing validates it, so the `_` must survive there verbatim.

        Asserted as a prefix, not an equality: the tag also carries the run nonce
        that keeps concurrent runs from deleting each other's fixtures. Pinning
        the whole string would make this test fail for the nonce — which is
        present on purpose — instead of for the sanitizing it exists to forbid.
        """
        client = mc.MeshMemoryClient(question_id="gpt4_4929293a", nonce="n1")
        self.assertEqual("bench-gpt4_4929293a-n1", client.bench_tag)
        self.assertEqual("bench-gpt4-4929293a-4581bcc5-n1", client.key_prefix)

    def test_an_already_safe_id_is_passed_through_untouched(self):
        """22 of 24 ids need nothing done to them, and their keys must not churn:
        an unnecessary rename orphans rows a previous run is still cleaning up."""
        self.assertEqual("184da446", mc.sanitize_key_component("184da446"))

    def test_ids_that_differ_only_in_a_separator_do_not_collide(self):
        """The sanitizer is lossy, and `remember` UPSERTs — so lossiness is not a
        cosmetic concern here, it silently merges two questions' fixtures."""
        self.assertNotEqual(
            mc.sanitize_key_component("gpt4_4929293a"),
            mc.sanitize_key_component("gpt4-4929293a"),
        )

    def test_a_degenerate_id_still_yields_a_valid_key(self):
        for raw in ("___", "-", "", "A_B", "a--b", "_lead", "trail_"):
            with self.subTest(raw=raw):
                self.assertRegex(
                    f"bench-{mc.sanitize_key_component(raw)}-s0", mc.MESH_KEY_RE
                )


class TestToolErrorSurvivesTheTransportTeardown(unittest.TestCase):
    """The reported cause must be the tool's rejection, not its unwind artifact.

    Drives the real `_run` through a transport whose teardown raises, which is
    what anyio does when the task groups are cancelled out from under it. A test
    that mocked `asyncio.run` would pass with the fix fully reverted.
    """

    VALIDATION = (
        "Bad Request: Validation failed (key: key must match pattern "
        "^[a-z0-9][a-z0-9-]*[a-z0-9]$)"
    )

    class _BrokenResourceError(Exception):
        """Stands in for `anyio.BrokenResourceError`."""

    def setUp(self):
        mc.MeshMemoryClient._exhausted_questions = 0
        self.addCleanup(setattr, mc.MeshMemoryClient, "_exhausted_questions", 0)
        patcher = mock.patch.object(mc.time, "sleep")
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)

    def _install(self, *, teardown_raises: bool):
        import contextlib
        import types

        broken = self._BrokenResourceError

        @contextlib.asynccontextmanager
        async def _stdio_client(_params):
            try:
                yield (None, None)
            finally:
                if teardown_raises:
                    # Cancelling the transport out from under an in-flight call
                    # raises here, DURING the unwind of the original exception —
                    # and this one replaces it.
                    raise broken("the pipe is gone")

        rejecting = self

        class _Session:
            async def __aenter__(self):
                return self

            async def __aexit__(self, *_exc):
                return False

            async def initialize(self):
                return None

            async def call_tool(self, name, args):
                if name == "remember":
                    return _ToolResult(text=rejecting.VALIDATION, is_error=True)
                return {}

        mcp_mod = types.ModuleType("mcp")
        mcp_mod.ClientSession = lambda _r, _w: _Session()
        mcp_mod.StdioServerParameters = lambda **kw: kw
        stdio_mod = types.ModuleType("mcp.client.stdio")
        stdio_mod.stdio_client = _stdio_client
        client_pkg = types.ModuleType("mcp.client")
        client_pkg.stdio = stdio_mod
        patcher = mock.patch.dict(
            sys.modules,
            {"mcp": mcp_mod, "mcp.client": client_pkg, "mcp.client.stdio": stdio_mod},
        )
        patcher.start()
        self.addCleanup(patcher.stop)

    def _call(self, client):
        return client.ingest_and_search(
            sessions=[[{"role": "user", "content": "x"}]],
            dates=["2023/05/10 (Wed) 01:57"],
            format_session_text=lambda turns, date: "session text",
            query="q",
            top_k=10,
        )

    def test_the_validation_message_is_what_gets_reported(self):
        self._install(teardown_raises=True)
        client = mc.MeshMemoryClient(question_id="gpt4_4929293a")
        with self.assertRaises(BaseException) as caught:
            self._call(client)
        reported = mc.flatten_exc(caught.exception)
        self.assertIn("key must match pattern", reported)
        self.assertNotEqual(
            "BrokenResourceError: the pipe is gone",
            reported,
            "the teardown artifact replaced the cause again",
        )

    def test_the_teardown_artifact_is_kept_as_context_not_dropped(self):
        """Preferring the tool error must not hide the transport failure outright —
        a run where BOTH happened is diagnosed from both halves."""
        self._install(teardown_raises=True)
        with self.assertRaises(BaseException) as caught:
            self._call(mc.MeshMemoryClient(question_id="gpt4_4929293a"))
        self.assertIn("BrokenResourceError", mc.flatten_exc(caught.exception))

    def test_a_permanent_rejection_is_not_retried(self):
        """A 400 will be a 400 four times over. Paying ~50s of backoff per
        question turns a clear failure into a timed-out job."""
        self._install(teardown_raises=True)
        with self.assertRaises(BaseException):
            self._call(mc.MeshMemoryClient(question_id="gpt4_4929293a"))
        self.sleep.assert_not_called()

    def test_a_clean_propagation_is_reported_once(self):
        """No teardown artifact: the RuntimeError already carries the message, so
        it must be passed through rather than re-wrapped around itself."""
        self._install(teardown_raises=False)
        with self.assertRaises(RuntimeError) as caught:
            self._call(mc.MeshMemoryClient(question_id="gpt4_4929293a"))
        self.assertEqual(
            1,
            mc.flatten_exc(caught.exception).count("key must match pattern"),
        )

    def test_a_transport_failure_with_no_tool_error_is_untouched(self):
        """The promotion must not manufacture a cause it does not have."""
        client = mc.MeshMemoryClient(question_id="q1")
        boom = RuntimeError("Connection closed")
        self.assertIs(boom, client._surfaced(boom))


# ---------------------------------------------------------------------------
# Concurrent runs must not delete each other's fixtures.
# ---------------------------------------------------------------------------


class _UpsertStore:
    """Server-side rows as Mesh actually keeps them: identified by memory `key`.

    `remember` UPSERTs on the key, so two processes storing the same key are
    handed back the SAME row id — which is exactly how one run's `forget(id)`
    reached into another run's live haystack. `_FakeSession` above deliberately
    mints a fresh id per store (it is testing orphan recovery, where reusing an
    id would hide the leak); that fake therefore *cannot* reproduce this bug, so
    this one models the upsert instead.
    """

    def __init__(self):
        self.rows: dict[str, dict] = {}   # memory_id -> {key, tags}
        self.by_key: dict[str, str] = {}  # key -> memory_id
        self.seq = 0

    def remember(self, key: str, tags: list[str]) -> str:
        mid = self.by_key.get(key)
        if mid is None:
            self.seq += 1
            mid = f"mem-{self.seq}"
            self.by_key[key] = mid
        self.rows[mid] = {"key": key, "tags": list(tags)}
        return mid

    def forget(self, mid: str) -> None:
        row = self.rows.pop(mid, None)
        if row is not None:
            self.by_key.pop(row["key"], None)

    def recall(self, tags_any: list[str]) -> list[dict]:
        want = set(tags_any)
        return [
            {"id": mid, "key": row["key"], "tags": row["tags"], "score": 1.0}
            for mid, row in sorted(self.rows.items())
            if want & set(row["tags"])
        ]


class _SharedSession:
    """An MCP session onto one `_UpsertStore` — i.e. onto the one workspace that
    a single MESH_BENCH_KEY resolves to, whoever is connecting."""

    def __init__(self, store: _UpsertStore):
        self.store = store

    async def call_tool(self, name, args):
        if name == "remember":
            return {
                "memory": {
                    "id": self.store.remember(args["key"], args["tags"]),
                    "key": args["key"],
                }
            }
        if name == "forget":
            self.store.forget(args["memory_id"])
            return {}
        if name == "recall":
            return {
                "items": self.store.recall(args.get("tags_any") or []),
                "search_mode": "hybrid",
            }
        raise AssertionError(f"unexpected tool {name}")


class TestConcurrentRunsDoNotDeleteEachOther(unittest.TestCase):
    """Replay of 2026-07-26: run A's cleanup deleting run B's live haystack.

    Every fixture name used to be a pure function of the question id, so two
    bench processes wrote the same keys and the same tags into the same
    workspace. A miss manufactured that way is indistinguishable from a real
    recall failure — and on the required arm it publishes REGRESSION for a PR
    that changed nothing. Worse, it corrupts a *baseline*: the re-snap of
    `baseline_retrieval.json` (run 30204433963) started at 13:37:57Z, inside the
    window of two other live bench jobs.

    Drives the real `_store` / `_sweep` / `_search`. Two clients built with the
    process-level nonce cache cleared in between, so the nonce comes from the
    real derivation path and not from a parameter the test chose.
    """

    QID = "gpt4_4929293a"
    N = 3

    def setUp(self):
        self.addCleanup(setattr, mc, "_RUN_NONCE", None)

    def _as_separate_process(self) -> mc.MeshMemoryClient:
        mc._RUN_NONCE = None          # a different process derives a fresh nonce
        return mc.MeshMemoryClient(question_id=self.QID)

    @staticmethod
    def _ingest(client, session):
        async def go():
            sem = asyncio.Semaphore(1)
            async with sem:
                for idx in range(TestConcurrentRunsDoNotDeleteEachOther.N):
                    mid = await client._store(session, "haystack text", idx, "2026-01-01")
                    if mid:
                        client._pending.append(mid)
        asyncio.run(go())

    @staticmethod
    def _sweep(client, session, *, deep: bool):
        asyncio.run(client._sweep(session, asyncio.Semaphore(1), deep=deep))

    @staticmethod
    def _search(client, session):
        return asyncio.run(client._search(session, "query", 10))

    def test_two_runs_of_one_question_do_not_share_fixture_names(self):
        a, b = self._as_separate_process(), self._as_separate_process()
        self.assertNotEqual(
            a.bench_tag, b.bench_tag,
            "the TAG is what the deep sweep deletes by — sharing it means one "
            "run's sweep collects every other run's live rows for this question",
        )
        self.assertNotEqual(
            a.key_prefix, b.key_prefix,
            "the KEY is what `remember` upserts on — sharing it means both runs "
            "are handed the same row id, so a `forget` by id crosses runs",
        )
        # Still the same question, still purgeable by the umbrella tag.
        self.assertIn(self.QID, a.bench_tag)
        self.assertIn(self.QID, b.bench_tag)

    def test_a_finishing_run_does_not_delete_a_live_runs_haystack(self):
        """The shallow sweep — by memory id, in the `finally` of every question."""
        store = _UpsertStore()
        session = _SharedSession(store)
        a, b = self._as_separate_process(), self._as_separate_process()

        self._ingest(a, session)
        self._ingest(b, session)
        self.assertEqual(2 * self.N, len(store.rows), "the upsert collapsed two runs into one set of rows")

        self._sweep(a, session, deep=False)     # A finishes this question

        hits = self._search(b, session)
        self.assertEqual(
            self.N, len(hits),
            "B's haystack was deleted by A's cleanup — B now scores a miss on "
            "evidence that was in the store a second ago",
        )
        self.assertEqual([], a._pending, "A must still have cleaned up after itself")

    def test_a_tag_sweep_does_not_reach_into_another_run(self):
        """The deep sweep — by `tags_any`, run when a previous attempt died.

        This is the worse half: it deletes rows it never stored, so it reaches
        even the sessions whose ids the other run never handed out.
        """
        store = _UpsertStore()
        session = _SharedSession(store)
        a, b = self._as_separate_process(), self._as_separate_process()

        self._ingest(a, session)
        self._ingest(b, session)

        a._dirty = True
        self._sweep(a, session, deep=True)

        self.assertEqual(
            self.N, len(self._search(b, session)),
            "A's tag sweep collected B's live fixtures",
        )
        self.assertEqual(
            [], store.recall([a.bench_tag]), "A's own rows survived its deep sweep"
        )

    def test_the_umbrella_tag_still_reaches_every_run(self):
        """Nonce-ing the per-question tag must not cost the cross-run cleanup
        handle: `lme-bench` is what an out-of-band orphan purge greps for."""
        store = _UpsertStore()
        session = _SharedSession(store)
        for _ in range(2):
            self._ingest(self._as_separate_process(), session)
        self.assertEqual(2 * self.N, len(store.recall([mc.SHARED_TAG])))


class TestTheRunNonce(unittest.TestCase):
    def setUp(self):
        self.addCleanup(setattr, mc, "_RUN_NONCE", None)
        mc._RUN_NONCE = None

    def _derive(self, **env):
        """`None` means "this variable is absent", which patch.dict cannot express."""
        with mock.patch.dict(
            mc.os.environ, {k: v for k, v in env.items() if v is not None}, clear=False
        ):
            for k, v in env.items():
                if v is None:
                    mc.os.environ.pop(k, None)
            return mc._derive_run_nonce()

    def test_it_is_cached_for_the_life_of_the_process(self):
        """All 24 questions of one run must share a nonce, or a run's footprint
        stops being greppable and purgeable as a unit."""
        self.assertEqual(mc.run_nonce(), mc.run_nonce())

    def test_two_jobs_of_the_same_workflow_run_still_differ(self):
        """The decisive case, and the reason GITHUB_RUN_ID alone is not enough:
        the two arms of ONE workflow run execute in parallel against one
        MESH_BENCH_KEY. On 2026-07-26 run 30202732563 had `Memory recall gate`
        (12:46:40Z→13:12:27Z) live alongside `LongMemEval-S end-to-end`
        (12:46:42Z→13:50Z+) — 26 minutes of overlap inside a single run id.

        The same fact is why a workflow-level `concurrency:` group cannot fix
        this: both jobs belong to the same group.
        """
        env = {"GITHUB_RUN_ID": "30202732563", "BENCH_RUN_NONCE": ""}
        self.assertNotEqual(self._derive(**env), self._derive(**env))

    def test_it_does_not_degrade_to_a_constant_when_ci_vars_are_absent(self):
        """Uniqueness must not depend on the environment setting anything: a
        nonce that silently collapses when a var is missing fails OPEN into
        precisely the bug it was added to prevent."""
        env = {"GITHUB_RUN_ID": None, "GITHUB_JOB": None, "BENCH_RUN_NONCE": None}
        self.assertNotEqual(self._derive(**env), self._derive(**env))

    def test_a_hostile_nonce_still_yields_a_key_mesh_accepts(self):
        """`BENCH_RUN_NONCE` is an operator-supplied escape hatch, so it is
        untrusted input on the one field the server validates. A rejected key is
        not a loud failure here — it is how a question goes quietly unmeasured.

        Built WITHOUT the `nonce=` parameter on purpose: passing it would route
        through `__init__`'s own sanitize call and the test would still pass with
        the derivation trusting the environment verbatim.
        """
        for raw in ("Run_2026/07/26", "  ", "___", "-x-", "ПРОГОН", "a" * 80):
            with self.subTest(raw=raw):
                with mock.patch.dict(mc.os.environ, {"BENCH_RUN_NONCE": raw}):
                    mc._RUN_NONCE = None
                    client = mc.MeshMemoryClient(question_id="gpt4_4929293a")
                self.assertRegex(f"{client.key_prefix}-s0", mc.MESH_KEY_RE)
                self.assertRegex(f"bench-{client.run_nonce}", mc.MESH_KEY_RE)

    def test_an_explicit_nonce_is_honoured_so_fixtures_can_be_re_reached(self):
        """Pinning the nonce is how an operator re-attaches to a previous run's
        rows to purge them by tag. Deliberately not collision-proof."""
        self.assertEqual(self._derive(BENCH_RUN_NONCE="purge-1"), "purge-1")

    def test_the_nonce_cannot_move_the_session_index(self):
        """`run_ci.retrieved_session_indices` reads the key's TRAILING `-s<idx>`.
        A nonce that could shift that mapping would silently mis-attribute hits
        to the wrong haystack session — a scoring error, not a crash."""
        sys.path.insert(0, str(Path(__file__).resolve().parent))
        import run_ci  # noqa: PLC0415

        client = mc.MeshMemoryClient(question_id="gpt4_4929293a", nonce="s9-s4")
        key = f"{client.key_prefix}-s7"
        self.assertEqual({7}, run_ci.retrieved_session_indices([{"key": key, "tags": []}]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
