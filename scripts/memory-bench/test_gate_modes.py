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

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import run_ci  # noqa: E402
from run_ci import (  # noqa: E402
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    EXIT_REGRESSION,
    MODE_BM25_ONLY,
    MODE_HYBRID,
    MODE_UNKNOWN,
    Baseline,
    build_baseline_payload,
    classify,
    effective_tolerance,
    load_baseline,
    modes_comparable,
    resolve_run_search_mode,
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

        for p, attr in (
            (self.tmp / "baseline.json", "BASELINE_FILE"),
            (self.tmp / "baseline_retrieval.json", "RETRIEVAL_BASELINE_FILE"),
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


if __name__ == "__main__":
    unittest.main(verbosity=2)
