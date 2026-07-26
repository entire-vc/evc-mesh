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


class TestBaselineSampleMismatch(unittest.TestCase):
    """A baseline captured on a different denominator is not an operand.

    This is the guard for the defect that produced today's `baseline_retrieval.json`:
    `temporal-reasoning: 1.0` was captured on 2 of its 4 questions, because the
    other 2 could not be stored at all. #361 guards the RUN side (a sample that
    shrank); nothing guarded the baseline side, so restoring the two questions
    would have scored 4 against a 2-question 1.000 and called the difference a
    quality regression on a required check.
    """

    def _write(self, payload) -> Path:
        tmp = Path(tempfile.mkdtemp()) / "baseline_retrieval.json"
        tmp.write_text(json.dumps(payload))
        return tmp

    def test_samples_round_trip_from_the_new_schema(self):
        path = self._write(
            {
                "search_mode": MODE_HYBRID,
                "top_k": 10,
                "samples": {"temporal-reasoning": 4, "overall": 24},
                "scores": {"temporal-reasoning": 1.0, "overall": 0.9},
            }
        )
        self.assertEqual(
            {"temporal-reasoning": 4, "overall": 24},
            run_ci.load_baseline_samples(path),
        )

    def test_a_baseline_without_samples_reads_as_no_constraint(self):
        """Absence must NOT read as a mismatch. Every baseline in existence
        predates the field, so reading absence as not-comparable would take the
        required gate blind on every category at once — a reader tightened past
        anything its writer has emitted."""
        path = self._write(
            {"search_mode": MODE_HYBRID, "scores": {"temporal-reasoning": 1.0}}
        )
        self.assertEqual({}, run_ci.load_baseline_samples(path))
        rc, regressions, unmeasured = decide_verdict(
            {"temporal-reasoning": 0.5},
            {"temporal-reasoning": (4, 4)},
            {"temporal-reasoning": 1.0},
            0.25,
            run_ci.load_baseline_samples(path),
        )
        # Unchanged behaviour: still a regression, judged on tolerance alone.
        self.assertEqual(run_ci.EXIT_REGRESSION, rc)
        self.assertEqual(["temporal-reasoning"], regressions)
        self.assertEqual([], unmeasured)

    def test_a_bigger_run_than_the_baseline_is_not_a_regression(self):
        """The exact case this task exists to make safe: the bug is fixed, all 4
        questions run, 2 of them miss — against a baseline of 1.000 on 2."""
        rc, regressions, unmeasured = decide_verdict(
            {"temporal-reasoning": 0.5, "multi-session": 1.0},
            {"temporal-reasoning": (4, 4), "multi-session": (4, 4)},
            {"temporal-reasoning": 1.0, "multi-session": 1.0},
            0.25,
            {"temporal-reasoning": 2, "multi-session": 4},
        )
        self.assertEqual([], regressions, "a sample change was called a regression")
        self.assertEqual(["temporal-reasoning"], unmeasured)
        self.assertEqual(run_ci.EXIT_INCONCLUSIVE, rc)

    def test_a_matching_denominator_is_still_judged_normally(self):
        """The guard must not have made the gate unable to fail: same denominator,
        real drop, still a regression."""
        rc, regressions, _ = decide_verdict(
            {"temporal-reasoning": 0.25},
            {"temporal-reasoning": (4, 4)},
            {"temporal-reasoning": 1.0},
            0.25,
            {"temporal-reasoning": 4},
        )
        self.assertEqual(run_ci.EXIT_REGRESSION, rc)
        self.assertEqual(["temporal-reasoning"], regressions)

    def test_a_regression_elsewhere_still_outranks_the_mismatch(self):
        """Precedence is unchanged: REGRESSION > INCONCLUSIVE. One incomparable
        category must not suppress a real finding in another."""
        rc, regressions, unmeasured = decide_verdict(
            {"temporal-reasoning": 1.0, "multi-session": 0.25},
            {"temporal-reasoning": (4, 4), "multi-session": (4, 4)},
            {"temporal-reasoning": 1.0, "multi-session": 1.0},
            0.25,
            {"temporal-reasoning": 2, "multi-session": 4},
        )
        self.assertEqual(run_ci.EXIT_REGRESSION, rc)
        self.assertEqual(["multi-session"], regressions)
        self.assertEqual(["temporal-reasoning"], unmeasured)

    def test_a_malformed_samples_block_is_ignored_not_fatal(self):
        for payload in (
            {"samples": "four", "scores": {}},
            {"samples": {"a": "four"}, "scores": {}},
            {"samples": {"a": True}, "scores": {}},
        ):
            with self.subTest(payload=payload):
                self.assertEqual({}, run_ci.load_baseline_samples(self._write(payload)))

    def test_the_writer_records_what_the_reader_enforces(self):
        """Writer/reader agreement, through the REAL writer — not a copy of it
        retyped in the test, which would agree with the writer by construction
        including when both are wrong. A guard whose writer cannot emit the shape
        its reader demands is a permanent no-op.
        """
        scores = {"temporal-reasoning": 1.0, "overall": 0.95}
        sizes = {"temporal-reasoning": (4, 4), "overall": (24, 24)}
        path = self._write(
            run_ci.retrieval_baseline_payload(
                scores, sizes, MODE_HYBRID, 10, "2026-07-26T00:00:00Z"
            )
        )
        samples = run_ci.load_baseline_samples(path)
        self.assertEqual({"temporal-reasoning": 4, "overall": 24}, samples)
        self.assertEqual([], run_ci.baseline_sample_mismatches(scores, sizes, samples))
        # And the mode/scores halves of the schema still round-trip.
        loaded, mode = load_baseline(path)
        self.assertEqual(MODE_HYBRID, mode)
        self.assertEqual(scores, loaded)


class TestUnmeasuredCopyNamesItsCause(unittest.TestCase):
    """The two causes of `unmeasured` are not interchangeable.

    "we lost questions to a harness error" is the author's infra problem; "the
    baseline was captured on a different denominator" is the maintainer's
    re-snap. Reported identically, whoever reads the log goes hunting the wrong
    fault.
    """

    def test_a_sample_mismatch_says_so(self):
        detail = run_ci.unmeasured_detail(
            "temporal-reasoning", {"temporal-reasoning": (4, 4)}, {"temporal-reasoning": 2}
        )
        self.assertIn("4 questions this run vs 2 in the baseline", detail)

    def test_a_lost_question_still_reports_the_fraction(self):
        detail = run_ci.unmeasured_detail(
            "temporal-reasoning", {"temporal-reasoning": (2, 4)}, {}
        )
        self.assertIn("2/4", detail)

    def test_the_table_never_scores_an_incomparable_row(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            run_ci.print_table(
                {"temporal-reasoning": 0.5},
                {"temporal-reasoning": 1.0},
                0.25,
                {"temporal-reasoning": (4, 4)},
                {"temporal-reasoning": 2},
            )
        out = buf.getvalue()
        self.assertIn("n=2", out)
        self.assertNotIn("REGRESS", out)


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


if __name__ == "__main__":
    unittest.main(verbosity=2)
