#!/usr/bin/env python3
"""Self-check for the dense-arm gate: `hybrid` is a claim about the EMBEDDER.

    python scripts/memory-bench/test_gate_dense_arm.py    # or: python -m unittest

`search_mode: hybrid` is set when the dense arm ran end-to-end — embedder alive,
query vectorised, VectorSearch returned no error. It is silent on whether that
arm matched a single row. Those are different states, and the difference is not
academic: in run 30316983402 every bench fixture had been written after the
chunked-embed deploy, so every `memories.embedding` was NULL, VectorSearch
matched nothing across the whole haystack — and the gate reported
`single-session-user 1.000`, `overall 0.9583`, `search_mode: hybrid`,
`degraded: false`. The best result ever recorded, measured on a corpus its dense
arm could not see.

Two properties are pinned here, and the second matters as much as the first:

  1. `hybrid` + `dense_rows == 0` everywhere ⇒ INCONCLUSIVE, its own reason kind,
     never REGRESSION (the author of a PR cannot fix a corpus with no
     embeddings, and a required check that reds on that gets bypassed).
  2. A server that does not report `dense_rows` at all changes NOTHING. If a
     missing field read as zero, this gate would take the prod arm inconclusive
     from the moment it merged until the moment the server was deployed — it
     would break during exactly the window it exists to fix.
"""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import run_ci  # noqa: E402
from run_ci import (  # noqa: E402
    DENSE_ARM_EMPTY,
    DENSE_ARM_SERVED,
    DENSE_ARM_UNKNOWN,
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    EXIT_REGRESSION,
    MODE_BM25_ONLY,
    MODE_HYBRID,
    REASON_DENSE_ARM_EMPTY,
    capture_blockers,
    resolve_dense_arm_status,
)
from test_gate_modes import _ArmHarness  # noqa: E402


def q(mode: str, dense: object = "omit") -> dict:
    """One question result. `dense="omit"` = the server never reported the key."""
    row = {"question_id": "q", "question_type": "multi-session", "correct": True, "search_mode": mode}
    if dense != "omit":
        row["dense_rows"] = dense
    return row


class TestResolveDenseArmStatus(unittest.TestCase):
    def test_hybrid_with_rows_is_served(self):
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID, 30), q(MODE_HYBRID, 27)]), DENSE_ARM_SERVED)

    def test_hybrid_with_zero_everywhere_is_empty(self):
        """THE case. Nothing else in the envelope distinguishes this from health."""
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID, 0), q(MODE_HYBRID, 0)]), DENSE_ARM_EMPTY)

    def test_one_zero_among_rows_is_still_served(self):
        """A lost arm is corpus-wide, not per-query.

        The vector arm draws from a relevance-neutral candidate pool, so it
        returns rows for any query while anything in the workspace carries an
        embedding. A single zero is a query oddity; treating it as a lost arm
        would take a required check inconclusive on noise, and a gate that cries
        wolf is a gate people stop reading.
        """
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID, 0), q(MODE_HYBRID, 12)]), DENSE_ARM_SERVED)

    def test_absent_field_is_unknown_not_empty(self):
        """BACK-COMPAT. An older Mesh reports nothing; that is not a finding."""
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID), q(MODE_HYBRID)]), DENSE_ARM_UNKNOWN)

    def test_null_field_is_unknown_not_empty(self):
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID, None)]), DENSE_ARM_UNKNOWN)

    def test_bm25_only_zeroes_are_not_evidence(self):
        """A deployment with no embedder has no dense arm to lose.

        Counting its zeroes would fire `dense-arm-empty` on every bm25-only run
        — a legitimate configuration the mode gate already owns. One cause must
        not produce two alerts with two different owners.
        """
        self.assertEqual(resolve_dense_arm_status([q(MODE_BM25_ONLY, 0), q(MODE_BM25_ONLY, 0)]), DENSE_ARM_UNKNOWN)

    def test_a_bool_is_not_a_count(self):
        """`bool` subclasses `int`: `dense_rows: true` must not read as 1 row."""
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID, True)]), DENSE_ARM_UNKNOWN)

    def test_a_string_is_not_a_count(self):
        self.assertEqual(resolve_dense_arm_status([q(MODE_HYBRID, "30")]), DENSE_ARM_UNKNOWN)

    def test_no_results_at_all_is_unknown(self):
        self.assertEqual(resolve_dense_arm_status([]), DENSE_ARM_UNKNOWN)

    def test_errored_questions_carry_no_verdict_and_no_count(self):
        """An errored question never ran a recall, so it says nothing either way."""
        errored = {"question_id": "q", "question_type": "multi-session", "error": "boom"}
        self.assertEqual(resolve_dense_arm_status([errored, q(MODE_HYBRID, 5)]), DENSE_ARM_SERVED)
        self.assertEqual(resolve_dense_arm_status([errored]), DENSE_ARM_UNKNOWN)


