#!/usr/bin/env python3
"""Pin the two CALL SITES of `corpus_sanitize`, not just the module itself.

    python scripts/memory-bench/test_corpus_sanitize_wiring.py

`test_corpus_sanitize.py` (14 tests) proves `normalise` /
`assert_only_distractors_touched` do the right thing when called directly. It
proves nothing about whether production code actually calls them. On
`db3fe337` two mutations to the WIRING — not the module — both left that
14/14 green:

  * **M1** — delete `content = normalised.text` in
    `mesh_client_stdio.MeshMemoryClient._store`. `normalise()` still runs and
    still logs; only the assignment goes, so the ORIGINAL (unsanitised)
    content reaches `remember`. Silent: nothing in the 14 tests reads what
    `_store` actually sends over the wire.
  * **M2** — replace `corpus_sanitize.assert_only_distractors_touched(dataset,
    format_session_text)` in `run_ci.cmd_run` with `touched = []`. The
    pre-flight audit simply never runs. Silent TODAY because 0 of the 9
    affected sessions are gold (proven twice, in the module's own docstring
    and in `assert_only_distractors_touched`'s) — but the guarantee the call
    exists for ("corpus refresh cannot silently bias the gold set") evaporates
    with it, and nothing turns red to say so.

This is `test_gate_blindness.py`'s class of bug one floor down: a self-check
can be present, correct, AND wired into the required job, and the production
call it is meant to guard can still not exist. Both tests below are written so
that deleting the call they pin — not just weakening it — is what goes red:

  * `TestStoreSendsNormalisedContent` captures the literal `content` argument
    `_store` hands to `session.call_tool("remember", ...)` and requires it to
    equal `corpus_sanitize.normalise(original).text`, not merely "differ from
    the original" (M1 still computes `normalised` — a weaker assertion could
    pass by accident) and not merely "normalise was called" (M1 does call it;
    only the assignment is missing).
  * `TestPreflightRunsBeforeIngest` replaces
    `corpus_sanitize.assert_only_distractors_touched` with a recorder that (a)
    raises a private sentinel exception and (b) records the exact `formatter`
    argument it received. It then drives the REAL `run_ci.main() -> cmd_run()`
    path (argparse and all — see `test_gate_modes.py`'s `_ArmHarness`, same
    idiom) with `run_ci.run_single` replaced by a second recorder standing in
    for "ingest happened". Un-mutated code: the sentinel propagates out of
    `main()` before `run_single` is ever called, and the formatter received is
    `run_ci.format_session_text` BY IDENTITY (a lookalike copy would not do —
    `corpus_sanitize.session_text` exists for exactly that: a same-shaped but
    NOT-identical fallback, and the whole point of the injection is that the
    audit scans the bytes the write path actually sends). M2: the recorder is
    never invoked at all, the loop runs, `run_single` fires — red on both the
    "sentinel raised" and the "ingest never touched" assertions.

unittest rather than pytest, matching the sibling self-checks: the required
job runs these as `python <file>`, nothing installed but the interpreter.
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

import corpus_sanitize  # noqa: E402
import mesh_client_stdio as mc  # noqa: E402
import run_ci  # noqa: E402

# A zero-width space — one of the two classes `normalise()` rewrites (the
# other, the instruction-override phrase, is exercised in test_corpus_sanitize.py
# already; one disallowed-invisible case here is enough to prove the WIRING,
# which is the only thing this file is responsible for).
_DIRTY_CONTENT = "secret​data"


class TestStoreSendsNormalisedContent(unittest.TestCase):
    """`_store` must hand `remember` the NORMALISED text, not the original.

    Constructs a real `MeshMemoryClient` (its `__init__` does no I/O — no
    network, no env lookup that can fail) so `self.qid` / `self.key_prefix` /
    `self.bench_tag` are the real values `_store` reads, then fakes only the
    stdio `session.call_tool`.
    """

    def setUp(self):
        # _log_store_id appends a line to this path on every successful store.
        # Redirect it into tmp so the test does not write into a real
        # operator's ~/bench/store_ids.jsonl.
        self.tmp = Path(tempfile.mkdtemp())
        patcher = mock.patch.object(mc, "BENCH_IDS_LOG", str(self.tmp / "store_ids.jsonl"))
        patcher.start()
        self.addCleanup(patcher.stop)

    def _run_store(self, content: str) -> tuple[dict, dict]:
        """Return (call_args, result) for one `_store(...)` call.

        `call_args` is the exact `{"key":..., "content":..., ...}` dict handed
        to `session.call_tool("remember", ...)`.
        """
        client = mc.MeshMemoryClient(question_id="wiring-q1")
        calls: list[tuple[str, dict]] = []

        class FakeSession:
            async def call_tool(self, name, args):
                calls.append((name, dict(args)))
                return {"memory": {"id": "mem-1", "key": args.get("key", "")}}

        import asyncio

        asyncio.run(client._store(FakeSession(), content, 0, ""))
        self.assertEqual(len(calls), 1, "expected exactly one remember call")
        name, args = calls[0]
        self.assertEqual(name, "remember")
        return args, {}

    def test_sent_content_equals_normalise_output(self):
        args, _ = self._run_store(_DIRTY_CONTENT)
        expected = corpus_sanitize.normalise(_DIRTY_CONTENT).text
        # Sanity on the fixture itself: if this ever stopped differing from the
        # original, the test below would pass vacuously (M1 would go
        # unnoticed because there would be nothing to un-normalise).
        self.assertNotEqual(expected, _DIRTY_CONTENT, "fixture must actually be dirty")
        self.assertEqual(
            args["content"], expected,
            "_store sent something other than corpus_sanitize.normalise(content).text — "
            "M1 (dropping `content = normalised.text`) would produce exactly this failure",
        )

    def test_sent_content_is_not_the_raw_original(self):
        # Belt-and-braces on the same defect from the other direction: even
        # without recomputing the expected value, the RAW dirty string must
        # never be what crosses the wire.
        args, _ = self._run_store(_DIRTY_CONTENT)
        self.assertNotEqual(
            args["content"], _DIRTY_CONTENT,
            "_store sent the unnormalised original to remember",
        )

    def test_clean_content_passes_through_unchanged(self):
        # Negative control: normalise() is a no-op on clean text, so a store of
        # clean content must send that content byte-for-byte. Without this, a
        # "fix" that always rewrites content (e.g. re-encoding it) would also
        # satisfy the two tests above.
        clean = "ordinary session text, nothing to rewrite"
        args, _ = self._run_store(clean)
        self.assertEqual(args["content"], clean)


class TestPreflightRunsBeforeIngest(unittest.TestCase):
    """`cmd_run` must call `assert_only_distractors_touched` BEFORE any
    per-question ingest, with the real `format_session_text`.

    Drives the actual `run_ci.main() -> cmd_run()` path (argparse included),
    same idiom as `test_gate_modes.py`'s `_ArmHarness`: the defect this pins is
    about WHERE a call sits in `cmd_run`, and a unit test of a helper in
    isolation would not see it move.
    """

    class _Sentinel(Exception):
        """Raised by the faked pre-flight audit; must reach the caller of
        `main()` untouched — `cmd_run` has no try/except around this call, and
        the test would be proving nothing if it silently swallowed a
        different exception instead."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        dataset = [
            {
                "question_id": "q1",
                "question_type": "single-session-user",
                "question": "irrelevant",
                "haystack_sessions": [],
                "haystack_dates": [],
                "haystack_session_ids": [],
                "answer_session_ids": [],
                "question_date": "",
            }
        ]
        self.dataset = dataset
        data_file = self.tmp / "data.json"
        data_file.write_text(json.dumps(dataset))

        # Every sink cmd_run can WRITE to, redirected into tmp — mirrors
        # test_gate_modes.py's _ArmHarness. Under the un-mutated code the
        # sentinel aborts the run long before any of these are touched; under
        # M2 the run proceeds to completion, and without this redirection it
        # would write test fixtures into the real scripts/memory-bench/results/.
        for p, attr in (
            (self.tmp / "baseline.json", "BASELINE_FILE"),
            (self.tmp / "baseline_retrieval.json", "RETRIEVAL_BASELINE_FILE"),
            (self.tmp / "baseline_retrieval_branch.json", "BRANCH_RETRIEVAL_BASELINE_FILE"),
            (self.tmp / "results", "RESULTS_DIR"),
            (self.tmp / "results" / "recall_gate.json", "RETRIEVAL_RESULTS_FILE"),
            (self.tmp / "results" / "longmemeval.json", "E2E_RESULTS_FILE"),
        ):
            patcher = mock.patch.object(run_ci, attr, p)
            patcher.start()
            self.addCleanup(patcher.stop)

        for patcher in (
            mock.patch.object(run_ci, "DATA_FILE", data_file),
            mock.patch.dict(
                os.environ,
                {"MESH_API_URL": "http://mesh.test", "MESH_AGENT_KEY": "k"},
            ),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def _run(self, *argv: str) -> int:
        with mock.patch.object(sys, "argv", ["run_ci.py", *argv]):
            return run_ci.main()

    def test_preflight_precedes_ingest_and_uses_the_real_formatter(self):
        preflight_calls: list[tuple[list, object]] = []
        ingest_calls: list[dict] = []

        def fake_preflight(dataset_arg, formatter=None):
            preflight_calls.append((dataset_arg, formatter))
            raise self._Sentinel("preflight recorder tripped")

        def fake_run_single(entry, **kwargs):
            ingest_calls.append(entry)
            return {
                "question_id": entry["question_id"],
                "question_type": entry["question_type"],
                "correct": True,
                "search_mode": "hybrid",
            }

        with mock.patch.object(
            corpus_sanitize, "assert_only_distractors_touched", side_effect=fake_preflight
        ), mock.patch.object(run_ci, "run_single", side_effect=fake_run_single):
            with self.assertRaises(
                self._Sentinel,
                msg=(
                    "cmd_run did not call assert_only_distractors_touched at all — "
                    "this is exactly what M2 (replacing the call with `touched = []`) "
                    "produces"
                ),
            ):
                self._run("--retrieval-only")

        self.assertEqual(
            len(preflight_calls), 1,
            "assert_only_distractors_touched must be called exactly once",
        )
        dataset_arg, formatter = preflight_calls[0]
        self.assertEqual(dataset_arg, self.dataset)
        self.assertIs(
            formatter, run_ci.format_session_text,
            "must pass the REAL run_ci.format_session_text by identity — the write "
            "path's own formatter, not a lookalike (corpus_sanitize.session_text is "
            "deliberately non-equivalent; see its docstring)",
        )
        self.assertEqual(
            ingest_calls, [],
            "run_single (ingest) was called — the pre-flight audit did not run "
            "BEFORE the ingest loop the way it must",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
