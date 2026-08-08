#!/usr/bin/env python3
"""Pins for fixture ageing: the harness must not be able to go blind again.

    python test_fixture_ages.py

`fixture_ages.py --selftest` checks the arithmetic (parsing, anchoring, the DSN
guard, the SQL shape). This file pins the parts that live OUTSIDE that module and
that can silently undo it:

  * the client applies the backdate between ingest and recall, and raises rather
    than degrading to "now" when it cannot;
  * the age regime is recorded on the baseline and enforced as a comparability
    axis, so a regime change cannot arrive as a REGRESSION;
  * absence of the field reads as the historical regime, not as UNKNOWN — the
    difference between a gate that keeps working through the rollout and one that
    takes itself INCONCLUSIVE on every run;
  * the workflow actually turns ageing ON in the scored arm, and does so in the
    capture step TOO. A capture under a different regime than the gate is the
    documented way this class of fix fails.

Stdlib only. No Mesh, no database, no network.
"""

from __future__ import annotations

import json
import re
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import fixture_ages  # noqa: E402
from fixture_ages import (  # noqa: E402
    AGE_MODE_ANCHORED,
    AGE_MODE_NOW,
    BackdateError,
)
from run_ci import (  # noqa: E402
    ARM_BRANCH,
    MODE_HYBRID,
    MODE_UNKNOWN,
    Baseline,
    build_baseline_payload,
    describe_age_sensitivity,
    describe_fixture_ages,
    load_baseline,
    resolve_fixture_age_mode,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github/workflows/memory-bench.yml"
REQUIRED_CONTEXT = "Memory recall gate"


def _result(mode: str | None, ages: dict | None = None, **extra) -> dict:
    r = {"question_id": "q", "question_type": "t", "correct": True, **extra}
    if mode is not None:
        r["fixture_age_mode"] = mode
    if ages is not None:
        r["fixture_ages"] = ages
    return r


class TestAgeModeResolution(unittest.TestCase):
    def test_nothing_reported_reads_as_the_historical_regime(self):
        """A result dict with no age field was measured on an un-aged corpus.

        This is the direction that decides whether the change can roll out at
        all. Reading absence as UNKNOWN would make the age gate refuse on every
        run produced by any code path that has not been taught the field —
        including every test fixture in this repo — so a required check would go
        INCONCLUSIVE at merge and stay there until the last producer was found.
        """
        self.assertEqual(AGE_MODE_NOW, resolve_fixture_age_mode([_result(None)] * 3))
        self.assertEqual(AGE_MODE_NOW, resolve_fixture_age_mode([]))

    def test_one_regime_is_reported_as_itself(self):
        self.assertEqual(
            AGE_MODE_ANCHORED,
            resolve_fixture_age_mode([_result(AGE_MODE_ANCHORED)] * 4),
        )

    def test_a_mixed_run_is_unknown_not_the_majority(self):
        """Two corpus ages pooled into one score is not a comparable number.

        Taking the majority would be the same mis-comparison the gate exists to
        prevent, one level down: the minority's questions would still be scored
        into the total under a label that does not describe them.
        """
        mixed = [_result(AGE_MODE_ANCHORED)] * 3 + [_result(AGE_MODE_NOW)]
        self.assertEqual(MODE_UNKNOWN, resolve_fixture_age_mode(mixed))

    def test_errored_questions_do_not_vote(self):
        """An error dict carries no observability, so it must not make a run mixed."""
        results = [_result(AGE_MODE_ANCHORED), {"question_id": "q2", "error": "boom"}]
        self.assertEqual(AGE_MODE_ANCHORED, resolve_fixture_age_mode(results))


class TestBaselineCarriesTheRegime(unittest.TestCase):
    def test_payload_records_the_measured_regime(self):
        payload = build_baseline_payload(
            [{"overall": 0.9}],
            MODE_HYBRID,
            10,
            {"overall": (24, 24)},
            ARM_BRANCH,
            "",
            fixture_age_mode=AGE_MODE_ANCHORED,
        )
        self.assertEqual(AGE_MODE_ANCHORED, payload["fixture_age_mode"])

    def test_round_trip_through_the_loader(self):
        payload = build_baseline_payload(
            [{"overall": 0.9}], MODE_HYBRID, 10, None, ARM_BRANCH, "",
            fixture_age_mode=AGE_MODE_ANCHORED,
        )
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "b.json"
            p.write_text(json.dumps(payload))
            self.assertEqual(AGE_MODE_ANCHORED, load_baseline(p).fixture_age_mode)

    def test_a_baseline_without_the_field_reads_as_unstated(self):
        """Not as `ingest-now`. The LOADER reports what the file says; the GATE
        is where unstated is interpreted. Collapsing the two here would leave no
        way to tell a pre-field file from one captured un-aged on purpose."""
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "b.json"
            p.write_text(json.dumps({
                "search_mode": MODE_HYBRID, "arm": ARM_BRANCH,
                "n_runs": 3, "scores": {"overall": 0.9},
            }))
            self.assertEqual("", load_baseline(p).fixture_age_mode)

    def test_the_flat_legacy_shape_still_loads(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "b.json"
            p.write_text(json.dumps({"overall": 0.75}))
            b = load_baseline(p)
            self.assertEqual("", b.fixture_age_mode)
            self.assertEqual(0.75, b.scores["overall"])

    def test_the_default_baseline_field_is_the_historical_regime(self):
        """`Baseline` is constructed positionally in places; the default must be
        the regime that was actually in force before this change existed."""
        self.assertEqual("", Baseline({}, MODE_HYBRID, 1, {}).fixture_age_mode)


class TestTheStepLogSaysWhatTheAgesWere(unittest.TestCase):
    """AC1: the log must show the age distribution, not just "ingested N"."""

    def test_un_aged_runs_say_so_in_words(self):
        line = describe_fixture_ages([_result(AGE_MODE_NOW)] * 3)
        self.assertIn(AGE_MODE_NOW, line)
        self.assertIn("NOT", line)

    def test_aged_runs_report_min_median_and_max_in_days(self):
        results = [
            _result(AGE_MODE_ANCHORED, {"n": 42, "min_days": 0.2, "median_days": 10.9, "max_days": 183.1}),
            _result(AGE_MODE_ANCHORED, {"n": 47, "min_days": 0.1, "median_days": 5.9, "max_days": 10.8}),
        ]
        line = describe_fixture_ages(results)
        self.assertIn("89 fixtures", line)          # 42 + 47
        self.assertIn("min=0.10d", line)            # the min ACROSS questions
        self.assertIn("max=183.10d", line)          # the max ACROSS questions
        self.assertRegex(line, r"median-of-medians=\d+\.\d\dd")

    def test_a_claimed_regime_with_no_summary_is_reported_as_missing(self):
        """The failure mode worth naming: the env said `question-anchored` and no
        question recorded ages. Reading that as an aged corpus is how the fix
        would be believed while doing nothing."""
        line = describe_fixture_ages([_result(AGE_MODE_ANCHORED)])
        self.assertIn("no per-question age summary", line)


class TestAgeingAloneDoesNotMeanTheGateMeasuresRecency(unittest.TestCase):
    """The correction that keeps this fix from being over-read.

    mesh-mcp defaults `apply_recency_decay` to FALSE, and only its temporal
    auto-classifier turns it on — over the caller's explicit value. So an aged
    corpus is a precondition for measuring recency, not the measurement. If the
    run header stopped saying which questions the ages reach, a backdated run
    would read as "the gate now enforces recency" for 22 questions where it
    provably does not.
    """

    def setUp(self):
        self.dataset = json.loads(
            (Path(__file__).resolve().parent / "data" / "lme_s_24.json").read_text()
        )

    def test_the_header_names_the_count_and_the_remainder(self):
        line = describe_age_sensitivity(self.dataset)
        self.assertRegex(line, r"\d+ of 24")
        self.assertIn("decay off", line)
        self.assertIn("mirror", line.lower())

    def test_the_shipped_dataset_still_has_age_sensitive_questions(self):
        """If a dataset edit drops both temporal-tripping questions, ageing the
        corpus stops being able to move ANY scored number — the harness would be
        blind again while every age check in this file still passed."""
        from fixture_ages import trips_temporal_profile

        hits = [
            e["question_id"]
            for e in self.dataset
            if trips_temporal_profile(e.get("question", ""))
        ]
        self.assertGreaterEqual(
            len(hits), 1,
            "no question in the dataset trips the temporal profile, so fixture "
            "ages cannot affect the scored gate at all. Either add one, or run "
            "the scored arm with BENCH_APPLY_RECENCY_DECAY=true and re-snap.",
        )

    def test_the_mirror_matches_the_documented_keyword_set(self):
        """The mirror's failure direction that matters: believing a query is
        age-neutral when the server treats it as temporal."""
        from fixture_ages import MCP_TEMPORAL_KEYWORDS, trips_temporal_profile

        for kw in MCP_TEMPORAL_KEYWORDS:
            self.assertTrue(
                trips_temporal_profile(f"something {kw} something"),
                f"the mirror does not fire on its own keyword {kw!r}",
            )
        self.assertFalse(trips_temporal_profile("what is my commute length"))

    def test_the_env_lever_has_three_states(self):
        """`BENCH_APPLY_RECENCY_DECAY` exists so a future age-dependent
        measurement needs no code change — and unset must stay unset, not
        become False, or it silently changes the server-side default."""
        import os

        from fixture_ages import ENV_APPLY_DECAY, resolve_apply_decay

        saved = os.environ.pop(ENV_APPLY_DECAY, None)
        try:
            self.assertIsNone(resolve_apply_decay())
            os.environ[ENV_APPLY_DECAY] = "true"
            self.assertIs(True, resolve_apply_decay())
            os.environ[ENV_APPLY_DECAY] = "false"
            self.assertIs(False, resolve_apply_decay())
            os.environ[ENV_APPLY_DECAY] = "maybe"
            with self.assertRaises(ValueError):
                resolve_apply_decay()
        finally:
            os.environ.pop(ENV_APPLY_DECAY, None)
            if saved is not None:
                os.environ[ENV_APPLY_DECAY] = saved


class TestTheClientAgesBetweenIngestAndRecall(unittest.TestCase):
    """The placement is the correctness property, not an implementation detail.

    `_sweep` deletes the haystack in the `finally` of every attempt and
    `--repeat N` re-ingests per pass, so there is no "after ingest" outside the
    attempt; and `BoostRelevance` stamps `updated_at = NOW()` on every row a
    recall returns, so a backdate applied after the recall is overwritten for
    exactly the rows that were ranked.

    Driven through the real `MeshMemoryClient._backdate` with `fixture_ages.backdate`
    swapped out, so what is pinned is the client's own ordering and error policy.
    """

    def setUp(self):
        # Imported lazily: mesh_client_stdio pulls in the MCP SDK, which the
        # other self-checks in the required job must not depend on.
        try:
            from mesh_client_stdio import MeshMemoryClient
        except ImportError as exc:  # pragma: no cover - CI installs the SDK
            self.skipTest(f"mesh_client_stdio unavailable: {exc}")
        self.Client = MeshMemoryClient
        self._real_backdate = fixture_ages.backdate
        self.calls: list[tuple[str, dict]] = []

        def fake(dsn, stamps, **kw):
            self.calls.append((dsn, stamps))
            return len(stamps)

        fixture_ages.backdate = fake

    def tearDown(self):
        fixture_ages.backdate = self._real_backdate

    def _client(self, **kw):
        return self.Client(question_id="ages-test", **kw)

    def test_ingest_now_does_not_touch_the_database(self):
        c = self._client(age_mode=AGE_MODE_NOW, backdate_dsn="postgres://x@127.0.0.1/y")
        c._backdate(["2023/05/10 (Wed) 01:57"], "2023/06/17 (Sat) 04:02")
        self.assertEqual([], self.calls)
        self.assertIsNone(c.age_summary)

    def test_anchored_stamps_every_session_key(self):
        c = self._client(
            age_mode=AGE_MODE_ANCHORED,
            backdate_dsn="postgres://mesh:mesh@127.0.0.1:5432/mesh",
        )
        dates = ["2023/05/10 (Wed) 01:57", "2023/06/17 (Sat) 04:02"]
        c._backdate(dates, "2023/06/17 (Sat) 04:02")
        self.assertEqual(1, len(self.calls))
        _, stamps = self.calls[0]
        self.assertEqual(
            {f"{c.key_prefix}-s0", f"{c.key_prefix}-s1"}, set(stamps),
            "the keys handed to the backdate must be the keys `_store` wrote, or "
            "the update matches nothing and the corpus keeps its ingest age",
        )
        # 38 days of spread must survive into the stamps themselves.
        gap = stamps[f"{c.key_prefix}-s1"] - stamps[f"{c.key_prefix}-s0"]
        self.assertAlmostEqual(38.0847, gap / timedelta(days=1), places=2)
        self.assertIsNotNone(c.age_summary)
        self.assertGreater(c.age_summary["max_days"], 38.0)

    def test_a_missing_dsn_raises_instead_of_silently_ageing_nothing(self):
        c = self._client(age_mode=AGE_MODE_ANCHORED, backdate_dsn="")
        with self.assertRaises(BackdateError):
            c._backdate(["2023/05/10 (Wed) 01:57"], "2023/06/17 (Sat) 04:02")

    def test_a_missing_question_date_raises(self):
        """The anchor is the whole construction. Without it the only options are
        literal 2023 dates (a common factor, which reorders nothing) or silently
        falling back to "now" — the state being fixed."""
        c = self._client(
            age_mode=AGE_MODE_ANCHORED, backdate_dsn="postgres://mesh@127.0.0.1/mesh"
        )
        with self.assertRaises(BackdateError):
            c._backdate(["2023/05/10 (Wed) 01:57"], "")

    def test_the_decay_flag_is_only_sent_when_named(self):
        """`None` must stay a third state. The MCP tool defaults
        apply_recency_decay to true, so collapsing unset into False would silently
        change the regime of every existing run, and collapsing it into True would
        leave the controls unable to ask for the off case."""
        self.assertIsNone(self._client().apply_recency_decay)
        self.assertIs(True, self._client(apply_recency_decay=True).apply_recency_decay)
        self.assertIs(False, self._client(apply_recency_decay=False).apply_recency_decay)


class TestTheWorkflowActuallyTurnsAgeingOn(unittest.TestCase):
    """A capability nothing invokes measures nothing.

    The specific failure being pinned: `--update-baseline` and the judging run
    are two different steps, and a regime set on only one of them produces a
    floor that is not comparable to the runs judged against it — which is how
    this harness has been bitten before (a baseline captured under
    fixture-isolation the nightlies lacked). The gate would then report
    `age-mode-mismatch` for ever, i.e. required and enforcing nothing.
    """

    def setUp(self):
        if not WORKFLOW.exists():  # pragma: no cover
            self.skipTest(f"{WORKFLOW} not found")
        self.text = WORKFLOW.read_text(encoding="utf-8")

    # An ASSIGNMENT, i.e. a line the runner would export. A comment cannot match:
    # `# Deliberately NO BENCH_BACKDATE_DSN: ...` mentions the key and sets
    # nothing, and counting it made two of these checks read the prose instead of
    # the config. Same trap the invocation matcher in test_gate_blindness.py
    # exists for, one file over.
    ASSIGN = re.compile(r"^\s*BENCH_BACKDATE_DSN:\s*(\S+)", re.M)

    def test_the_assignment_matcher_ignores_prose(self):
        """Positive control on the matcher above, in the direction that broke."""
        self.assertEqual(
            [], self.ASSIGN.findall("          # Deliberately NO BENCH_BACKDATE_DSN: see below\n")
        )
        self.assertEqual(
            ["postgres://mesh@127.0.0.1/mesh"],
            self.ASSIGN.findall("          BENCH_BACKDATE_DSN: postgres://mesh@127.0.0.1/mesh\n"),
        )

    def _step(self, name_fragment: str) -> str:
        """The step block whose `- name:` contains the fragment."""
        blocks = re.split(r"\n      - name: ", self.text)
        for b in blocks[1:]:
            if name_fragment in b.splitlines()[0]:
                return b
        self.fail(f"no step named like {name_fragment!r} in {WORKFLOW.name}")

    def test_the_scored_branch_run_ages_its_fixtures(self):
        step = self._step("Run the recall gate against the branch build")
        self.assertIn(f"BENCH_FIXTURE_AGE_MODE: {AGE_MODE_ANCHORED}", step)
        self.assertTrue(
            self.ASSIGN.findall(step),
            "the scored branch run has no BENCH_BACKDATE_DSN assignment, so its "
            "age mode asks for ages it cannot apply",
        )

    def test_the_capture_uses_THE_SAME_regime_as_the_judging_run(self):
        capture = self._step("Re-snap the branch recall baseline")
        judge = self._step("Run the recall gate against the branch build")

        def mode(block: str) -> str | None:
            m = re.search(r"BENCH_FIXTURE_AGE_MODE:\s*(\S+)", block)
            return m.group(1) if m else None

        self.assertEqual(
            mode(judge), mode(capture),
            "the capture and the judged run must age fixtures identically, or the "
            "committed floor is a measurement of a different corpus than every "
            "run compared against it",
        )
        self.assertIsNotNone(mode(capture))

    def test_the_prod_arm_is_never_handed_a_backdate_dsn(self):
        """Backdating rewrites created_at/updated_at. The prod arm measures the
        live workspace, where that would corrupt real memories' ranking.
        `assert_local_dsn` already refuses a remote host — this is the second
        lock, on the side that decides what gets passed at all."""
        for name in (
            "Run the recall gate (prod canary)",
            "Re-snap the retrieval baseline",
        ):
            try:
                step = self._step(name)
            except AssertionError:
                continue  # that step's name changed; the DSN scan below still covers it
            self.assertEqual([], self.ASSIGN.findall(step), f"{name} must not backdate")

    def test_every_backdate_dsn_in_the_workflow_is_loopback(self):
        """Scans the whole file, so a new step cannot introduce a remote DSN
        without this failing — the enumeration a per-step check cannot give."""
        found = self.ASSIGN.findall(self.text)
        self.assertTrue(found, "no BENCH_BACKDATE_DSN assignments found at all — "
                               "this scan would then pass having checked nothing")
        for dsn in found:
            fixture_ages.assert_local_dsn(dsn.strip("'\""))  # raises if not local

    def test_the_negative_control_runs_without_a_dsn(self):
        """AC3's premise is a corpus born at ingest. Handing that step a DSN
        would leave the YAML unable to express which corpus it measures."""
        step = self._step("Recency control — and what it sees is AGE")
        self.assertEqual([], self.ASSIGN.findall(step.split("run:")[0]))

    def test_both_recency_controls_are_invoked(self):
        self.assertIn("recency_control.py --expect visible", self.text)
        self.assertIn("recency_control.py --expect blind", self.text)


class TestTheGateRefusesRatherThanRegresses(unittest.TestCase):
    """An age-mode mismatch must be INCONCLUSIVE, never REGRESSION.

    Read off the source rather than by running a full gate: the verdict path
    needs a dataset, a live Mesh and an embedder. What matters is pinned exactly
    — that the mismatch branch returns the inconclusive exit and carries a
    reason kind, because a red nobody can clear is how a required check gets
    routed around.
    """

    def test_the_mismatch_branch_returns_inconclusive(self):
        src = (Path(__file__).resolve().parent / "run_ci.py").read_text()
        block = src.split("# ── Age gate:")[1].split("# ── Mode gate:")[0]
        self.assertIn("REASON_AGE_MODE_MISMATCH", block)
        self.assertIn("return EXIT_INCONCLUSIVE", block)
        self.assertNotIn(
            "EXIT_REGRESSION", block,
            "an age-mode mismatch is a comparability failure, not evidence that "
            "this PR made memory worse; going red on it blocks every open PR the "
            "moment the regime changes",
        )

    def test_an_unstated_baseline_is_read_as_the_historical_regime(self):
        src = (Path(__file__).resolve().parent / "run_ci.py").read_text()
        block = src.split("# ── Age gate:")[1].split("# ── Mode gate:")[0]
        self.assertIn(
            "baseline.fixture_age_mode or AGE_MODE_NOW", block,
            "every baseline captured before this field existed WAS measured on an "
            "un-aged corpus, so unstated has a known correct reading; treating it "
            "as 'matches this run' would be the silent mis-comparison the gate "
            "exists to prevent",
        )


class TestBackdateIsExactOrItFails(unittest.TestCase):
    """A partial backdate leaves two age regimes in one corpus.

    No score measured over that is attributable, so `backdate` must reject any
    row count that is not exactly the number of fixtures — in BOTH directions.
    Too few means part of the haystack kept its ingest age; too many means the
    update reached rows this code does not own.
    """

    def test_row_count_must_equal_the_fixture_count(self):
        stamps = {"bench-a-s0": datetime.now(timezone.utc)}
        sql = fixture_ages.build_backdate_sql(stamps)
        self.assertIn("SELECT count(*) FROM u", sql)
        self.assertIn("RETURNING 1", sql)

    def test_both_timestamp_columns_move_together(self):
        """`created_at` drives apply_recency_decay and decayed_relevance;
        `updated_at` drives the recency_weight FTS blend. Setting only one leaves
        a third of the age-dependent surface unmeasurable while looking done."""
        sql = fixture_ages.build_backdate_sql({"bench-a-s0": datetime.now(timezone.utc)})
        self.assertIn("created_at = v.ts", sql)
        self.assertIn("updated_at = v.ts", sql)


if __name__ == "__main__":
    unittest.main(verbosity=2)
