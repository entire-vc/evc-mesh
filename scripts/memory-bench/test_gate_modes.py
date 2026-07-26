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
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import run_ci  # noqa: E402
from run_ci import (  # noqa: E402
    EXIT_REGRESSION,
    MODE_BM25_ONLY,
    MODE_HYBRID,
    MODE_UNKNOWN,
    category_comparable,
    category_sample_sizes,
    decide_verdict,
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
                "scores": {"single-session-user": 0.75, "overall": 0.83},
            }
        )
        scores, mode = load_baseline(path)
        self.assertEqual(mode, MODE_HYBRID)
        self.assertEqual(scores["overall"], 0.83)

    def test_legacy_flat_schema_has_no_mode(self):
        # Backward compat: a pre-mode baseline is UNKNOWN, so it yields
        # INCONCLUSIVE rather than a coin-flip verdict.
        path = self._write({"single-session-user": 0.75, "overall": 0.83})
        scores, mode = load_baseline(path)
        self.assertEqual(mode, MODE_UNKNOWN)
        self.assertEqual(scores["overall"], 0.83)
        self.assertFalse(modes_comparable(mode, MODE_HYBRID))


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

    BASELINE = {
        "temporal-reasoning": 1.0,
        "multi-session": 1.0,
        "overall": 1.0,
    }

    @staticmethod
    def _verdict(scores, sizes, baseline, tolerance=0.25):
        """Calls the REAL decision — never a copy of it.

        `decide_verdict` is the same function cmd_run returns from, so reverting
        the logic in run_ci.py fails these tests. A test that re-implemented the
        rule here would pass against the reverted code and guard nothing.
        """
        rc, _regressions, _unmeasured = decide_verdict(scores, sizes, baseline, tolerance)
        return rc

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
        rc, _regressions, unmeasured = decide_verdict(scores, sizes, self.BASELINE, 0.25)
        self.assertEqual(rc, run_ci.EXIT_INCONCLUSIVE)
        self.assertIn("temporal-reasoning", unmeasured)

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


class TestSampleIsPrinted(unittest.TestCase):
    def test_table_shows_the_denominator_and_flags_unmeasured(self):
        # The score alone is unreadable: "1.000" looks identical whether it came
        # from 4 questions or from the 2 that survived.
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_ci.print_table(
                {"temporal-reasoning": 1.0},
                {"temporal-reasoning": 1.0},
                0.25,
                {"temporal-reasoning": (2, 4)},
            )
        out = buf.getvalue()
        self.assertIn("2/4", out)
        self.assertIn("UNMEASURED", out)


if __name__ == "__main__":
    unittest.main(verbosity=2)
