#!/usr/bin/env python3
"""Self-check for the mode-aware recall gate's verdict logic.

The repo has no pytest convention for scripts/, so this is stdlib `unittest`
and runs with no dependencies at all:

    python scripts/memory-bench/test_gate_modes.py     # or: python -m unittest

The functions under test are deliberately pure (no Mesh, no network): the whole
point of the mode gate is that its decision is obviously correct by inspection.

The property that matters: a mode mismatch NEVER produces EXIT_REGRESSION.
Exit 1 blocks the merge. If a dead embedder could produce exit 1, then the day
prod's embedder runs out of credit, every PR in the repo goes red for a fault no
author can fix — the check gets bypassed and the safety net is gone for good.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import run_ci  # noqa: E402
from run_ci import (  # noqa: E402
    EXIT_INCONCLUSIVE,
    MIN_TOLERANCE,
    EXIT_OK,
    EXIT_REGRESSION,
    MODE_BM25_ONLY,
    MODE_HYBRID,
    MODE_UNKNOWN,
    INELIGIBLE_SAMPLE,
    INELIGIBLE_SPREAD,
    Baseline,
    build_baseline_payload,
    capture_blockers,
    category_comparable,
    category_sample_sizes,
    classify,
    decide_verdict,
    effective_tolerance,
    load_baseline,
    modes_comparable,
    resolve_run_search_mode,
)


# Module-scoped belt for the results artifact. Several classes in this file drive
# the real `main()`, and only some of them own a tmp dir — `TestTheGate...` runs
# against the REAL dataset with no file patches at all. A per-harness patch list
# isolates exactly the harnesses that remembered to add the newest sink, which is
# how a test fixture ends up in a shipped artifact: the recall job runs these
# self-checks BEFORE the gate and then uploads scripts/memory-bench/results/, and
# it only ever runs `--retrieval-only`, so nothing overwrites a longmemeval.json
# left behind by a stub. Redirecting for the whole module makes that structurally
# impossible rather than a rule each future harness has to remember.
_RESULTS_TMP: tempfile.TemporaryDirectory | None = None
_RESULTS_PATCHERS: list = []


def setUpModule() -> None:
    global _RESULTS_TMP
    _RESULTS_TMP = tempfile.TemporaryDirectory(prefix="gate-results-")
    root = Path(_RESULTS_TMP.name)
    for attr, value in (
        ("RESULTS_DIR", root),
        ("RETRIEVAL_RESULTS_FILE", root / "recall_gate.json"),
        ("E2E_RESULTS_FILE", root / "longmemeval.json"),
    ):
        patcher = mock.patch.object(run_ci, attr, value)
        patcher.start()
        _RESULTS_PATCHERS.append(patcher)


def tearDownModule() -> None:
    for patcher in reversed(_RESULTS_PATCHERS):
        patcher.stop()
    _RESULTS_PATCHERS.clear()
    if _RESULTS_TMP is not None:
        _RESULTS_TMP.cleanup()


class TestTheSelfChecksCannotWriteIntoTheUploadedArtifact(unittest.TestCase):
    """Guards the belt above, not any one harness.

    If this fails, some test in this file is writing stub fixtures into the
    directory CI uploads as `recall-gate-results`.
    """

    def test_every_results_path_points_outside_the_repo(self):
        repo_results = (Path(run_ci.__file__).resolve().parent / "results").resolve()
        for attr in ("RESULTS_DIR", "RETRIEVAL_RESULTS_FILE", "E2E_RESULTS_FILE"):
            target = Path(getattr(run_ci, attr)).resolve()
            self.assertFalse(
                target == repo_results or repo_results in target.parents,
                f"{attr} still points into the uploaded results/ dir: {target}",
            )


class TestResolveRunSearchMode(unittest.TestCase):
    def test_all_hybrid(self):
        results = [{"search_mode": MODE_HYBRID}, {"search_mode": MODE_HYBRID}]
        self.assertEqual(resolve_run_search_mode(results), MODE_HYBRID)

    def test_one_degraded_question_degrades_the_run(self):
        # Even a single bm25-only recall moves the aggregate score, so the run as
        # a whole is not comparable to a hybrid baseline.
        results = [{"search_mode": MODE_HYBRID}, {"search_mode": MODE_BM25_ONLY}]
        self.assertEqual(resolve_run_search_mode(results), MODE_BM25_ONLY)

    def test_unknown_dominates(self):
        results = [{"search_mode": MODE_HYBRID}, {"search_mode": MODE_UNKNOWN}]
        self.assertEqual(resolve_run_search_mode(results), MODE_UNKNOWN)

    def test_old_server_reports_nothing(self):
        # No `search_mode` key at all (Mesh without this PR deployed).
        self.assertEqual(resolve_run_search_mode([{"correct": True}]), MODE_UNKNOWN)

    def test_no_results_at_all(self):
        self.assertEqual(resolve_run_search_mode([]), MODE_UNKNOWN)

    def test_unrecognised_mode_string_is_unknown(self):
        self.assertEqual(
            resolve_run_search_mode([{"search_mode": "vector-only"}]), MODE_UNKNOWN
        )


class TestModesComparable(unittest.TestCase):
    def test_same_known_mode_is_comparable(self):
        self.assertTrue(modes_comparable(MODE_HYBRID, MODE_HYBRID))
        self.assertTrue(modes_comparable(MODE_BM25_ONLY, MODE_BM25_ONLY))

    def test_cross_mode_is_not_comparable(self):
        # THE wedge scenario: baseline snapped with a live embedder, embedder
        # dies, every PR now serves bm25-only and scores lower.
        self.assertFalse(modes_comparable(MODE_HYBRID, MODE_BM25_ONLY))
        self.assertFalse(modes_comparable(MODE_BM25_ONLY, MODE_HYBRID))

    def test_unknown_is_never_comparable(self):
        self.assertFalse(modes_comparable(MODE_UNKNOWN, MODE_HYBRID))
        self.assertFalse(modes_comparable(MODE_HYBRID, MODE_UNKNOWN))
        self.assertFalse(modes_comparable(MODE_UNKNOWN, MODE_UNKNOWN))

    def test_a_cross_mode_drop_can_never_block_a_merge(self):
        # Restated as the invariant the gate must hold: whenever the modes differ,
        # the verdict path taken is INCONCLUSIVE, so EXIT_REGRESSION is unreachable.
        for baseline, run in [
            (MODE_HYBRID, MODE_BM25_ONLY),
            (MODE_BM25_ONLY, MODE_HYBRID),
            (MODE_UNKNOWN, MODE_BM25_ONLY),
            (MODE_HYBRID, MODE_UNKNOWN),
        ]:
            with self.subTest(baseline=baseline, run=run):
                self.assertFalse(modes_comparable(baseline, run))
                verdict = 2 if not modes_comparable(baseline, run) else EXIT_REGRESSION
                self.assertNotEqual(verdict, EXIT_REGRESSION)


class TestLoadBaseline(unittest.TestCase):
    def _write(self, payload) -> Path:
        tmp = Path(tempfile.mkdtemp()) / "baseline_retrieval.json"
        tmp.write_text(json.dumps(payload))
        return tmp

    def test_mode_scoped_schema(self):
        path = self._write(
            {
                "search_mode": MODE_HYBRID,
                "captured_at": "2026-07-13T00:00:00Z",
                "top_k": 10,
                "n_runs": 3,
                "scores": {"single-session-user": 0.75, "overall": 0.83},
                "spread": {"single-session-user": 0.25, "overall": 0.09},
            }
        )
        b = load_baseline(path)
        self.assertEqual(b.search_mode, MODE_HYBRID)
        self.assertEqual(b.scores["overall"], 0.83)
        self.assertEqual(b.n_runs, 3)
        self.assertEqual(b.spread["single-session-user"], 0.25)

    def test_legacy_flat_schema_has_no_mode(self):
        # Backward compat: a pre-mode baseline is UNKNOWN, so it yields
        # INCONCLUSIVE rather than a coin-flip verdict.
        path = self._write({"single-session-user": 0.75, "overall": 0.83})
        b = load_baseline(path)
        self.assertEqual(b.search_mode, MODE_UNKNOWN)
        self.assertEqual(b.scores["overall"], 0.83)
        self.assertFalse(modes_comparable(b.search_mode, MODE_HYBRID))

    def test_mode_scoped_schema_without_spread_is_read_as_one_sample(self):
        # A baseline written before `n_runs`/`spread` existed must not be credited
        # with a precision it never measured.
        path = self._write(
            {"search_mode": MODE_HYBRID, "top_k": 10, "scores": {"overall": 0.83}}
        )
        b = load_baseline(path)
        self.assertEqual(b.n_runs, 1)
        self.assertEqual(b.spread, {})


class TestBuildBaselinePayload(unittest.TestCase):
    """Both arms must write a baseline their own mode gate can accept.

    THE BUG this pins: `--update-baseline` built the mode-scoped payload only
    inside `if retrieval_only:`. The advisory arm's `else` wrote a bare flat
    `json.dumps(scores)`, so re-snapping baseline.json produced another mode-less
    file that `load_baseline` reads as UNKNOWN. Once the mode gate applies to that
    arm too, such a baseline can NEVER be comparable — the arm would be pinned at
    INCONCLUSIVE for ever, silently, because it is not a required check.
    """

    def test_payload_round_trips_into_a_comparable_baseline(self):
        payload = build_baseline_payload([{"overall": 0.5}], MODE_HYBRID, top_k=10)
        tmp = Path(tempfile.mkdtemp()) / "baseline.json"
        tmp.write_text(json.dumps(payload))
        b = load_baseline(tmp)
        self.assertEqual(b.search_mode, MODE_HYBRID)
        self.assertTrue(
            modes_comparable(b.search_mode, MODE_HYBRID),
            "a freshly snapped baseline must be comparable to a run in the same mode",
        )

    def test_mean_and_spread_over_passes(self):
        payload = build_baseline_payload(
            [
                {"multi-session": 0.5, "overall": 0.591},
                {"multi-session": 0.0, "overall": 0.500},
                {"multi-session": 0.0, "overall": 0.409},
            ],
            MODE_HYBRID,
            top_k=10,
        )
        self.assertEqual(payload["n_runs"], 3)
        self.assertAlmostEqual(payload["scores"]["multi-session"], 0.5 / 3)
        self.assertAlmostEqual(payload["spread"]["multi-session"], 0.5)
        self.assertAlmostEqual(payload["spread"]["overall"], 0.182, places=3)

    def test_a_single_pass_records_zero_spread_and_says_n_runs_1(self):
        payload = build_baseline_payload([{"overall": 0.667}], MODE_HYBRID, top_k=10)
        self.assertEqual(payload["n_runs"], 1)
        self.assertEqual(payload["spread"]["overall"], 0.0)

    def test_a_degraded_capture_records_the_degraded_mode(self):
        payload = build_baseline_payload([{"overall": 0.4}], MODE_BM25_ONLY, top_k=10)
        self.assertEqual(payload["search_mode"], MODE_BM25_ONLY)


class TestEffectiveToleranceAndEligibility(unittest.TestCase):
    """A verdict may not be finer-grained than the measurement's own noise.

    Observed on four consecutive nightlies of IDENTICAL code (artifacts of runs
    29807366999 / 29897108364 / 30147341465 / 30191444472):
    `single-session-assistant` = 1.000, 1.000, 0.250, 1.000 and `multi-session` =
    0.500, 0.000, 0.000, 0.000. With 4 questions per category one flipped answer
    is 0.25 — exactly the default tolerance — so the arm reported `✗ REGRESS` on
    judge nondeterminism roughly every other night.
    """

    def test_spread_widens_the_threshold_but_tolerance_is_the_floor(self):
        b = Baseline({"a": 1.0, "b": 1.0}, MODE_HYBRID, 3, {"a": 0.75, "b": 0.05})
        self.assertEqual(effective_tolerance("a", 0.25, b), 0.75)
        self.assertEqual(effective_tolerance("b", 0.25, b), 0.25)

    def test_a_missing_spread_falls_back_to_tolerance(self):
        b = Baseline({"a": 1.0}, MODE_HYBRID, 1, {})
        self.assertEqual(effective_tolerance("a", 0.25, b), 0.25)

    def test_noise_within_the_observed_spread_is_not_a_regression(self):
        b = Baseline({"single-session-assistant": 0.812}, MODE_HYBRID, 4, {"single-session-assistant": 0.75})
        (v,) = classify({"single-session-assistant": 0.25}, b, 0.25)
        self.assertTrue(v.eligible)
        self.assertFalse(v.regressed, "0.250 was observed with no code change; it is noise")

    def test_a_drop_past_the_observed_spread_still_regresses(self):
        b = Baseline({"single-session-assistant": 0.812}, MODE_HYBRID, 4, {"single-session-assistant": 0.75})
        (v,) = classify({"single-session-assistant": 0.0}, b, 0.25)
        self.assertTrue(v.regressed, "below anything ever observed — that is a real signal")

    def test_a_category_that_cannot_fail_is_not_reported_as_passing(self):
        # multi-session: mean 0.125, spread 0.500 → threshold -0.375. No score can
        # go under it, so the category has no verdict to give. A ✓ here would be
        # manufactured coverage.
        b = Baseline({"multi-session": 0.125}, MODE_HYBRID, 4, {"multi-session": 0.5})
        (v,) = classify({"multi-session": 0.0}, b, 0.25)
        self.assertFalse(v.eligible)
        self.assertFalse(v.regressed)


class TestMissingCredentialsAreInconclusive(unittest.TestCase):
    """A missing credential must never be reportable as a memory regression.

    `_require_env` used to `sys.exit(1)`, which is EXIT_REGRESSION. Since the
    recall gate is a REQUIRED check, that made a PR with no access to the Mesh
    secrets (any PR from a fork) fail with "this PR makes memory worse" — a red
    check its author could never clear. Cannot-measure is exit 2, always.
    """

    def test_missing_env_exits_inconclusive_not_regression(self):
        for key in ("MESH_API_URL", "MESH_AGENT_KEY"):
            with self.subTest(key=key), mock.patch.dict(os.environ, {key: ""}):
                with self.assertRaises(SystemExit) as cm:
                    run_ci._require_env(key)
                self.assertEqual(cm.exception.code, run_ci.EXIT_INCONCLUSIVE)
                self.assertNotEqual(cm.exception.code, run_ci.EXIT_REGRESSION)


class _ArmHarness(unittest.TestCase):
    """Drive the real `main()` → `cmd_run()` verdict path for either arm.

    Deliberately end-to-end through argparse and the whole verdict chain rather
    than calling the pure helpers: the defect being pinned was not in a helper,
    it was in WHICH ARM the caller applied the helpers to. A test of
    `modes_comparable` alone stays green with the bug fully present.
    """

    CATEGORIES = ["knowledge-update", "multi-session"]

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        dataset = [
            {"question_id": f"q{i}", "question_type": cat}
            for cat in self.CATEGORIES
            for i in range(4)
        ]
        data_file = self.tmp / "data.json"
        data_file.write_text(json.dumps(dataset))
        self.dataset = dataset

        # Every file main() WRITES has to be redirected into tmp, not just the
        # baselines it reads. The results artifact is a new output sink, and a
        # sink missing from this list writes test fixtures into the real
        # scripts/memory-bench/results/ — which CI then uploads as
        # `recall-gate-results`. The recall job runs these self-checks BEFORE
        # the gate and only ever runs `--retrieval-only`, so nothing overwrites
        # longmemeval.json: the artifact would ship `q0 knowledge-update
        # overall 1.000` from _stub_answers, indistinguishable from a real
        # advisory run to anyone who downloads it.
        for p, attr in (
            (self.tmp / "baseline.json", "BASELINE_FILE"),
            (self.tmp / "baseline_retrieval.json", "RETRIEVAL_BASELINE_FILE"),
            # New sink (ADR-0003). Listed here for the reason the comment above
            # gives, not because a test in THIS file writes it today: the next
            # `--arm branch` case added here would otherwise drop a stub
            # baseline into the repo, where it becomes a merge threshold.
            (self.tmp / "baseline_retrieval_branch.json", "BRANCH_RETRIEVAL_BASELINE_FILE"),
            (self.tmp / "results", "RESULTS_DIR"),
            (self.tmp / "results" / "recall_gate.json", "RETRIEVAL_RESULTS_FILE"),
            (self.tmp / "results" / "longmemeval.json", "E2E_RESULTS_FILE"),
        ):
            patcher = mock.patch.object(run_ci, attr, p)
            patcher.start()
            self.addCleanup(patcher.stop)
            setattr(self, attr, p)

        for patcher in (
            mock.patch.object(run_ci, "DATA_FILE", data_file),
            mock.patch.dict(
                os.environ,
                {"MESH_API_URL": "http://mesh.test", "MESH_AGENT_KEY": "k", "LME_JUDGE_API_KEY": "k"},
            ),
            # The advisory arm constructs OpenAI clients; nothing is called on them
            # because run_single is stubbed.
            mock.patch.dict(sys.modules, {"openai": mock.MagicMock()}),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    QUESTIONS_PER_CATEGORY = 4

    def _stub_answers(self, correct_by_category: dict[str, int], search_mode: str):
        """Return `correct_by_category[cat]` of the 4 questions in each category.

        The counter wraps every 4 questions so the stub is stable across repeated
        `_run` calls and across `--repeat` passes — otherwise the second run scores
        0 everywhere and every test looks like a regression.
        """
        seen: dict[str, int] = {}

        def fake_run_single(entry, **kwargs):
            cat = entry["question_type"]
            n = seen.get(cat, 0) % self.QUESTIONS_PER_CATEGORY
            seen[cat] = seen.get(cat, 0) + 1
            return {
                "question_id": entry["question_id"],
                "question_type": cat,
                "correct": n < correct_by_category.get(cat, 0),
                "search_mode": search_mode,
            }

        patcher = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        patcher.start()
        self.addCleanup(patcher.stop)

    def _run(self, *argv: str) -> int:
        with mock.patch.object(sys, "argv", ["run_ci.py", *argv]):
            return run_ci.main()

    def _write_baseline(self, arm_file: Path, payload: dict) -> None:
        arm_file.write_text(json.dumps(payload))


class TestBothArmsGetTheModeGate(_ArmHarness):
    """THE defect: the mode gate was `if retrieval_only and not modes_comparable(...)`.

    So the advisory arm compared a `hybrid` run against the legacy mode-less
    baseline.json — which reads back as UNKNOWN — and published `✗ REGRESSION` for
    the difference. Nightly run 30191444472 (2026-07-26) printed
    `overall 0.409 vs 0.667 -0.258 ✗ REGRESS` while the recall gate on the SAME
    commit reported retrieval at 0.909 vs 0.864 PASS.
    """

    LEGACY_FLAT_BASELINE = {"knowledge-update": 0.75, "multi-session": 0.75, "overall": 0.75}

    def test_advisory_arm_against_a_legacy_baseline_is_inconclusive_not_regression(self):
        self._write_baseline(self.BASELINE_FILE, self.LEGACY_FLAT_BASELINE)
        self._stub_answers({"knowledge-update": 1, "multi-session": 0}, MODE_HYBRID)
        rc = self._run("--tolerance", "0.25")
        self.assertEqual(
            rc,
            EXIT_INCONCLUSIVE,
            "a hybrid run vs an UNKNOWN-mode baseline is not comparable in EITHER arm",
        )
        self.assertNotEqual(rc, EXIT_REGRESSION)

    def test_recall_arm_against_a_legacy_baseline_is_also_inconclusive(self):
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.LEGACY_FLAT_BASELINE)
        self._stub_answers({"knowledge-update": 1, "multi-session": 0}, MODE_HYBRID)
        self.assertEqual(self._run("--retrieval-only"), EXIT_INCONCLUSIVE)

    def test_advisory_arm_cross_mode_is_inconclusive(self):
        self._write_baseline(
            self.BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0},
                "spread": {},
            },
        )
        self._stub_answers({"knowledge-update": 0, "multi-session": 0}, MODE_BM25_ONLY)
        rc = self._run()
        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertNotEqual(rc, EXIT_REGRESSION)

    def test_a_comparable_advisory_run_can_still_go_red(self):
        # The point of the fix is NOT to silence the arm. Same mode, same schema, a
        # drop past the recorded spread ⇒ the arm must still report a regression.
        self._write_baseline(
            self.BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0},
                "spread": {"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0},
            },
        )
        self._stub_answers({"knowledge-update": 0, "multi-session": 0}, MODE_HYBRID)
        self.assertEqual(self._run("--tolerance", "0.25"), EXIT_REGRESSION)

    def test_a_comparable_advisory_run_within_tolerance_passes(self):
        self._write_baseline(
            self.BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 0.5, "multi-session": 0.5, "overall": 0.5},
                "spread": {"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0},
            },
        )
        self._stub_answers({"knowledge-update": 2, "multi-session": 2}, MODE_HYBRID)
        self.assertEqual(self._run("--tolerance", "0.25"), EXIT_OK)


class TestNoBaselineIsNeverAVacuousGreenInEitherArm(_ArmHarness):
    """`return EXIT_INCONCLUSIVE if retrieval_only else EXIT_OK`.

    If baseline.json ever went missing, the advisory arm returned EXIT_OK — a
    plain green pass compared against nothing at all.
    """

    def test_advisory_arm_with_no_baseline_is_inconclusive(self):
        self.assertFalse(self.BASELINE_FILE.exists())
        self._stub_answers({"knowledge-update": 4, "multi-session": 4}, MODE_HYBRID)
        rc = self._run()
        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertNotEqual(rc, EXIT_OK, "a pass against nothing is not a pass")

    def test_recall_arm_with_no_baseline_is_inconclusive(self):
        self._stub_answers({"knowledge-update": 4, "multi-session": 4}, MODE_HYBRID)
        self.assertEqual(self._run("--retrieval-only"), EXIT_INCONCLUSIVE)


class TestBaselineCaptureIsModeScopedInBothArms(_ArmHarness):
    def test_advisory_update_baseline_writes_a_mode_scoped_file(self):
        self._stub_answers({"knowledge-update": 2, "multi-session": 2}, MODE_HYBRID)
        self.assertEqual(self._run("--update-baseline"), EXIT_OK)
        written = json.loads(self.BASELINE_FILE.read_text())
        self.assertEqual(written["search_mode"], MODE_HYBRID)
        self.assertIn("scores", written)
        self.assertEqual(written["n_runs"], 1)

    def test_the_freshly_written_baseline_is_accepted_by_the_gate_it_feeds(self):
        # The end-to-end property: capture, then run again, and the mode gate must
        # NOT declare the result incomparable. Without the writer fix this fails.
        self._stub_answers({"knowledge-update": 2, "multi-session": 2}, MODE_HYBRID)
        self.assertEqual(self._run("--update-baseline"), EXIT_OK)
        self.assertEqual(self._run("--tolerance", "0.25"), EXIT_OK)

    def test_repeat_records_the_mean_and_the_spread(self):
        # Two passes: 4/4 then 0/4 in knowledge-update ⇒ mean 0.5, spread 1.0.
        flip = {"n": 0}

        def fake_run_single(entry, **kwargs):
            cat = entry["question_type"]
            correct = flip["n"] < len(self.dataset)
            flip["n"] += 1
            return {
                "question_id": entry["question_id"],
                "question_type": cat,
                "correct": correct,
                "search_mode": MODE_HYBRID,
            }

        p = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        p.start()
        self.addCleanup(p.stop)
        self.assertEqual(self._run("--update-baseline", "--repeat", "2"), EXIT_OK)
        written = json.loads(self.BASELINE_FILE.read_text())
        self.assertEqual(written["n_runs"], 2)
        self.assertAlmostEqual(written["scores"]["overall"], 0.5)
        self.assertAlmostEqual(written["spread"]["overall"], 1.0)

    def test_repeat_is_rejected_on_a_verdict_run(self):
        # Averaging passes to decide a verdict would hide the very variance the
        # verdict has to account for.
        with self.assertRaises(SystemExit):
            self._run("--repeat", "3")


class TestAnArmThatCanOnlyPassIsNotGreen(_ArmHarness):
    def test_all_categories_ineligible_is_inconclusive(self):
        # Every category's recorded spread exceeds its own mean ⇒ nothing can fail
        # ⇒ nothing was enforced. That is blindness, not health.
        self._write_baseline(
            self.BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 4,
                "scores": {"knowledge-update": 0.5, "multi-session": 0.125, "overall": 0.3},
                "spread": {"knowledge-update": 0.5, "multi-session": 0.5, "overall": 0.5},
            },
        )
        self._stub_answers({"knowledge-update": 0, "multi-session": 0}, MODE_HYBRID)
        rc = self._run("--tolerance", "0.25")
        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertNotEqual(rc, EXIT_OK)

    def test_one_eligible_category_is_enough_for_a_real_verdict(self):
        self._write_baseline(
            self.BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 4,
                "scores": {"knowledge-update": 1.0, "multi-session": 0.125, "overall": 0.6},
                "spread": {"knowledge-update": 0.0, "multi-session": 0.5, "overall": 0.1},
            },
        )
        self._stub_answers({"knowledge-update": 4, "multi-session": 0}, MODE_HYBRID)
        self.assertEqual(self._run("--tolerance", "0.25"), EXIT_OK)


class TestCategorySampleSizes(unittest.TestCase):
    def test_errored_questions_shrink_ran_but_not_attempted(self):
        results = [
            {"question_type": "temporal-reasoning", "correct": True},
            {"question_type": "temporal-reasoning", "correct": False},
            {"question_type": "temporal-reasoning", "error": "ingest: boom"},
            {"question_type": "temporal-reasoning", "error": "ingest: boom"},
            {"question_type": "multi-session", "correct": True},
        ]
        sizes = category_sample_sizes(results)
        self.assertEqual(sizes["temporal-reasoning"], (2, 4))
        self.assertEqual(sizes["multi-session"], (1, 1))


class TestCategoryComparable(unittest.TestCase):
    """The tolerance is a claim about the denominator, so the denominator has
    to be checked before the tolerance is applied."""

    def test_full_sample_is_comparable(self):
        # 4 questions, tolerance 0.25: one flipped answer moves the score by
        # exactly the tolerance — which is what the tolerance was chosen to absorb.
        self.assertTrue(category_comparable(4, 0.25))

    def test_one_lost_question_breaks_the_calibration(self):
        # 1/3 = 0.333 > 0.25: a single unlucky answer now clears the tolerance
        # on its own, so the row can no longer distinguish noise from a drop.
        self.assertFalse(category_comparable(3, 0.25))
        self.assertFalse(category_comparable(2, 0.25))

    def test_zero_questions_is_never_comparable(self):
        # The nastiest case: a wiped-out category vanishes from `scores`
        # entirely, so the old regression loop never looked at it and the run
        # still printed "all categories within tolerance".
        self.assertFalse(category_comparable(0, 0.25))
        self.assertFalse(category_comparable(0, 1.0))

    def test_pooled_overall_survives_a_dropped_question(self):
        # `overall` pools all 24, so losing 2 leaves a 1/22 quantum — still far
        # inside the tolerance. The sample gate must not flag it.
        self.assertTrue(category_comparable(22, 0.25))


class TestSampleGateVerdicts(unittest.TestCase):
    """End-to-end verdict precedence, driven through the real scoring path."""

    # A single-pass baseline: no recorded spread, so every threshold falls back
    # to --tolerance and these cases isolate the SAMPLE gate from the spread gate.
    BASELINE = Baseline(
        scores={
            "temporal-reasoning": 1.0,
            "multi-session": 1.0,
            "overall": 1.0,
        },
        search_mode=MODE_HYBRID,
        n_runs=1,
        spread={},
    )

    @classmethod
    def _decide(cls, scores, sizes, baseline, tolerance=0.25):
        """Calls the REAL decision — never a copy of it.

        `classify` + `decide_verdict` are the same functions cmd_run returns
        from, so reverting the logic in run_ci.py fails these tests. A test that
        re-implemented the rule here would pass against the reverted code and
        guard nothing.
        """
        return decide_verdict(classify(scores, baseline, tolerance, sizes))

    @classmethod
    def _verdict(cls, scores, sizes, baseline, tolerance=0.25):
        return cls._decide(scores, sizes, baseline, tolerance).exit_code

    def test_todays_real_run_is_inconclusive_not_green(self):
        # Replay of scheduled run 30191444472 (2026-07-26): 2 BrokenResourceError
        # drops, BOTH in temporal-reasoning. 2/24 sits inside the 10% global error
        # budget, so the run was scored — and temporal-reasoning printed 1.000 ✓
        # measured on half its questions, under "All categories within tolerance".
        scores = {"temporal-reasoning": 1.0, "multi-session": 1.0, "overall": 0.909}
        sizes = {"temporal-reasoning": (2, 4), "multi-session": (4, 4), "overall": (22, 24)}
        self.assertEqual(
            self._verdict(scores, sizes, self.BASELINE), run_ci.EXIT_INCONCLUSIVE
        )

    def test_a_dropped_question_never_manufactures_a_regression(self):
        # The false-RED half. temporal-reasoning survives on 2 questions and both
        # miss → 0.000 vs a baseline of 1.000. Under the old code that is exit 1:
        # a required check blocking the merge because the harness dropped a
        # connection. It must be INCONCLUSIVE — nothing about the PR was measured.
        scores = {"temporal-reasoning": 0.0, "multi-session": 1.0, "overall": 0.909}
        sizes = {"temporal-reasoning": (2, 4), "multi-session": (4, 4), "overall": (22, 24)}
        rc = self._verdict(scores, sizes, self.BASELINE)
        self.assertEqual(rc, run_ci.EXIT_INCONCLUSIVE)
        self.assertNotEqual(rc, run_ci.EXIT_REGRESSION)

    def test_a_wiped_category_is_not_a_pass(self):
        # A category that lost every question drops out of `scores` altogether,
        # so iterating `scores` to find regressions can never see it. Its absence
        # has to be caught from the baseline side.
        scores = {"multi-session": 1.0, "overall": 1.0}  # no temporal-reasoning key
        sizes = {"temporal-reasoning": (0, 4), "multi-session": (4, 4), "overall": (20, 24)}
        verdict = self._decide(scores, sizes, self.BASELINE)
        self.assertEqual(verdict.exit_code, run_ci.EXIT_INCONCLUSIVE)
        self.assertIn("temporal-reasoning", verdict.unmeasured)
        # ...and it must land in the SAMPLE bucket, not be mistaken for the
        # baseline being too noisy — the two carry different reason kinds and
        # route to different alert copy.
        self.assertEqual(verdict.reason, run_ci.REASON_CATEGORY_UNMEASURED)
        self.assertNotIn("temporal-reasoning", verdict.spread_blind)

    def test_a_real_regression_still_blocks_despite_blindness_elsewhere(self):
        # Precedence guard. If INCONCLUSIVE outranked REGRESSION, one flaky
        # question anywhere in the run would suppress every merge block — a
        # cheaper way to disable the gate than editing it.
        scores = {"temporal-reasoning": 1.0, "multi-session": 0.0, "overall": 0.5}
        sizes = {"temporal-reasoning": (2, 4), "multi-session": (4, 4), "overall": (22, 24)}
        self.assertEqual(
            self._verdict(scores, sizes, self.BASELINE), run_ci.EXIT_REGRESSION
        )

    def test_clean_full_sample_still_passes(self):
        # The gate must not have become unpassable: a clean run is still green.
        scores = {"temporal-reasoning": 1.0, "multi-session": 1.0, "overall": 1.0}
        sizes = {"temporal-reasoning": (4, 4), "multi-session": (4, 4), "overall": (24, 24)}
        self.assertEqual(self._verdict(scores, sizes, self.BASELINE), run_ci.EXIT_OK)


class TestUnmeasuredCopyNamesItsCause(_ArmHarness):
    """The two causes of `unmeasured` are not interchangeable.

    "we lost questions to a harness error" is the PR author's infra problem; "the
    baseline was captured on a different denominator" is the maintainer's re-snap.
    Reported identically, whoever reads the log goes hunting the wrong fault — and
    because the workflow dedups its alert on the reason KIND, a live alert for one
    cause would suppress the arrival of the other.

    The `samples` half of this (evc-mesh#364) was superseded by `sample_sizes`
    (#363), which now carries the denominator; what survives here is the COPY and
    the reason kind, driven through the real `main()` so it is the shipped banner
    being asserted rather than a re-typed copy of it.
    """

    def _gate(self, sample_sizes: dict, correct: dict[str, int]):
        self._write_baseline(
            self.RETRIEVAL_BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0},
                "spread": {"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0},
                "sample_sizes": sample_sizes,
            },
        )
        self._stub_answers(correct, MODE_HYBRID)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = self._run("--retrieval-only")
        return rc, buf.getvalue()

    # Baseline says knowledge-update was measured on 2 questions; this run measures
    # all 4 and gets them all right. Nothing about quality changed — only the
    # denominator — so this must NOT read as a harness failure.
    MISMATCHED = {
        "knowledge-update": [2, 4],
        "multi-session": [4, 4],
        "overall": [4, 4],
    }

    def test_a_sample_mismatch_says_so_and_gets_its_own_reason_kind(self):
        rc, out = self._gate(
            self.MISMATCHED, {"knowledge-update": 4, "multi-session": 4}
        )
        self.assertEqual(run_ci.EXIT_INCONCLUSIVE, rc)
        self.assertIn("4 questions this run vs 2 in the baseline", out)
        self.assertIn("the maintainer's to clear", out)
        # Its OWN kind: folded into category-unmeasured, the workflow's dedup would
        # let a live "we lost questions" alert swallow this one, which has a
        # different owner and a different fix.
        self.assertIn(
            f"{run_ci.REASON_PREFIX} {run_ci.REASON_BASELINE_SAMPLE_MISMATCH}", out
        )

    def test_the_banner_says_what_was_still_enforced(self):
        # THE overstatement this pins: `category-unmeasured` is partial by
        # construction — a regression in every surviving category still blocks.
        # A banner that says "nothing was measured" when all but one category was
        # earns a discount, and then it understates at the moment it matters.
        _, out = self._gate(
            self.MISMATCHED, {"knowledge-update": 4, "multi-session": 4}
        )
        self.assertIn("WERE compared and enforced normally", out)
        self.assertIn("multi-session", out)
        self.assertNotIn("enforced nothing at all", out)

    def test_a_lost_question_reports_the_fraction_and_the_other_cause(self):
        # No sample_sizes in the baseline at all, and the run genuinely loses a
        # question: the OTHER cause, which must still name the harness.
        self._write_baseline(
            self.RETRIEVAL_BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0},
                "spread": {"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0},
            },
        )

        def fake_run_single(entry, **kwargs):
            # One knowledge-update question dies; the rest answer correctly.
            if entry["question_id"] == "q0" and entry["question_type"] == "knowledge-update":
                return {
                    "question_id": entry["question_id"],
                    "question_type": entry["question_type"],
                    "error": "boom",
                    "search_mode": MODE_HYBRID,
                }
            return {
                "question_id": entry["question_id"],
                "question_type": entry["question_type"],
                "correct": True,
                "search_mode": MODE_HYBRID,
            }

        patcher = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        patcher.start()
        self.addCleanup(patcher.stop)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = self._run("--retrieval-only", "--max-error-rate", "0.5")
        out = buf.getvalue()
        self.assertEqual(run_ci.EXIT_INCONCLUSIVE, rc)
        self.assertIn("3/4 questions", out)
        self.assertIn("lost to a harness error", out)
        self.assertNotIn("in the baseline", out)

    def test_a_matching_denominator_is_still_judged_normally(self):
        # The guard must not have become unpassable: same denominator, clean run.
        rc, out = self._gate(
            {"knowledge-update": [4, 4], "multi-session": [4, 4], "overall": [4, 4]},
            {"knowledge-update": 4, "multi-session": 4},
        )
        self.assertEqual(run_ci.EXIT_OK, rc)
        self.assertIn("All categories within tolerance", out)

    def test_a_baseline_without_sample_sizes_reads_as_no_constraint(self):
        # Every baseline in existence predates the field. Reading its silence as a
        # mismatch would take the required gate blind on every category at once —
        # a reader tightened past anything its writer has ever emitted.
        rc, out = self._gate({}, {"knowledge-update": 4, "multi-session": 4})
        self.assertEqual(run_ci.EXIT_OK, rc)
        self.assertNotIn("in the baseline", out)


class TestSampleIsPrinted(unittest.TestCase):
    def test_table_shows_the_denominator_and_flags_unmeasured(self):
        # The score alone is unreadable: "1.000" looks identical whether it came
        # from 4 questions or from the 2 that survived.
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_ci.print_table(
                {"temporal-reasoning": 1.0},
                Baseline(
                    scores={"temporal-reasoning": 1.0},
                    search_mode=MODE_HYBRID,
                    n_runs=1,
                    spread={},
                ),
                0.25,
                {"temporal-reasoning": (2, 4)},
            )
        out = buf.getvalue()
        self.assertIn("2/4", out)
        self.assertIn("UNMEASURED", out)


class TestPersistentErrorClassification(unittest.TestCase):
    """A deterministic failure must not be filed under a budget built for blips.

    Two questions errored with `BrokenResourceError` in 13 of 13 job executions
    over 6 days. 2/24 = 8.3% sat under `--max-error-rate 0.10`, so every run
    reported a clean verdict over 22 questions and the error line — present in
    100% of runs — read as furniture. Both were `temporal-reasoning`, a
    4-question category, so one seventh of the safety net silently did not exist.
    """

    # The exact shape the harness reported for six days: a transient-LOOKING
    # message on a question that was in fact unrunnable.
    HISTORIC = "ingest_and_search: BrokenResourceError"

    def test_a_non_transient_message_is_persistent_on_the_first_run(self):
        detail = (
            "RuntimeError: remember failed: Bad Request: Validation failed "
            "(key: key must match pattern ^[a-z0-9][a-z0-9-]*[a-z0-9]$)"
        )
        self.assertEqual(run_ci.ERROR_PERSISTENT, run_ci.classify_error(detail, 1, 4))

    def test_one_blip_that_recovered_is_transient(self):
        self.assertEqual(
            run_ci.ERROR_TRANSIENT,
            run_ci.classify_error("RuntimeError: Connection closed", 1, 4),
        )

    def test_a_transient_message_that_burned_every_retry_is_persistent(self):
        """The arm that catches a permanent failure wearing a transient message.
        Four fresh mesh-mcp processes over ~50s of backoff dying identically is
        not a restart, whatever the string says — and this is decided from ONE
        run, where comparing errored ids against the previous run cannot be."""
        self.assertEqual(
            run_ci.ERROR_PERSISTENT,
            run_ci.classify_error("RuntimeError: Connection closed", 4, 4),
        )

    def test_exhausting_a_withdrawn_allowance_proves_nothing(self):
        """Once the breaker is open every question gets a single attempt. Reading
        1-of-1 as "retrying did not help" would mark a whole run's worth of
        genuine outage questions permanent and alert on the wrong thing."""
        self.assertEqual(
            run_ci.ERROR_TRANSIENT,
            run_ci.classify_error("RuntimeError: Connection closed", 1, 1),
        )

    def test_the_predicate_is_the_clients_own(self):
        """One definition of "transient", not two. If the gate kept its own copy,
        the retry policy and the error budget would drift, and a question the
        client refuses to retry could still be forgiven as a blip."""
        import mesh_client_stdio

        with mock.patch.object(
            mesh_client_stdio, "is_transient_text", return_value=False
        ) as pred:
            self.assertEqual(
                run_ci.ERROR_PERSISTENT, run_ci.classify_error("Connection closed", 1, 4)
            )
        pred.assert_called()

    def test_the_historic_failure_is_caught_by_the_exhaustion_arm(self):
        self.assertEqual(
            run_ci.ERROR_PERSISTENT, run_ci.classify_error(self.HISTORIC, 4, 4)
        )

    def test_a_single_lost_question_is_already_inconclusive_today(self):
        """Pins the premise behind classify_error's stated limit.

        The exhaustion arm can mislabel a >50s outage as persistent. That is only
        harmless while losing one question ALREADY makes its category
        incomparable — then the mislabel changes the reason kind, not the
        verdict. Both halves of that are properties of the dataset and the
        tolerance, so both are asserted here: if a refresh gives categories more
        questions, this fails and the docstring's claim has to be revisited
        rather than quietly becoming false.
        """
        entries = json.loads(run_ci.DATA_FILE.read_text())
        sizes = {}
        for e in entries:
            sizes[e["question_type"]] = sizes.get(e["question_type"], 0) + 1
        self.assertEqual({4}, set(sizes.values()), sizes)
        self.assertTrue(run_ci.category_comparable(4, 0.25))
        self.assertFalse(run_ci.category_comparable(3, 0.25))

    def test_an_unclassified_old_result_is_not_counted(self):
        """Results written by an older run_ci carry no `error_kind`. Inferring
        one from absence would manufacture alerts out of stale artifacts."""
        self.assertEqual([], run_ci.persistent_errors([{"error": "boom"}]))


class TestPersistentErrorsAreNotForgiven(unittest.TestCase):
    def test_a_persistent_error_turns_a_green_run_inconclusive(self):
        self.assertEqual(
            run_ci.EXIT_INCONCLUSIVE, run_ci.persistent_verdict(run_ci.EXIT_OK, 1, 0)
        )

    def test_it_never_downgrades_a_regression(self):
        """The one thing this must not do. These two questions failed on every
        run for six days: if a persistent error could demote a REGRESSION, the
        required gate would have stopped blocking bad memory PRs entirely — a
        bigger hole than the one being closed."""
        self.assertEqual(
            EXIT_REGRESSION, run_ci.persistent_verdict(EXIT_REGRESSION, 2, 0)
        )

    def test_within_the_allowance_the_verdict_is_untouched(self):
        self.assertEqual(run_ci.EXIT_OK, run_ci.persistent_verdict(run_ci.EXIT_OK, 1, 1))

    def test_transient_errors_still_ride_on_the_percentage_budget(self):
        """The existing behaviour must survive: a mesh-api restart mid-run is
        exactly what the rate budget is for, and turning those runs INCONCLUSIVE
        would switch the safety net off on the commits that changed memory."""
        errors = [
            {"question_id": f"q{i}", "error": "boom", "error_kind": run_ci.ERROR_TRANSIENT}
            for i in range(2)
        ]
        self.assertEqual([], run_ci.persistent_errors(errors))
        self.assertEqual(
            run_ci.EXIT_OK,
            run_ci.persistent_verdict(run_ci.EXIT_OK, len(run_ci.persistent_errors(errors)), 0),
        )

    def test_the_report_names_them_and_says_they_will_recur(self):
        errors = [
            {
                "question_id": "gpt4_4929293a",
                "error": "ingest_and_search: BrokenResourceError",
                "error_stage": "ingest_and_search",
                "error_kind": run_ci.ERROR_PERSISTENT,
            },
            {
                "question_id": "184da446",
                "error": "ingest_and_search: Connection closed",
                "error_stage": "ingest_and_search",
                "error_kind": run_ci.ERROR_TRANSIENT,
            },
        ]
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_ci.print_error_report(errors, 24)
        out = buf.getvalue()
        self.assertIn("PERSISTENT", out)
        self.assertIn("gpt4_4929293a", out)
        # The transient one must NOT be named in the persistent block — an alert
        # that over-reports gets discounted, and then under-reports when it counts.
        persistent_block = out.split("PERSISTENT", 1)[1]
        self.assertNotIn("184da446", persistent_block)


class TestTheErrorReportCarriesTheKind(unittest.TestCase):
    """`run_single` is where the classification has to be attached, because it is
    the only place that still knows how much retry budget the question spent.
    Everything downstream reads `error_kind` off the result dict — if it is never
    written, every check above passes and the gate silently forgives for ever."""

    def _errored(self, exc, attempts_made, attempts_allowed):
        import mesh_client_stdio

        class _Client:
            def __init__(self, question_id, **_kw):
                self.qid = question_id
                self.attempts_made = attempts_made
                self.attempts_allowed = attempts_allowed

            def ingest_and_search(self, **_kw):
                raise exc

        entry = {
            "question_id": "gpt4_4929293a",
            "question_type": "temporal-reasoning",
            "haystack_sessions": [],
            "haystack_dates": [],
            "question": "q",
        }
        with mock.patch.object(mesh_client_stdio, "MeshMemoryClient", _Client), \
             contextlib.redirect_stderr(io.StringIO()):
            return run_ci.run_single(
                entry,
                chat_client=None,
                chat_model="",
                judge_client=None,
                judge_model="",
                top_k=10,
                retrieval_only=True,
            )

    def test_a_validation_rejection_is_recorded_as_persistent(self):
        exc = RuntimeError(
            "remember failed: Bad Request: Validation failed "
            "(key: key must match pattern ^[a-z0-9][a-z0-9-]*[a-z0-9]$)"
        )
        r = self._errored(exc, 1, 4)
        self.assertEqual(run_ci.ERROR_PERSISTENT, r["error_kind"])

    def test_a_blip_that_had_retries_left_is_recorded_as_transient(self):
        r = self._errored(RuntimeError("Connection closed"), 1, 4)
        self.assertEqual(run_ci.ERROR_TRANSIENT, r["error_kind"])


class TestTheGateActuallyUsesTheClassification(unittest.TestCase):
    """Drives the REAL `main()` — parser, cmd_run, verdict and all.

    The pure functions above can all be correct while the caller feeds them the
    wrong thing, and a gate wired to blind input is indistinguishable from no
    gate. So this one runs the actual entrypoint over a mocked `run_single` and
    reads the exit code and the machine-readable reason the workflow greps.
    """

    def _drive(self, error_kind, argv_extra=()):
        dataset = json.loads(run_ci.DATA_FILE.read_text())
        failing = dataset[0]["question_id"]

        def fake_run_single(entry, **_kw):
            if entry["question_id"] == failing:
                return {
                    "question_id": entry["question_id"],
                    "question_type": entry["question_type"],
                    "error": "ingest_and_search: BrokenResourceError",
                    "error_stage": "ingest_and_search",
                    "error_kind": error_kind,
                }
            return {
                "question_id": entry["question_id"],
                "question_type": entry["question_type"],
                # Every surviving question answers correctly, so nothing here can
                # be mistaken for a quality signal: the only thing under test is
                # what the harness does with the one question that never ran.
                "correct": True,
                "search_mode": run_ci.MODE_HYBRID,
            }

        argv = ["run_ci.py", "--retrieval-only", *argv_extra]
        env = {"MESH_API_URL": "http://localhost:0", "MESH_AGENT_KEY": "agk_test_x"}
        buf = io.StringIO()
        with mock.patch.object(run_ci, "run_single", side_effect=fake_run_single), \
             mock.patch.object(run_ci, "_load_env", lambda: None), \
             mock.patch.dict(os.environ, env, clear=False), \
             mock.patch.object(sys, "argv", argv), \
             contextlib.redirect_stdout(buf):
            rc = run_ci.main()
        return rc, buf.getvalue(), failing

    def test_one_persistent_error_makes_the_run_inconclusive(self):
        rc, out, failing = self._drive(run_ci.ERROR_PERSISTENT)
        self.assertEqual(run_ci.EXIT_INCONCLUSIVE, rc)
        self.assertIn(
            f"{run_ci.REASON_PREFIX} {run_ci.REASON_PERSISTENT_ERRORS}", out
        )
        self.assertIn(failing, out)

    def test_the_same_error_classified_transient_is_still_forgiven(self):
        """The control. Same run, same lost question, same shrunken category —
        only the classification differs. Without it the test would pass on a
        build that simply calls every error persistent."""
        rc, out, _ = self._drive(run_ci.ERROR_TRANSIENT)
        self.assertNotIn(run_ci.REASON_PERSISTENT_ERRORS, out)
        self.assertNotEqual(EXIT_REGRESSION, rc)

    def test_the_allowance_is_reachable_from_the_command_line(self):
        """A flag nobody can set is not a lever. Raising the allowance past the
        single failure has to return the run to the old behaviour."""
        rc, out, _ = self._drive(
            run_ci.ERROR_PERSISTENT, argv_extra=("--max-persistent-errors", "1")
        )
        self.assertNotIn(
            f"{run_ci.REASON_PREFIX} {run_ci.REASON_PERSISTENT_ERRORS}", out
        )


class TestBaselineRecordsItsOwnDenominator(unittest.TestCase):
    """The sample gate is one-sided without this.

    `category_comparable` catches a RUN whose sample shrank. Nothing catches a
    BASELINE captured on a smaller sample than the runs it judges — and that is
    the live case: `temporal-reasoning: 1.0` in the shipped baseline rests on 2
    of 4 questions (evc-mesh#362). Restore the missing two and a fully-measured
    4-question run gets compared against a 2-question figure; one miss reads as
    -0.25 and two as a REGRESSION, for a change in sample, not in quality.
    """

    def test_payload_records_sample_sizes(self):
        payload = build_baseline_payload(
            [{"temporal-reasoning": 1.0, "overall": 0.9}],
            MODE_HYBRID,
            top_k=10,
            sizes={"temporal-reasoning": (2, 4), "overall": (22, 24)},
        )
        self.assertEqual(payload["sample_sizes"]["temporal-reasoning"], [2, 4])

    def test_sample_sizes_round_trip_through_load_baseline(self):
        payload = build_baseline_payload(
            [{"temporal-reasoning": 1.0}],
            MODE_HYBRID,
            top_k=10,
            sizes={"temporal-reasoning": (2, 4)},
        )
        tmp = Path(tempfile.mkdtemp()) / "baseline.json"
        tmp.write_text(json.dumps(payload))
        self.assertEqual(load_baseline(tmp).sample_sizes["temporal-reasoning"], (2, 4))

    def test_a_baseline_without_the_field_reads_as_unknown_not_as_matching(self):
        # Every baseline shipped before this field exists. It must not be
        # credited with a denominator it never recorded.
        tmp = Path(tempfile.mkdtemp()) / "baseline.json"
        tmp.write_text(json.dumps({"search_mode": MODE_HYBRID, "scores": {"a": 1.0}}))
        self.assertEqual(load_baseline(tmp).sample_sizes, {})

    def test_restored_questions_do_not_manufacture_a_regression(self):
        # THE coupling this exists for: the fix to #362 restores temporal-reasoning
        # to 4 questions. Against a baseline captured on 2, both restored questions
        # missing is 0.5 vs 1.0 — a REGRESSION verdict on a required check, caused
        # by the sample changing, blocking the very PR that fixes the sample.
        baseline = Baseline(
            scores={"temporal-reasoning": 1.0, "overall": 0.9},
            search_mode=MODE_HYBRID,
            n_runs=1,
            spread={},
            sample_sizes={"temporal-reasoning": (2, 4), "overall": (22, 24)},
        )
        verdict = decide_verdict(
            classify(
                {"temporal-reasoning": 0.5, "overall": 0.9},
                baseline,
                0.25,
                {"temporal-reasoning": (4, 4), "overall": (24, 24)},
            )
        )
        self.assertNotEqual(verdict.exit_code, EXIT_REGRESSION)
        self.assertIn("temporal-reasoning", verdict.unmeasured)

    def test_matching_denominators_are_compared_normally(self):
        # The gate must not have become unpassable: same sample both sides still
        # yields a real verdict, in both directions.
        baseline = Baseline(
            scores={"temporal-reasoning": 1.0, "overall": 0.9},
            search_mode=MODE_HYBRID,
            n_runs=1,
            spread={},
            sample_sizes={"temporal-reasoning": (4, 4), "overall": (24, 24)},
        )
        sizes = {"temporal-reasoning": (4, 4), "overall": (24, 24)}
        self.assertEqual(
            decide_verdict(
                classify({"temporal-reasoning": 1.0, "overall": 0.9}, baseline, 0.25, sizes)
            ).exit_code,
            EXIT_OK,
        )
        self.assertEqual(
            decide_verdict(
                classify({"temporal-reasoning": 0.0, "overall": 0.9}, baseline, 0.25, sizes)
            ).exit_code,
            EXIT_REGRESSION,
        )


class TestRepeatCannotLaunderMissingCoverage(unittest.TestCase):
    """`--repeat N` multiplies precision, never coverage.

    The sample gate's denominator has to be DISTINCT QUESTIONS. Counting answer
    rows instead lets repetition dissolve a systematic loss: the two `gpt4_*`
    ids fail deterministically on every pass (evc-mesh#362), so at `--repeat 3`
    temporal-reasoning's honest 2-of-4 counts as "6 ran", 1/6 clears the 0.25
    tolerance, and the baseline is captured on half the category with the gate
    reporting nothing wrong — the exact defect the gate exists to catch, laundered
    through the flag added to make baselines more trustworthy.
    """

    @staticmethod
    def _rows(passes: int):
        rows = []
        for _ in range(passes):
            for qid in ("aaa1", "bbb2"):
                rows.append(
                    {"question_id": qid, "question_type": "temporal-reasoning", "correct": True}
                )
            for qid in ("gpt4_dead1", "gpt4_dead2"):
                rows.append(
                    {
                        "question_id": qid,
                        "question_type": "temporal-reasoning",
                        "error": "ingest_and_search: BrokenResourceError",
                    }
                )
        return rows

    def test_three_passes_still_report_two_of_four(self):
        sizes = category_sample_sizes(self._rows(3))
        self.assertEqual(sizes["temporal-reasoning"], (2, 4))

    def test_repetition_does_not_make_a_half_category_comparable(self):
        sizes = category_sample_sizes(self._rows(3))
        ran, _attempted = sizes["temporal-reasoning"]
        self.assertFalse(
            category_comparable(ran, 0.25),
            "3 passes over the same 2 surviving questions is still a 2-question sample",
        )

    def test_single_pass_behaviour_is_unchanged(self):
        sizes = category_sample_sizes(self._rows(1))
        self.assertEqual(sizes["temporal-reasoning"], (2, 4))

    def test_rows_without_ids_still_count_individually(self):
        # Hand-built rows in older tests carry no question_id; they must not all
        # collapse into a single "question".
        rows = [
            {"question_type": "multi-session", "correct": True},
            {"question_type": "multi-session", "correct": False},
            {"question_type": "multi-session", "correct": True},
            {"question_type": "multi-session", "correct": True},
        ]
        self.assertEqual(category_sample_sizes(rows)["multi-session"], (4, 4))


class TestTwoBlindnessesStayApart(unittest.TestCase):
    """The sample gate (#361) and the spread gate must not collapse into one.

    Both answer "this category got no verdict", and merging their branches is
    the obvious simplification — but their escalation policies are opposites and
    picking either one for both is a live defect:

      * treat spread-blindness like a sample loss (any occurrence ⇒ exit 2) and
        the advisory arm is INCONCLUSIVE every night for as long as the baseline
        stands, which is the silent no-op this card exists to remove;
      * treat a sample loss like spread-blindness (only total ⇒ exit 2) and
        #361's headline case walks straight back in — temporal-reasoning scored
        on 2 of 4 questions and reported green.

    So the reason kinds, and the escalation each triggers, are pinned here.
    """

    # multi-session's spread (0.9) swamps its baseline (0.8) ⇒ threshold < 0 ⇒
    # no score can fall under it. knowledge-update is a normal, rulable category.
    BASELINE = Baseline(
        scores={"knowledge-update": 0.8, "multi-session": 0.8, "overall": 0.8},
        search_mode=MODE_HYBRID,
        n_runs=3,
        spread={"knowledge-update": 0.1, "multi-session": 0.9, "overall": 0.1},
    )
    FULL = {"knowledge-update": (4, 4), "multi-session": (4, 4), "overall": (24, 24)}

    def _decide(self, scores, sizes, baseline=None, tolerance=0.25):
        baseline = baseline or self.BASELINE
        return decide_verdict(classify(scores, baseline, tolerance, sizes))

    def test_reasons_are_tagged_distinctly(self):
        scores = {"knowledge-update": 0.8, "multi-session": 0.8, "overall": 0.8}
        sizes = dict(self.FULL, **{"knowledge-update": (2, 4)})
        by_cat = {
            v.category: v.ineligible_reason
            for v in classify(scores, self.BASELINE, 0.25, sizes)
        }
        self.assertEqual(by_cat["knowledge-update"], INELIGIBLE_SAMPLE)
        self.assertEqual(by_cat["multi-session"], INELIGIBLE_SPREAD)
        self.assertIsNone(by_cat["overall"])

    def test_one_spread_blind_category_does_not_make_the_run_inconclusive(self):
        # THE regression this guards: if spread-blindness escalated per category,
        # this run — which genuinely enforced knowledge-update and overall — would
        # report that it enforced nothing, every single night.
        scores = {"knowledge-update": 0.8, "multi-session": 0.0, "overall": 0.8}
        verdict = self._decide(scores, self.FULL)
        self.assertEqual(verdict.exit_code, EXIT_OK)
        self.assertEqual(verdict.spread_blind, ["multi-session"])
        self.assertEqual(verdict.unmeasured, [])

    def test_all_spread_blind_is_inconclusive_not_green(self):
        baseline = Baseline(
            scores={"knowledge-update": 0.8, "overall": 0.8},
            search_mode=MODE_HYBRID,
            n_runs=3,
            spread={"knowledge-update": 0.9, "overall": 0.9},
        )
        verdict = self._decide(
            {"knowledge-update": 0.0, "overall": 0.0},
            {"knowledge-update": (4, 4), "overall": (24, 24)},
            baseline,
        )
        self.assertEqual(verdict.exit_code, EXIT_INCONCLUSIVE)
        self.assertEqual(verdict.reason, run_ci.REASON_NO_ELIGIBLE_CATEGORY)

    def test_one_sample_loss_is_inconclusive_even_with_others_eligible(self):
        # The mirror of the test above: for the SAMPLE gate, one is enough.
        scores = {"knowledge-update": 0.8, "multi-session": 0.8, "overall": 0.8}
        sizes = dict(self.FULL, **{"knowledge-update": (2, 4)})
        verdict = self._decide(scores, sizes)
        self.assertEqual(verdict.exit_code, EXIT_INCONCLUSIVE)
        self.assertEqual(verdict.reason, run_ci.REASON_CATEGORY_UNMEASURED)

    def test_sample_loss_outranks_spread_blindness_in_the_reported_reason(self):
        # Both present. The alert must name the anomaly someone can act on, not
        # the standing property of the baseline.
        scores = {"knowledge-update": 0.8, "multi-session": 0.8, "overall": 0.8}
        sizes = dict(self.FULL, **{"knowledge-update": (2, 4)})
        verdict = self._decide(scores, sizes)
        self.assertEqual(verdict.reason, run_ci.REASON_CATEGORY_UNMEASURED)
        self.assertEqual(verdict.spread_blind, ["multi-session"])

    def test_a_category_failing_both_is_reported_as_the_sample_loss(self):
        # multi-session is spread-blind AND lost half its questions. The sample
        # loss is tonight's event and the actionable one, so it wins the tag.
        scores = {"knowledge-update": 0.8, "multi-session": 0.8, "overall": 0.8}
        sizes = dict(self.FULL, **{"multi-session": (2, 4)})
        by_cat = {
            v.category: v.ineligible_reason
            for v in classify(scores, self.BASELINE, 0.25, sizes)
        }
        self.assertEqual(by_cat["multi-session"], INELIGIBLE_SAMPLE)

    def test_regression_outranks_both_blindnesses(self):
        # knowledge-update really regressed (0.0 vs threshold 0.55) while
        # multi-session is spread-blind and overall lost questions. A merge block
        # that a flaky question elsewhere can suppress is not a merge block.
        scores = {"knowledge-update": 0.0, "multi-session": 0.8, "overall": 0.8}
        sizes = dict(self.FULL, **{"overall": (2, 24)})
        verdict = self._decide(scores, sizes)
        self.assertEqual(verdict.exit_code, EXIT_REGRESSION)
        self.assertEqual(verdict.regressions, ["knowledge-update"])

    def test_table_distinguishes_the_two_statuses(self):
        # A reader has to be able to tell "act on this" from "this is how the
        # baseline is". Same glyph for both would make the loud one ignorable.
        buf = io.StringIO()
        scores = {"knowledge-update": 0.8, "multi-session": 0.8, "overall": 0.8}
        sizes = dict(self.FULL, **{"knowledge-update": (2, 4)})
        with contextlib.redirect_stdout(buf):
            run_ci.print_table(scores, self.BASELINE, 0.25, sizes)
        out = buf.getvalue()
        self.assertIn("UNMEASURED", out)
        self.assertIn("no verdict", out)

    def test_a_wiped_category_prints_no_score_rather_than_zero(self):
        # It scored nothing; printing 0.000 would report a wipe as a total miss.
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_ci.print_table(
                {"knowledge-update": 0.8, "overall": 0.8},
                self.BASELINE,
                0.25,
                {"knowledge-update": (4, 4), "multi-session": (0, 4), "overall": (20, 24)},
            )
        row = next(
            ln for ln in buf.getvalue().splitlines() if ln.startswith("multi-session")
        )
        self.assertIn("—", row)
        self.assertNotIn("0.000", row)


class TestCaptureBlockers(unittest.TestCase):
    """A required check's baseline may not be pinned on a partial or degraded run.

    #363 made an incomplete capture WARN. A warning printed into a capture log
    is advice: the file is already on disk, the artifact still uploads, and the
    number still gets committed. That is exactly how `baseline_retrieval.json`
    came to pin `temporal-reasoning: 1.0` on 2 of 4 questions and stay that way
    for six days (evc-mesh#362) — nobody re-read the log.

    The refusal is asymmetric on purpose. See `capture_blockers`' docstring: the
    advisory baseline blocks no merge and costs money per pass, so refusing there
    trades a flawed baseline for none at all.
    """

    FULL = {"temporal-reasoning": (4, 4), "multi-session": (4, 4), "overall": (24, 24)}

    def test_a_complete_hybrid_run_is_capturable(self):
        self.assertEqual(capture_blockers(self.FULL, MODE_HYBRID, retrieval_only=True), [])

    def test_the_live_defect_is_refused(self):
        # The exact shape that produced the shipped baseline: 2 of 4 in
        # temporal-reasoning, 22 of 24 overall, everything else clean.
        sizes = dict(self.FULL, **{"temporal-reasoning": (2, 4), "overall": (22, 24)})
        blockers = capture_blockers(sizes, MODE_HYBRID, retrieval_only=True)
        self.assertTrue(blockers)
        self.assertTrue(
            any("temporal-reasoning measured 2/4" in b for b in blockers),
            f"the blocker must name the category and its denominator: {blockers}",
        )

    def test_a_degraded_mode_is_refused_even_on_a_complete_run(self):
        # Every question ran, so the sample is perfect — and the figures are still
        # unusable: they pin a required check at the quality level of a dead
        # embedder, and every healthy run afterwards is compared across modes.
        blockers = capture_blockers(self.FULL, MODE_BM25_ONLY, retrieval_only=True)
        self.assertTrue(any(MODE_BM25_ONLY in b for b in blockers))

    def test_an_unknown_mode_is_refused(self):
        # Not "probably hybrid". A baseline whose own mode is UNKNOWN makes every
        # future comparison against it INCONCLUSIVE — it would arm the gate with a
        # file that can never produce a verdict.
        self.assertTrue(capture_blockers(self.FULL, MODE_UNKNOWN, retrieval_only=True))

    def test_overall_is_checked_alongside_the_categories_not_instead(self):
        # A run can be short overall while every category it did reach is complete
        # (a whole category wiped out drops `overall` without shortening any
        # surviving row). Checking only the categories would let that through.
        sizes = {"multi-session": (4, 4), "overall": (20, 24)}
        self.assertTrue(any("overall measured 20/24" in b
                            for b in capture_blockers(sizes, MODE_HYBRID, retrieval_only=True)))

    def test_the_advisory_arm_is_deliberately_not_gated(self):
        sizes = dict(self.FULL, **{"temporal-reasoning": (2, 4)})
        self.assertEqual(capture_blockers(sizes, MODE_BM25_ONLY, retrieval_only=False), [])

    def test_a_category_nobody_attempted_is_not_a_blocker(self):
        # attempted == 0 means the category is absent from this dataset, not that
        # it was lost. Reporting it would make every capture unrefusable-by-noise.
        sizes = dict(self.FULL, **{"single-session-user": (0, 0)})
        self.assertEqual(capture_blockers(sizes, MODE_HYBRID, retrieval_only=True), [])


class TestCaptureRefusalReachesTheFile(_ArmHarness):
    """The guard must run BEFORE the write, through the real `main()`.

    A pure-function test of `capture_blockers` stays green with the call site
    deleted, or placed after `write_text`. What is being pinned here is that
    `baseline_retrieval.json` does not exist on disk afterwards.
    """

    def _stub_with_errors(self, errored: set[tuple[str, int]], search_mode: str):
        """Like `_stub_answers`, but the (category, index) pairs in `errored` fail.

        An errored row carries no `correct` key — that is what makes
        `category_sample_sizes` count it as attempted-but-not-ran, which is the
        signal the capture guard reads.
        """
        seen: dict[str, int] = {}

        def fake_run_single(entry, **kwargs):
            cat = entry["question_type"]
            n = seen.get(cat, 0) % self.QUESTIONS_PER_CATEGORY
            seen[cat] = seen.get(cat, 0) + 1
            row = {
                "question_id": entry["question_id"],
                "question_type": cat,
                "search_mode": search_mode,
            }
            if (cat, n) in errored:
                row["error"] = "remember failed: Bad Request: Validation failed (key)"
            else:
                row["correct"] = True
            return row

        patcher = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_an_incomplete_capture_writes_no_file_and_says_why(self):
        self._stub_with_errors({("multi-session", 0)}, MODE_HYBRID)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            # --max-error-rate 0.2 reproduces the real hole: 2 of 24 is 8.3%, UNDER
            # the 10% budget, so the run is never declared inconclusive and sails
            # straight into the capture. Without this the test would pass on the
            # harness-errors guard and prove nothing about the capture guard.
            rc = self._run("--retrieval-only", "--update-baseline", "--max-error-rate", "0.2")
        out = buf.getvalue()
        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertFalse(
            self.RETRIEVAL_BASELINE_FILE.exists(),
            "the baseline must not be on disk after a refused capture",
        )
        self.assertIn("capture-refused", out)
        self.assertNotIn("harness-errors", out)

    def test_a_refused_capture_does_not_overwrite_the_baseline_it_would_replace(self):
        # The failure that costs the most: an operator re-snaps, the run is short,
        # and the previous good baseline is gone. Refusing has to leave it intact.
        prior = {"search_mode": MODE_HYBRID, "n_runs": 3, "scores": {"overall": 0.9},
                 "spread": {}, "sample_sizes": {"overall": [24, 24]}}
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, prior)
        self._stub_with_errors({("multi-session", 0)}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            self._run("--retrieval-only", "--update-baseline", "--max-error-rate", "0.2")
        self.assertEqual(json.loads(self.RETRIEVAL_BASELINE_FILE.read_text()), prior)

    def test_a_complete_hybrid_capture_writes_the_file_with_its_denominators(self):
        self._stub_with_errors(set(), MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            rc = self._run("--retrieval-only", "--update-baseline")
        self.assertEqual(rc, EXIT_OK)
        payload = json.loads(self.RETRIEVAL_BASELINE_FILE.read_text())
        self.assertEqual(payload["search_mode"], MODE_HYBRID)
        # AC3 of the re-snap: the file must carry the denominators, or the
        # baseline-side sample guard stays inert for another six days.
        self.assertIn("sample_sizes", payload)
        for cat in self.CATEGORIES:
            self.assertEqual(payload["sample_sizes"][cat], [4, 4])

    def test_the_escape_hatch_writes_but_only_when_asked(self):
        self._stub_with_errors({("multi-session", 0)}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            rc = self._run("--retrieval-only", "--update-baseline",
                           "--allow-partial-capture", "--max-error-rate", "0.2")
        self.assertEqual(rc, EXIT_OK)
        payload = json.loads(self.RETRIEVAL_BASELINE_FILE.read_text())
        # And it still records the truth about what it was measured on, so the
        # denominator guard can refuse the comparison later.
        self.assertEqual(payload["sample_sizes"]["multi-session"], [3, 4])

    def test_the_escape_hatch_is_rejected_on_a_verdict_run(self):
        with self.assertRaises(SystemExit):
            with contextlib.redirect_stdout(io.StringIO()), \
                    contextlib.redirect_stderr(io.StringIO()):
                self._run("--retrieval-only", "--allow-partial-capture")

    def test_the_advisory_capture_still_writes_on_a_short_sample(self):
        # #363's behaviour, pinned so this change cannot silently extend to the
        # arm it deliberately left alone.
        self._stub_with_errors({("multi-session", 0)}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            rc = self._run("--update-baseline", "--max-error-rate", "0.2")
        self.assertEqual(rc, EXIT_OK)
        self.assertTrue(self.BASELINE_FILE.exists())


class TestResultsArtifact(_ArmHarness):
    """The artifact's behaviour through the REAL `main()`.

    test_gold_rank.py pins the fields and `write_results_artifact` directly; what
    can only be checked here is the CALL SITE — that the write survives the
    early-return verdict paths, and that a failed write cannot move the exit code
    of a required check. A correct writer invoked after the branch that returns is
    still an artifact you do not get on the runs you most want it for.
    """

    def _questions(self) -> list[dict]:
        return json.loads(self.RETRIEVAL_RESULTS_FILE.read_text())["questions"]

    def test_the_artifact_is_written_on_a_passing_run(self):
        self._write_baseline(
            self.RETRIEVAL_BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0},
                "spread": {},
                "sample_sizes": {"knowledge-update": [4, 4], "multi-session": [4, 4]},
            },
        )
        self._stub_answers({"knowledge-update": 4, "multi-session": 4}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            rc = self._run("--retrieval-only")
        self.assertEqual(rc, EXIT_OK)
        self.assertTrue(self.RETRIEVAL_RESULTS_FILE.exists())

    def test_the_artifact_survives_an_early_return(self):
        """INCONCLUSIVE returns before the verdict block. An artifact that only
        appears on the happy path is missing exactly when it is wanted most."""
        self._stub_answers({"knowledge-update": 1, "multi-session": 0}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            rc = self._run("--retrieval-only")
        self.assertEqual(rc, EXIT_INCONCLUSIVE, "no baseline -> cannot compare")
        self.assertTrue(
            self.RETRIEVAL_RESULTS_FILE.exists(),
            "the per-question results are the only thing left to read on a blind run",
        )

    def test_existing_keys_are_written_through_verbatim(self):
        # Comparing against historical artifacts must not break: additive only.
        self._stub_answers({"knowledge-update": 4, "multi-session": 0}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            self._run("--retrieval-only")
        q = self._questions()[0]
        for key in ("question_id", "question_type", "correct", "search_mode"):
            self.assertIn(key, q, f"{key} was dropped — historical readers break")
        self.assertEqual(len(self._questions()), 8)

    def test_a_failure_to_write_the_artifact_cannot_change_the_verdict(self):
        """Observability must never be able to fail a required check."""
        self._write_baseline(
            self.RETRIEVAL_BASELINE_FILE,
            {
                "search_mode": MODE_HYBRID,
                "n_runs": 3,
                "scores": {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0},
                "spread": {},
                "sample_sizes": {"knowledge-update": [4, 4], "multi-session": [4, 4]},
            },
        )
        self._stub_answers({"knowledge-update": 4, "multi-session": 4}, MODE_HYBRID)
        with mock.patch.object(
            run_ci.Path, "write_text", side_effect=OSError("read-only fs")
        ), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(
            io.StringIO()
        ):
            rc = self._run("--retrieval-only")
        self.assertEqual(rc, EXIT_OK, "a write failure must be reported, not fatal")

    def test_the_run_does_not_write_into_the_real_results_directory(self):
        """Isolation guard for the sink itself: with RESULTS_DIR patched, a run
        must leave the repo's own results/ untouched. Without this the self-check
        step drops stub fixtures into the directory CI uploads."""
        real = Path(run_ci.__file__).resolve().parent / "results"
        before = sorted(p.name for p in real.glob("*.json")) if real.exists() else []
        self._stub_answers({"knowledge-update": 4, "multi-session": 0}, MODE_HYBRID)
        with contextlib.redirect_stdout(io.StringIO()):
            self._run("--retrieval-only")
        after = sorted(p.name for p in real.glob("*.json")) if real.exists() else []
        self.assertEqual(before, after, "tests must not write into the uploaded dir")
        self.assertTrue(self.RETRIEVAL_RESULTS_FILE.exists())


class TestTheRecordedSpreadIsNotRunToRunNoise(unittest.TestCase):
    """#ee17a86f. The field is repeatability inside one capture, and the tolerance
    floor — not the field — is what absorbs inter-run movement.

    The defect this class pins is not a wrong number, it is a wrong READING of a
    correct number: `spread: 0.000` on every category invites "the harness is
    deterministic, tighten the gate", and measured inter-run movement at the same
    measurement-path SHA is a full 0.250.
    """

    def test_the_payload_states_the_scope_in_the_field_name(self):
        payload = build_baseline_payload([{"a": 1.0}, {"a": 0.5}], MODE_HYBRID, 10)
        self.assertIn(
            "within_capture_spread", payload,
            "the honest name is gone, so a reader of the committed baseline sees only "
            "`spread` again and has nothing telling them its scope is one process.",
        )
        self.assertEqual(payload["within_capture_spread"], payload["spread"])

    def test_the_old_key_is_still_written(self):
        """Expand, not rename. A bare rename already cost this harness a guard once
        (`samples` → `sample_sizes`): a capture written to the dead name reads as
        ABSENT, which disarms the check silently instead of failing it."""
        payload = build_baseline_payload([{"a": 1.0}, {"a": 0.5}], MODE_HYBRID, 10)
        self.assertIn("spread", payload)

    def test_a_baseline_written_under_the_old_key_alone_still_guards(self):
        """Every baseline committed before this change carries only `spread`. If the
        reader stopped honouring it they would all silently fall back to bare
        tolerance — the inert direction, which announces nothing."""
        with tempfile.TemporaryDirectory() as tmp:
            f = Path(tmp) / "b.json"
            f.write_text(json.dumps({
                "search_mode": MODE_HYBRID, "n_runs": 3,
                "scores": {"a": 1.0}, "spread": {"a": 0.75},
            }), encoding="utf-8")
            self.assertEqual(load_baseline(f).spread, {"a": 0.75})

    def test_the_new_key_wins_when_both_are_present(self):
        with tempfile.TemporaryDirectory() as tmp:
            f = Path(tmp) / "b.json"
            f.write_text(json.dumps({
                "search_mode": MODE_HYBRID, "n_runs": 3, "scores": {"a": 1.0},
                "within_capture_spread": {"a": 0.75}, "spread": {"a": 0.10},
            }), encoding="utf-8")
            self.assertEqual(load_baseline(f).spread, {"a": 0.75})

    def test_neither_key_reads_as_no_spread_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            f = Path(tmp) / "b.json"
            f.write_text(json.dumps({
                "search_mode": MODE_HYBRID, "n_runs": 1, "scores": {"a": 1.0},
            }), encoding="utf-8")
            self.assertEqual(load_baseline(f).spread, {})


class TestTheToleranceFloor(unittest.TestCase):
    """#ee17a86f AC2. `max(tolerance, spread)` is inert while spread is 0.000, so
    `tolerance` is the only term standing between the gate and one-question noise.
    Nothing stopped it being lowered."""

    def test_the_floor_value_itself_is_pinned_to_its_evidence(self):
        """Lowering `MIN_TOLERANCE` is the dangerous edit, and because the argparse
        default is bound to it, doing so also moves the default and reddens a
        scattering of unrelated-looking verdict tests. None of those say WHY. This
        one does, and it is the first failure a reader will scan to.

        0.25 is one question on a 4-question CI category — the movement measured
        between 30543232410 and 30545673968, two runs whose diff touches no
        measurement code. Lowering it is a claim that the instrument got quieter,
        which is a measurement, not an argument.
        """
        self.assertEqual(
            0.25, MIN_TOLERANCE,
            "MIN_TOLERANCE moved. It is not a taste knob: it is the smallest "
            "tolerance the retrieval arm's MEASURED inter-run movement fits inside "
            "(0.250, one question on a 4q category). Lowering it re-arms the failure "
            "#ee17a86f was filed for — a required check reddening on noise no PR "
            "author can clear. Raise or lower it only with a fresh pair of runs at "
            "one measurement-path SHA quoted on the card.",
        )

    def test_the_argparse_default_is_the_floor_itself(self):
        """Read off the shipped parser, not restated: a default that drifted below
        the floor would otherwise be caught only when someone passed the flag."""
        src = (Path(__file__).resolve().parent / "run_ci.py").read_text(encoding="utf-8")
        m = re.search(r'"--tolerance",\s*\n\s*type=float,\s*\n\s*default=([A-Za-z_0-9.]+),', src)
        self.assertIsNotNone(m, "the --tolerance default moved; repoint this pin")
        self.assertEqual(
            "MIN_TOLERANCE", m.group(1),
            f"--tolerance defaults to {m.group(1)!r} rather than the floor constant, so "
            f"the two can drift apart silently.",
        )

    def test_the_workflow_does_not_pass_a_tolerance_under_the_floor(self):
        """The likely edit is in the WORKFLOW, not in Python: `inputs.tolerance ||
        '0.25'` appears twice as a literal. A floor enforced only in argparse would
        never see a change made there."""
        wf = (Path(__file__).resolve().parents[2]
              / ".github" / "workflows" / "memory-bench.yml").read_text(encoding="utf-8")
        literals = re.findall(r"inputs\.tolerance \|\| '([0-9.]+)'", wf)
        self.assertTrue(literals, "the tolerance default literal moved; repoint this pin")
        for lit in literals:
            self.assertGreaterEqual(
                float(lit), MIN_TOLERANCE,
                f"the workflow defaults --tolerance to {lit}, under the {MIN_TOLERANCE} "
                f"noise floor. The gate would redden on movement measured between two "
                f"runs of identical measurement code, which no PR author can clear.",
            )

    def test_a_tolerance_under_the_floor_is_refused_before_measuring(self):
        rc, out, reached = _run_gate_with_tolerance(0.1)
        self.assertEqual(EXIT_INCONCLUSIVE, rc, out)
        self.assertIn("below the noise floor", out)
        self.assertIn("GATE_REASON: tolerance-below-floor", out)
        self.assertFalse(
            reached,
            "the refusal fired but only AFTER measuring. The point of the floor is "
            "that a run which cannot support a verdict does not spend a measurement "
            "to find that out.",
        )

    def test_the_override_lets_it_through(self):
        """Deliberate, loud, and possible — a floor with no override gets worked
        around by editing the constant, which leaves no trace at the call site."""
        _rc, out, reached = _run_gate_with_tolerance(0.1, override=True)
        self.assertNotIn("below the noise floor", out)
        self.assertTrue(reached, "the override did not actually let the run proceed")

    def test_the_floor_itself_is_allowed(self):
        _rc, out, reached = _run_gate_with_tolerance(MIN_TOLERANCE)
        self.assertNotIn("below the noise floor", out)
        self.assertTrue(reached, "the floor value itself was refused; it must be allowed")


class _MeasurementReached(Exception):
    """Raised by the stubbed measurement so the driver can assert, positively, that
    the floor check let the run through — rather than inferring it from the absence
    of a message."""


def _run_gate_with_tolerance(tolerance: float, override: bool = False):
    """Drive the shipped `main()` up to the first measurement.

    `run_single` is stubbed to raise immediately, which does three things: it keeps
    the check under test (the floor is evaluated BEFORE anything is measured) as the
    only thing that can decide the outcome; it makes "did we get past the floor?" a
    positive signal instead of the absence of a string; and it keeps the real
    dataset's 24 questions and their error spew out of the job log — these suites run
    inside the required check, and a self-check that prints its own fixture failures
    there is indistinguishable from the gate failing.

    Returns (rc, stdout, reached_measurement).
    """
    argv = ["run_ci.py", "--retrieval-only", "--tolerance", str(tolerance)]
    if override:
        argv.append("--allow-tolerance-below-floor")
    buf = io.StringIO()
    env = {"MESH_API_URL": "http://127.0.0.1:1", "MESH_AGENT_KEY": "x"}
    reached = False

    def _stub(*_a, **_kw):
        nonlocal reached
        reached = True
        raise _MeasurementReached

    with mock.patch.dict(os.environ, env, clear=False), \
         mock.patch.object(sys, "argv", argv), \
         mock.patch.object(run_ci, "run_single", side_effect=_stub), \
         contextlib.redirect_stdout(buf):
        try:
            rc = run_ci.main()
        except SystemExit as exc:
            rc = exc.code
        except (_MeasurementReached, Exception):  # noqa: BLE001
            rc = None
    return rc, buf.getvalue(), reached


if __name__ == "__main__":
    unittest.main(verbosity=2)