class _DenseArmHarness(_ArmHarness):
    """`_ArmHarness` with a stub that also reports `dense_rows` per question."""

    HYBRID_BASELINE = {
        "search_mode": MODE_HYBRID,
        "n_runs": 3,
        "scores": {"knowledge-update": 0.5, "multi-session": 0.5, "overall": 0.5},
        "spread": {"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0},
        # `overall` counts DISTINCT question_ids, and _ArmHarness reuses q0..q3 in
        # both categories — so 4, not 8. A mismatch here reads as
        # `⚠ UNMEASURED` and would mask whatever the test was actually pinning.
        "sample_sizes": {"knowledge-update": [4, 4], "multi-session": [4, 4], "overall": [4, 4]},
    }

    def _stub_dense(self, correct_by_category: dict[str, int], dense: object, mode: str = MODE_HYBRID):
        seen: dict[str, int] = {}

        def fake_run_single(entry, **kwargs):
            cat = entry["question_type"]
            n = seen.get(cat, 0) % self.QUESTIONS_PER_CATEGORY
            seen[cat] = seen.get(cat, 0) + 1
            row = {
                "question_id": entry["question_id"],
                "question_type": cat,
                "correct": n < correct_by_category.get(cat, 0),
                "search_mode": mode,
            }
            if dense != "omit":
                row["dense_rows"] = dense
            return row

        p = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        p.start()
        self.addCleanup(p.stop)

    def _gate_log(self, *argv: str) -> tuple[int, str]:
        import contextlib
        import io

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = self._run(*argv)
        return rc, buf.getvalue()


class TestDenseArmEmptyIsInconclusive(_DenseArmHarness):
    def test_a_hybrid_run_with_an_empty_dense_arm_is_inconclusive(self):
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, 0)

        rc, log = self._gate_log("--retrieval-only", "--tolerance", "0.25")

        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertNotEqual(rc, EXIT_REGRESSION, "the author cannot fix an unembedded corpus")
        self.assertIn(f"{run_ci.REASON_PREFIX} {REASON_DENSE_ARM_EMPTY}", log)

    def test_the_identical_run_with_a_live_dense_arm_passes(self):
        """POSITIVE CONTROL.

        Same baseline, same scores, same mode — only `dense_rows` differs. Without
        this, a gate hard-wired to INCONCLUSIVE would pass the test above and
        look like it worked.
        """
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, 30)

        self.assertEqual(self._run("--retrieval-only", "--tolerance", "0.25"), EXIT_OK)

    def test_an_empty_dense_arm_outranks_a_score_regression(self):
        """A run served without its dense arm measured nothing comparable.

        Same precedence the mode gate already has, and for the same reason:
        reporting REGRESSION here would blame the diff for the corpus.
        """
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        # 0.0 against a 0.5 baseline at tolerance 0.25 is a genuine REGRESSION —
        # `test_a_regressing_run_still_regresses` proves the same numbers exit 1
        # when the dense arm is not the issue. So this asserts precedence, not
        # merely that some earlier branch happened to fire.
        self._stub_dense({"knowledge-update": 0, "multi-session": 0}, 0)

        rc, log = self._gate_log("--retrieval-only", "--tolerance", "0.25")

        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertIn(REASON_DENSE_ARM_EMPTY, log)

    def test_bm25_only_run_does_not_raise_the_dense_arm_reason(self):
        """It is incomparable for a DIFFERENT reason, with a different fix."""
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, 0, mode=MODE_BM25_ONLY)

        rc, log = self._gate_log("--retrieval-only", "--tolerance", "0.25")

        self.assertEqual(rc, EXIT_INCONCLUSIVE)
        self.assertNotIn(REASON_DENSE_ARM_EMPTY, log)
        self.assertIn(run_ci.REASON_MODE_MISMATCH, log)


