#!/usr/bin/env python3
"""Self-check for the ARM scoping of the recall gate (ADR-0003).

    python scripts/memory-bench/test_gate_arm.py

Background — the defect these pin
---------------------------------
The recall gate was required on every PR and took its target from
`secrets.MESH_API_URL`: the deployed production server. No job built `cmd/api`
from the branch. So the required check measured the binary that was ALREADY
RUNNING and reported a confident green about a program the PR did not contain.
PRs #388/#389/#391 (memory chunking) each passed that way, and the regression
they carried — new memories' vectors going to `memory_chunks` while nothing read
them back, so `memories.embedding` was NULL and those memories left the dense
arm — was invisible to it by construction.

The fix splits the gate into two arms with two targets. Which makes the *new*
hazard the one tested here: two baseline files that look interchangeable and are
not. `baseline_retrieval.json` (prod: prod's embedder, prod's accumulated
corpus) and `baseline_retrieval_branch.json` (branch: bge-small-en-v1.5, empty
DB) hold the same keys with the same value ranges. Comparing a branch run to the
prod baseline is arithmetically fine and factually meaningless — and it fails in
whichever direction the numbers happen to fall, so it can equally block a good
PR or pass a bad one.

The invariant: a baseline may only judge a run from its own arm, and every other
case is INCONCLUSIVE — never REGRESSION. Exit 1 blocks a merge, and no PR author
can fix which file CI picked up.
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
    ARM_BRANCH,
    ARM_PROD,
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    EXIT_REGRESSION,
    MODE_HYBRID,
    REASON_ARM_MISMATCH,
    build_baseline_payload,
    load_baseline,
)


class _Harness(unittest.TestCase):
    """Drive the real `main()` end-to-end, as test_gate_modes.py does.

    Same reasoning: the thing being pinned is not a pure helper, it is which
    FILE the caller resolved and whether the verdict path honoured its label. A
    unit test of the comparison helper stays green with the bug fully present.
    """

    CATEGORIES = ["knowledge-update", "multi-session"]
    QUESTIONS_PER_CATEGORY = 4

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        dataset = [
            {"question_id": f"q{i}", "question_type": cat}
            for cat in self.CATEGORIES
            for i in range(4)
        ]
        data_file = self.tmp / "data.json"
        data_file.write_text(json.dumps(dataset))

        # Every sink main() can write, redirected into tmp. Missing one here
        # means a stub baseline lands in the repo and becomes a real merge
        # threshold — see the same list in test_gate_modes.py.
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
            setattr(self, attr, p)

        for patcher in (
            mock.patch.object(run_ci, "DATA_FILE", data_file),
            mock.patch.dict(
                os.environ,
                {"MESH_API_URL": "http://mesh.test", "MESH_AGENT_KEY": "k"},
            ),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def _stub_answers(self, correct_by_category: dict[str, int]):
        seen: dict[str, int] = {}

        def fake_run_single(entry, **kwargs):
            cat = entry["question_type"]
            n = seen.get(cat, 0) % self.QUESTIONS_PER_CATEGORY
            seen[cat] = seen.get(cat, 0) + 1
            return {
                "question_id": entry["question_id"],
                "question_type": cat,
                "correct": n < correct_by_category.get(cat, 0),
                "search_mode": MODE_HYBRID,
                "gold_rank": 1,
                "n_returned": 10,
                "n_haystack": 40,
            }

        p = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        p.start()
        self.addCleanup(p.stop)

    def _run(self, *argv: str) -> tuple[int, str]:
        buf = io.StringIO()
        with mock.patch.object(sys, "argv", ["run_ci.py", *argv]), \
                contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
            rc = run_ci.main()
        return rc, buf.getvalue()

    def _baseline(self, scores: dict[str, float], arm: str | None) -> dict:
        payload = build_baseline_payload(
            [scores, scores, scores], MODE_HYBRID, 10,
            {c: (4, 4) for c in scores}, arm or ARM_PROD,
        )
        if arm is None:
            # A pre-ADR-0003 file: written before the field existed.
            payload.pop("arm")
        return payload


class TestTheArmSelectsTheBaselineFile(_Harness):
    def test_prod_arm_reads_the_prod_baseline(self):
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_PROD)
        self.assertEqual(rc, EXIT_OK, out)

    def test_branch_arm_reads_the_branch_baseline(self):
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_BRANCH)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_OK, out)

    def test_branch_arm_ignores_a_present_prod_baseline(self):
        """The failure this rules out: the branch arm silently falling back to
        the prod file because its own is absent. That is the ORIGINAL defect
        wearing new clothes — a green certifying a measurement of prod."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn("no-baseline", out)

    def test_default_arm_is_prod(self):
        """Every existing caller (the prod canary, a local run, the docs' own
        command) omits --arm. If the default moved, they would all read a file
        that does not exist and report no-baseline."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only")
        self.assertEqual(rc, EXIT_OK, out)


class TestACrossArmComparisonIsRefused(_Harness):
    def test_prod_baseline_in_the_branch_path_is_inconclusive_not_regression(self):
        """Someone copies baseline_retrieval.json over the branch one. The scores
        differ (different embedder), so an unguarded gate returns a verdict —
        and which verdict is pure luck. Must refuse instead."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)

    def test_it_refuses_even_when_the_scores_would_have_PASSED(self):
        """The direction that hides. A mislabelled baseline whose numbers happen
        to clear tolerance produces a GREEN required check over a comparison
        between two different systems — indistinguishable from a real pass, and
        the exact shape of #b052cdda. A guard that only fires on a red delta
        would be no guard at all."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})  # 1.000, way over
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertNotEqual(rc, EXIT_OK, "a cross-arm comparison must never be green")
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)

    def test_a_cross_arm_mismatch_never_blocks_a_merge(self):
        """Scores far below the mislabelled baseline: the tempting behaviour is
        EXIT_REGRESSION. But no PR author can fix which file CI resolved, and a
        red nobody can clear gets the check bypassed — which is how a safety net
        dies permanently rather than for one PR."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 0, "multi-session": 0})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertNotEqual(rc, EXIT_REGRESSION, "must not block on a file-choice fault")
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)

    def test_branch_baseline_in_the_prod_path_is_refused_too(self):
        """Symmetric. Tested separately because the guard has two halves and
        only one of them is exercised by the branch-side cases: an unstated arm
        is TOLERATED in prod (every pre-ADR-0003 file has none) and REFUSED in
        branch, so a one-sided test would pass with the prod half deleted."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_BRANCH)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_PROD)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)


class TestUnstatedArmBackCompat(_Harness):
    def test_a_pre_adr_baseline_still_judges_the_prod_arm(self):
        """`baseline_retrieval.json` on main has no `arm` field. If an unstated
        arm were refused, this change would wedge the prod canary on the commit
        that lands it — a self-inflicted total blindness."""
        payload = self._baseline(
            {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, None)
        self.assertNotIn("arm", payload)
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(payload))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_PROD)
        self.assertEqual(rc, EXIT_OK, out)

    def test_an_unstated_arm_is_refused_in_the_BRANCH_arm(self):
        """No legitimate unlabelled branch baseline can exist — the file is
        created by this change. So unstated there means "the prod file was
        copied in", which is exactly the case that must not produce a verdict."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(self._baseline(
            {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, None)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)


class TestTheArmIsRecordedOnCapture(_Harness):
    def test_capture_writes_the_arm_into_the_file(self):
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH,
                            "--update-baseline", "--repeat", "2")
        self.assertEqual(rc, EXIT_OK, out)
        written = json.loads(self.BRANCH_RETRIEVAL_BASELINE_FILE.read_text())
        self.assertEqual(written["arm"], ARM_BRANCH)
        self.assertEqual(load_baseline(self.BRANCH_RETRIEVAL_BASELINE_FILE).arm, ARM_BRANCH)

    def test_capture_in_one_arm_does_not_touch_the_other_arms_file(self):
        """A capture that wrote both would silently overwrite the prod baseline —
        the threshold behind the canary — with numbers from a different embedder."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 0.5, "multi-session": 0.5, "overall": 0.5}, ARM_PROD)))
        before = self.RETRIEVAL_BASELINE_FILE.read_text()
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        self._run("--retrieval-only", "--arm", ARM_BRANCH, "--update-baseline", "--repeat", "2")
        self.assertEqual(self.RETRIEVAL_BASELINE_FILE.read_text(), before)

    def test_the_committed_branch_baseline_states_arm_branch(self):
        """Guards the real file in the repo, not a fixture. A branch baseline
        committed without its label is accepted by nothing and INCONCLUSIVEs the
        required check on every PR."""
        real = Path(run_ci.__file__).resolve().parent / "baseline_retrieval_branch.json"
        if not real.exists():
            self.skipTest("branch baseline not captured yet")
        self.assertEqual(json.loads(real.read_text()).get("arm"), ARM_BRANCH)


if __name__ == "__main__":
    unittest.main(verbosity=2)
