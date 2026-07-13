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
    EXIT_REGRESSION,
    MODE_BM25_ONLY,
    MODE_HYBRID,
    MODE_UNKNOWN,
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


if __name__ == "__main__":
    unittest.main(verbosity=2)