class TestOldServerWithoutDenseRowsIsUnaffected(_DenseArmHarness):
    """THE BACK-COMPAT PROPERTY — pinned, not assumed.

    Until the server reporting `dense_rows` is deployed, the prod arm sees no
    such field. If that read as "empty", this gate would wedge the prod canary at
    INCONCLUSIVE for the whole rollout window: broken precisely while being
    fixed, which is how a correct guard gets reverted.
    """

    def test_a_passing_run_still_passes(self):
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, "omit")

        rc, log = self._gate_log("--retrieval-only", "--tolerance", "0.25")

        self.assertEqual(rc, EXIT_OK)
        self.assertNotIn(REASON_DENSE_ARM_EMPTY, log)

    def test_a_regressing_run_still_regresses(self):
        """Back-compat must not turn the gate OFF either — silence is not a pass."""
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 0, "multi-session": 0}, "omit")

        # Tolerance 0.25, not 0.10: with 4 questions per category the smallest
        # measurable step is 0.25, so `category_comparable(4, 0.10)` is False and
        # the run would exit INCONCLUSIVE on the SAMPLE gate — reading as
        # "back-compat broke the gate" when nothing about dense_rows was involved.
        self.assertEqual(self._run("--retrieval-only", "--tolerance", "0.25"), EXIT_REGRESSION)

    def test_the_gate_says_out_loud_that_it_could_not_check(self):
        """An inert check that stays quiet is indistinguishable from a passing one."""
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, "omit")

        _, log = self._gate_log("--retrieval-only", "--tolerance", "0.25")

        self.assertIn("did not report `dense_rows`", log)


class TestCaptureRefusesAnEmptyDenseArm(unittest.TestCase):
    """A baseline snapped with no dense arm pins the blindness as the floor.

    Every later healthy run scores at or above a BM25-only floor, so the gate
    would be permanently green AND permanently blind — the exact failure this
    harness exists to remove, installed as its own standard.
    """

    SIZES = {"multi-session": (4, 4), "overall": (4, 4)}

    def test_empty_dense_arm_blocks_a_retrieval_capture(self):
        blockers = capture_blockers(self.SIZES, MODE_HYBRID, True, DENSE_ARM_EMPTY)
        self.assertTrue(blockers)
        self.assertTrue(any("dense arm returned ZERO rows" in b for b in blockers))

    def test_a_served_dense_arm_does_not_block(self):
        self.assertEqual(capture_blockers(self.SIZES, MODE_HYBRID, True, DENSE_ARM_SERVED), [])

    def test_an_unreporting_server_does_not_block(self):
        """Back-compat again: never refuse a capture over a check we cannot run."""
        self.assertEqual(capture_blockers(self.SIZES, MODE_HYBRID, True, DENSE_ARM_UNKNOWN), [])

    def test_the_default_is_inert(self):
        """Callers that predate the argument keep their exact behaviour."""
        self.assertEqual(capture_blockers(self.SIZES, MODE_HYBRID, True), [])

    def test_the_advisory_arm_is_still_warn_and_write(self):
        self.assertEqual(capture_blockers(self.SIZES, MODE_HYBRID, False, DENSE_ARM_EMPTY), [])


class TestResultsArtifactRecordsTheDenseArm(_DenseArmHarness):
    def test_dense_arm_and_per_question_counts_land_in_the_artifact(self):
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, 0)
        self._run("--retrieval-only", "--tolerance", "0.25")

        payload = json.loads(self.RETRIEVAL_RESULTS_FILE.read_text())
        self.assertEqual(payload["dense_arm"], DENSE_ARM_EMPTY)
        self.assertEqual(payload["search_mode"], MODE_HYBRID)
        self.assertTrue(all(row["dense_rows"] == 0 for row in payload["questions"]))

    def test_the_artifact_is_written_even_though_the_run_went_inconclusive(self):
        """Observability must survive every early-returning verdict branch."""
        self._write_baseline(self.RETRIEVAL_BASELINE_FILE, self.HYBRID_BASELINE)
        self._stub_dense({"knowledge-update": 2, "multi-session": 2}, 0)
        self.assertEqual(self._run("--retrieval-only", "--tolerance", "0.25"), EXIT_INCONCLUSIVE)
        self.assertTrue(self.RETRIEVAL_RESULTS_FILE.exists())


class TestClientReadsTheEnvelope(unittest.TestCase):
    """`_envelope_int` — every rejected shape must land on None, never on 0."""

    def setUp(self):
        from mesh_client_stdio import _envelope_int

        self.read = _envelope_int

    def test_a_count_is_read(self):
        self.assertEqual(self.read({"dense_rows": 30}, "dense_rows"), 30)

    def test_zero_is_a_real_count_not_an_absence(self):
        self.assertEqual(self.read({"dense_rows": 0}, "dense_rows"), 0)

    def test_absent_is_none(self):
        self.assertIsNone(self.read({}, "dense_rows"))

    def test_every_bad_shape_is_none(self):
        for bad in (None, "30", 3.5, True, False, -1, [], {}):
            with self.subTest(bad=bad):
                self.assertIsNone(self.read({"dense_rows": bad}, "dense_rows"))

    def test_a_non_dict_payload_is_none(self):
        self.assertIsNone(self.read(["items"], "dense_rows"))


if __name__ == "__main__":
    unittest.main(verbosity=2)
