#!/usr/bin/env python3
"""Self-checks for the cancelled-prod-arm classifier.

    python scripts/memory-bench/test_classify_cancelled_arm.py

The classifier's dangerous failure is the same as every other guard in this
harness: silence. It says `superseded` for the one cause that must not page, so
anything that widens `superseded` turns the alarm off without turning it red.
Each test below pins one way that could happen.

The centrepiece is REPLAY. `classify_cancelled_arm.py` exists because the prod
canary was cancelled 3 times out of 3 and every death read as weather. A
classifier that only produces NEW verdicts is unfalsifiable, so it is fed the
recorded API payloads of those exact three runs (`data/cancelled_arm_replay/`,
captured verbatim from the GitHub API) and required to reproduce the conclusions
the incident reached by hand — including which of them was legitimate.

That replay already earned its place: it FAILED the first draft. Without the
`created_at <= completed_at` bound the newer-run query answers "as of now", and
re-running it hours later returns 4/3/2 superseders, scoring all three historical
defects as harmless. `test_unbounded_query_would_have_missed_the_incident` pins
that as a negative control, so the bound cannot be removed as tidy-up.
"""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import classify_cancelled_arm as cca  # noqa: E402

DATA = Path(__file__).resolve().parent / "data" / "cancelled_arm_replay"

CANARY = "Memory recall canary (prod)"
ADVISORY = "LongMemEval-S end-to-end (advisory)"


def _load(name: str) -> dict:
    return json.loads((DATA / name).read_text(encoding="utf-8"))


def _jobs(*jobs: dict) -> dict:
    return {"jobs": list(jobs)}


def _job(name: str, conclusion: str, steps: int = 0, completed: str = "2026-07-28T06:22:29Z") -> dict:
    return {
        "name": name,
        "conclusion": conclusion,
        "started_at": "2026-07-28T06:22:29Z",
        "completed_at": completed,
        "steps": [{"name": f"s{i}", "conclusion": "success"} for i in range(steps)],
    }


def _runs(*pairs: tuple[int, str], event: str = "push", branch: str = "main") -> dict:
    return {
        "workflow_runs": [
            {"id": i, "event": event, "created_at": t, "head_branch": branch}
            for i, t in pairs
        ]
    }


def _classify(jobs: dict, runs: dict, run_id: int, ref: str = "main"):
    """Every rule case names the watched run's ref explicitly.

    Before the cross-ref split there was nothing to name, so the tests were
    silent about it — and silence defaulted to "same ref", the answer that never
    pages. The ref is now an argument at every call site so that a test asserting
    `superseded` is visibly asserting *same-ref* supersede.
    """
    return cca.classify(jobs, runs, run_id, run_ref=ref)


class Rules(unittest.TestCase):
    def test_evicted_with_no_superseder_is_a_defect(self):
        # The incident itself: killed before executing anything, with nothing
        # newer in existence that could have done it legitimately.
        verdict, reason, _ = _classify(_jobs(_job(CANARY, "cancelled")), _runs(), 100)
        self.assertEqual(cca.VERDICT_DEFECT, verdict)
        self.assertIn("sibling job of its own run", reason)

    def test_evicted_by_an_older_newer_run_is_expected(self):
        verdict, _, rows = _classify(
            _jobs(_job(CANARY, "cancelled")),
            _runs((200, "2026-07-28T06:22:28Z")),
            100,
        )
        self.assertEqual(cca.VERDICT_SUPERSEDED, verdict)
        self.assertEqual(1, rows[0]["superseders_alive"])

    def test_a_run_created_after_the_death_cannot_have_caused_it(self):
        # The bound. A run that did not exist yet is not an alibi.
        verdict, _, _ = _classify(
            _jobs(_job(CANARY, "cancelled", completed="2026-07-28T06:22:29Z")),
            _runs((200, "2026-07-28T06:30:24Z")),
            100,
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)

    def test_cancelled_while_running_is_a_defect_even_with_a_superseder(self):
        # A timeout reports `cancelled` too. Elapsed time cannot tell it from an
        # eviction (started_at is stamped at QUEUE time), but executed steps can,
        # and a job that was measuring prod when it died is never "superseded".
        verdict, reason, _ = _classify(
            _jobs(_job(CANARY, "cancelled", steps=9)),
            _runs((200, "2026-07-28T06:22:28Z")),
            100,
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)
        self.assertIn("timeout or a mid-run cancel", reason)

    def test_skipped_steps_do_not_count_as_execution(self):
        job = _job(CANARY, "cancelled")
        job["steps"] = [{"name": "gate", "conclusion": "skipped"}]
        verdict, _, rows = _classify(_jobs(job), _runs((200, "2026-07-28T06:22:28Z")), 100)
        self.assertEqual(0, rows[0]["steps"])
        self.assertEqual(cca.VERDICT_SUPERSEDED, verdict)

    def test_pull_request_runs_are_not_superseders(self):
        # Their prod arms are skipped by `if:`, so they never enter the group.
        # Counting them would excuse a real eviction on any busy PR day.
        verdict, _, _ = _classify(
            _jobs(_job(CANARY, "cancelled")),
            _runs((200, "2026-07-28T06:22:28Z"), event="pull_request"),
            100,
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)

    def test_older_run_ids_are_not_superseders(self):
        verdict, _, _ = _classify(
            _jobs(_job(CANARY, "cancelled")),
            _runs((50, "2026-07-28T06:22:28Z")),
            100,
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)

    def test_a_successful_arm_is_not_classified(self):
        verdict, _, rows = _classify(_jobs(_job(CANARY, "success")), _runs(), 100)
        self.assertEqual(cca.VERDICT_NONE, verdict)
        self.assertEqual([], rows)

    def test_non_prod_jobs_are_ignored(self):
        # The required branch arm builds its own postgres and embedder; it is not
        # in the group and its cancellation is a different question.
        verdict, _, _ = _classify(
            _jobs(_job("Memory recall gate", "cancelled", steps=4)), _runs(), 100
        )
        self.assertEqual(cca.VERDICT_NONE, verdict)

    def test_one_defect_among_expected_arms_still_reds_the_run(self):
        verdict, _, _ = _classify(
            _jobs(
                _job(ADVISORY, "cancelled"),
                _job(CANARY, "cancelled", steps=3),
            ),
            _runs((200, "2026-07-28T06:22:28Z")),
            100,
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)


class FailsTowardAlerting(unittest.TestCase):
    def test_unreadable_run_list_is_not_an_alibi(self):
        verdict, reason, _ = cca.classify(_jobs(_job(CANARY, "cancelled")), None, 100)
        self.assertEqual(cca.VERDICT_DEFECT, verdict)
        self.assertIn("unreadable", reason)

    def test_missing_completed_at_is_not_an_alibi(self):
        job = _job(CANARY, "cancelled")
        job["completed_at"] = None
        verdict, _, _ = _classify(_jobs(job), _runs((200, "2026-07-28T06:22:28Z")), 100)
        self.assertEqual(cca.VERDICT_DEFECT, verdict)


class ReplayTheIncident(unittest.TestCase):
    """The positive control: reproduce the OLD numbers, not just produce new ones.

    Ground truth as established by hand in #3ce651a0 — the canary of a run whose
    own advisory sibling queued behind it is a DEFECT; the canary of a run that a
    genuinely newer run superseded is EXPECTED.
    """

    RUNS = None

    @classmethod
    def setUpClass(cls):
        cls.RUNS = _load("runs-all-refs.json")

    def _replay(self, run_id: int, arm: str, ref: str = "main"):
        jobs = _load(f"jobs-{run_id}.json")
        # Classify each arm in isolation so a run with two cancelled arms yields
        # a verdict per arm rather than one merged answer.
        only = {"jobs": [j for j in jobs["jobs"] if j.get("name") == arm]}
        return cca.classify(only, self.RUNS, run_id, run_ref=ref)

    def test_30330437164_canary_killed_by_its_own_sibling(self):
        verdict, _, rows = self._replay(30330437164, CANARY)
        self.assertEqual(0, rows[0]["steps"])
        self.assertEqual(0, rows[0]["superseders_alive"])
        self.assertEqual(cca.VERDICT_DEFECT, verdict)

    def test_30334077279_canary_legitimately_superseded(self):
        # This one is the reason the classifier cannot simply red every
        # `cancelled`: run 30334590080 really did arrive and really was newer.
        verdict, _, rows = self._replay(30334077279, CANARY)
        self.assertEqual(0, rows[0]["steps"])
        self.assertEqual(1, rows[0]["superseders_alive"])
        self.assertEqual(cca.VERDICT_SUPERSEDED, verdict)

    def test_30334590080_canary_killed_by_its_own_sibling(self):
        verdict, _, rows = self._replay(30334590080, CANARY)
        self.assertEqual(0, rows[0]["steps"])
        self.assertEqual(0, rows[0]["superseders_alive"])
        self.assertEqual(cca.VERDICT_DEFECT, verdict)

    def test_30334590080_advisory_superseded_by_the_nightly(self):
        # The same run's advisory outlived its canary by 8 minutes and was then
        # evicted by the 06:30 schedule run — legitimate, and the classifier must
        # not count it as a second defect.
        verdict, _, rows = self._replay(30334590080, ADVISORY)
        self.assertEqual(1, rows[0]["superseders_alive"])
        self.assertEqual(cca.VERDICT_SUPERSEDED, verdict)

    def test_unbounded_query_would_have_missed_the_incident(self):
        """NEGATIVE CONTROL — the first draft, pinned so it cannot come back.

        Drop the `created_at <= completed_at` bound and every historical defect
        scores as an expected supersede, because by the time anyone re-reads the
        list there is always something newer. This test asserts the WRONG answer
        from the WRONG rule, which is what makes the right rule falsifiable.
        """
        orig = cca.superseders_alive_at
        self.addCleanup(lambda: setattr(cca, "superseders_alive_at", orig))
        # "9999" sorts after every RFC3339 timestamp, i.e. "as of whenever this
        # is read" — which is exactly what an unbounded query means.
        cca.superseders_alive_at = lambda runs, run_id, at, ref: orig(runs, run_id, "9999", ref)

        for run_id in (30330437164, 30334590080):
            verdict, _, _ = self._replay(run_id, CANARY)
            self.assertEqual(
                cca.VERDICT_SUPERSEDED,
                verdict,
                f"run {run_id}: expected the unbounded rule to wrongly excuse this",
            )




class CrossRefContention(unittest.TestCase):
    """Ordering (f): the group is repo-scoped, the premise for (a) is not.

    `memory-bench-prod` is a repository-wide concurrency group, so a run on ANY
    ref evicts a pending arm. Only a newer run on the SAME ref carries the
    premise that makes ordering (a) an accepted cost — "the newer commit is the
    one worth measuring". A `workflow_dispatch` on a colleague's feature branch
    is not a newer commit on `main`; it destroys the post-deploy verdict and
    supplies nothing in its place.
    """

    def test_evicted_by_another_ref_is_contended_not_superseded(self):
        verdict, reason, rows = _classify(
            _jobs(_job(CANARY, "cancelled")),
            _runs((200, "2026-07-28T06:22:28Z"), branch="riker/some-experiment"),
            100,
            ref="main",
        )
        self.assertEqual(cca.VERDICT_CONTENDED, verdict)
        self.assertEqual(0, rows[0]["superseders_same_ref"])
        self.assertEqual(1, rows[0]["superseders_foreign_ref"])
        self.assertIn("DIFFERENT ref", reason)

    def test_same_ref_wins_over_a_foreign_one(self):
        # A real supersede does not stop being one because a branch run also
        # happened to be in flight.
        runs = {
            "workflow_runs": [
                {"id": 200, "event": "push", "created_at": "2026-07-28T06:22:28Z",
                 "head_branch": "main"},
                {"id": 201, "event": "workflow_dispatch",
                 "created_at": "2026-07-28T06:22:28Z", "head_branch": "someone/wip"},
            ]
        }
        verdict, _, rows = _classify(_jobs(_job(CANARY, "cancelled")), runs, 100)
        self.assertEqual(cca.VERDICT_SUPERSEDED, verdict)
        self.assertEqual(1, rows[0]["superseders_same_ref"])

    def test_an_unattributable_ref_fails_toward_alerting(self):
        # The watched run absent from the list, no --run-ref: the evictor cannot
        # be attributed to this ref, and unattributable contention is contention.
        verdict, _, _ = cca.classify(
            _jobs(_job(CANARY, "cancelled")),
            _runs((200, "2026-07-28T06:22:28Z")),
            100,
        )
        self.assertEqual(cca.VERDICT_CONTENDED, verdict)

    def test_contended_pages_and_superseded_does_not(self):
        # The guard the workflow actually reads. `must_page` is derived from a
        # positive list, so a verdict added later is loud until someone decides
        # otherwise in writing — the previous `verdict != 'defect'` gate would
        # have made `contended` silent on arrival.
        self.assertNotIn(cca.VERDICT_CONTENDED, cca.QUIET_VERDICTS)
        self.assertNotIn(cca.VERDICT_DEFECT, cca.QUIET_VERDICTS)
        self.assertIn(cca.VERDICT_SUPERSEDED, cca.QUIET_VERDICTS)
        self.assertIn(cca.VERDICT_NONE, cca.QUIET_VERDICTS)


class ReplayTheFirstPostFixPush(unittest.TestCase):
    """Out-of-sample: run 30341031059, the merge of the sibling fix itself.

    Captured live AFTER the fix shipped, from payloads the classifier had never
    seen. Two things are pinned here at once — that the intra-run sibling
    eviction really is gone (the advisory is `skipped`, so it cannot contend),
    and that what killed the canary anyway was a feature-branch dispatch two
    seconds earlier.
    """

    @classmethod
    def setUpClass(cls):
        cls.RUNS = _load("runs-all-refs.json")
        cls.JOBS = _load("jobs-30341031059.json")

    def test_the_sibling_can_no_longer_contend(self):
        advisory = [j for j in self.JOBS["jobs"] if j["name"] == ADVISORY]
        self.assertEqual(1, len(advisory))
        self.assertEqual("skipped", advisory[0]["conclusion"])

    def test_the_canary_was_evicted_across_refs(self):
        only = {"jobs": [j for j in self.JOBS["jobs"] if j["name"] == CANARY]}
        verdict, reason, rows = cca.classify(only, self.RUNS, 30341031059, run_ref="main")
        self.assertEqual(0, rows[0]["steps"])
        self.assertEqual(0, rows[0]["superseders_same_ref"])
        self.assertEqual(1, rows[0]["superseders_foreign_ref"])
        self.assertEqual(cca.VERDICT_CONTENDED, verdict)
        self.assertIn("another branch", reason)

    def test_branch_scoped_run_list_would_invert_this(self):
        """NEGATIVE CONTROL — the rule that shipped, pinned so it cannot return.

        The workflow fetched the run list with `?branch=${GITHUB_REF_NAME}`. A
        cross-ref evictor is then structurally absent from the payload, the count
        comes back 0, and the classifier reports the only mechanism left in its
        enumeration: "evicted by a sibling job of its own run" — which
        `test_the_sibling_can_no_longer_contend` shows to be false of this very
        run. The alert still fired, so the bug was invisible: right outcome,
        provably wrong reason, and one query widening away from going silent.
        """
        branch_scoped = {
            "workflow_runs": [
                r for r in self.RUNS["workflow_runs"] if r.get("head_branch") == "main"
            ]
        }
        only = {"jobs": [j for j in self.JOBS["jobs"] if j["name"] == CANARY]}
        verdict, reason, rows = cca.classify(
            only, branch_scoped, 30341031059, run_ref="main"
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)
        self.assertEqual(0, rows[0]["superseders_alive"])
        self.assertIn("sibling job of its own run", reason)


class _Clock:
    """A clock that only moves when something sleeps.

    Real elapsed time is the one input a test cannot hold still, and the whole
    point of the settling logic is *when* it reads. Injecting the clock is what
    makes "it waited out the grace before the first read" an assertion rather
    than a hope.
    """

    def __init__(self, start: float):
        self.t = float(start)

    def now(self) -> float:
        return self.t

    def sleep(self, seconds: float) -> None:
        self.t += float(seconds)


class _LaggingEndpoint:
    """`/actions/runs` as it actually behaved: the newest row shows up late.

    `responses` is consumed one per call; the last entry repeats for ever, so a
    two-element list means "stale once, then correct". `None` simulates a failed
    read.
    """

    def __init__(self, clock: _Clock, responses: list):
        self.clock = clock
        self.responses = responses
        self.read_at: list[float] = []
        self.calls = 0

    def __call__(self):
        self.read_at.append(self.clock.now())
        payload = self.responses[min(self.calls, len(self.responses) - 1)]
        self.calls += 1
        return payload


def _epoch(stamp: str) -> float:
    return cca._epoch(stamp)


class PayloadFreshness(unittest.TestCase):
    """The race that made this watchdog's FIRST LIVE FIRING wrong (#13fb482b).

    Run 30341031059's canary died at 08:08:18Z, evicted from the repo-scoped
    group by run 30341157927 — a `workflow_dispatch` on a feature branch,
    created at 08:08:16Z, two seconds before the death. The watchdog listed
    `/actions/runs` at ~08:08:30Z and the evictor was **not in the response**.
    So `superseders_alive_at` counted 0, and the classifier printed the only
    mechanism left in its enumeration: "a sibling job of its own run" — about a
    run whose sibling was `skipped` in the same payload it had just read.

    Same code, same run ids, opposite verdict, difference only in the payload.
    That is what "a pure function of recorded history" got wrong: `classify` is
    a pure function of the payload it is HANDED, and a live fetch at the instant
    of death is a function of API propagation.

    The lagged payload is derived from the captured one by removing exactly the
    row that was late — which is what the lag did — rather than hand-written, so
    it cannot drift away from the real capture.
    """

    RUN_ID = 30341031059
    EVICTOR = 30341157927
    DEATH = "2026-07-28T08:08:18Z"

    @classmethod
    def setUpClass(cls):
        cls.FULL = _load("runs-all-refs.json")
        cls.JOBS = _load("jobs-30341031059.json")
        cls.LAGGED = {
            "workflow_runs": [
                r for r in cls.FULL["workflow_runs"] if r.get("id") != cls.EVICTOR
            ]
        }

    def _canary_only(self) -> dict:
        return {"jobs": [j for j in self.JOBS["jobs"] if j["name"] == CANARY]}

    # -- the derivation itself, before anything is concluded from it ---------

    def test_the_lagged_payload_differs_by_exactly_the_late_row(self):
        """POSITIVE CONTROL on the fixture derivation.

        If the removal matched nothing, every test below would be comparing a
        payload with itself and passing while proving nothing — the same
        vacuous-negative-control shape this harness has already paid for twice.
        """
        full = {r["id"] for r in self.FULL["workflow_runs"]}
        lagged = {r["id"] for r in self.LAGGED["workflow_runs"]}
        self.assertEqual({self.EVICTOR}, full - lagged)

    def test_the_late_row_is_a_foreign_ref_dispatch_created_before_the_death(self):
        """Pins the ground truth the whole card rests on, from the capture."""
        row = next(r for r in self.FULL["workflow_runs"] if r["id"] == self.EVICTOR)
        self.assertEqual("workflow_dispatch", row["event"])
        self.assertNotEqual("main", row["head_branch"])
        self.assertGreater(row["id"], self.RUN_ID)
        self.assertLessEqual(row["created_at"], self.DEATH)

    def test_the_death_instant_is_read_off_the_captured_jobs(self):
        self.assertEqual(self.DEATH, cca.latest_death(self._canary_only()))

    # -- AC5: replaying the recorded payloads reproduces both verdicts -------

    def test_the_lagging_payload_reproduces_the_false_alert(self):
        """NEGATIVE CONTROL — the live firing, replayed.

        This is the verdict the watchdog actually published, and it is wrong for
        a reason nothing in its own output revealed.
        """
        verdict, reason, rows = cca.classify(
            self._canary_only(), self.LAGGED, self.RUN_ID, run_ref="main"
        )
        self.assertEqual(cca.VERDICT_DEFECT, verdict)
        self.assertEqual(0, rows[0]["superseders_alive"])
        self.assertIn("sibling job of its own run", reason)

    def test_the_settled_payload_is_contended_not_superseded(self):
        verdict, _, rows = cca.classify(
            self._canary_only(), self.FULL, self.RUN_ID, run_ref="main"
        )
        self.assertEqual(cca.VERDICT_CONTENDED, verdict)
        self.assertEqual(1, rows[0]["superseders_foreign_ref"])
        self.assertTrue(cca.VERDICT_CONTENDED not in cca.QUIET_VERDICTS)

    def test_settling_turns_the_lagged_answer_into_the_true_one(self):
        """END TO END: the fix, driven against the real recorded payloads."""
        clock = _Clock(_epoch(self.DEATH) + 9)  # the watchdog started 9s after
        endpoint = _LaggingEndpoint(clock, [self.LAGGED, self.FULL])
        payload, freshness = cca.fetch_settled_runs(
            endpoint,
            self.RUN_ID,
            self.DEATH,
            now=clock.now,
            sleep=clock.sleep,
        )
        self.assertTrue(freshness["settled"], freshness)
        verdict, reason, rows = cca.classify(
            self._canary_only(), payload, self.RUN_ID, run_ref="main", freshness=freshness
        )
        self.assertEqual(cca.VERDICT_CONTENDED, verdict)
        self.assertEqual(1, rows[0]["superseders_foreign_ref"])
        self.assertIn("another branch", reason)
        self.assertNotIn("sibling job of its own run", reason)

    # -- AC4: the settling behaviour itself ---------------------------------

    def test_no_read_is_taken_before_the_grace_has_elapsed(self):
        """The grace is measured from the DEATH, not from process start.

        Without this, two equally-stale reads 15s apart taken 1s after the death
        agree with each other and the tool calls that settled — agreement on a
        stale payload is exactly as wrong as a single stale read, and more
        convincing.
        """
        death = _epoch(self.DEATH)
        clock = _Clock(death + 2)
        endpoint = _LaggingEndpoint(clock, [self.FULL])
        cca.fetch_settled_runs(
            endpoint, self.RUN_ID, self.DEATH,
            now=clock.now, sleep=clock.sleep, grace_seconds=45,
        )
        self.assertTrue(endpoint.read_at, "the endpoint was never read")
        self.assertGreaterEqual(endpoint.read_at[0] - death, 45)

    def test_a_late_start_does_not_wait_again(self):
        """Grace already satisfied by the time we look ⇒ read immediately."""
        clock = _Clock(_epoch(self.DEATH) + 600)
        endpoint = _LaggingEndpoint(clock, [self.FULL])
        cca.fetch_settled_runs(
            endpoint, self.RUN_ID, self.DEATH, now=clock.now, sleep=clock.sleep,
        )
        self.assertEqual(_epoch(self.DEATH) + 600, endpoint.read_at[0])

    def test_a_failed_read_is_not_agreement(self):
        """Fail-open check: `None` must reset the streak, not confirm it.

        A probe whose empty output reads as "all clear" is the fail-open this
        harness has now paid for five times.
        """
        clock = _Clock(_epoch(self.DEATH) + 100)
        endpoint = _LaggingEndpoint(clock, [self.FULL, None, self.FULL, self.FULL])
        payload, freshness = cca.fetch_settled_runs(
            endpoint, self.RUN_ID, self.DEATH,
            now=clock.now, sleep=clock.sleep, agree_polls=2,
        )
        self.assertTrue(freshness["settled"])
        self.assertEqual(4, endpoint.calls)
        self.assertIs(self.FULL, payload)

    def test_the_last_successful_payload_survives_a_trailing_failure(self):
        clock = _Clock(_epoch(self.DEATH) + 100)
        endpoint = _LaggingEndpoint(clock, [self.FULL, None])
        payload, freshness = cca.fetch_settled_runs(
            endpoint, self.RUN_ID, self.DEATH,
            now=clock.now, sleep=clock.sleep, deadline_seconds=40,
        )
        self.assertFalse(freshness["settled"])
        self.assertIs(self.FULL, payload)

    def test_an_endpoint_that_never_settles_gives_up_and_says_so(self):
        """The deadline exists because the watchdog has a `timeout-minutes`.

        A settler that waits for ever turns a loud wrong answer into silence,
        which is strictly worse.
        """
        clock = _Clock(_epoch(self.DEATH) + 100)
        churn = [
            {"workflow_runs": self.FULL["workflow_runs"]},
            {"workflow_runs": self.LAGGED["workflow_runs"]},
        ] * 20
        endpoint = _LaggingEndpoint(clock, churn)
        payload, freshness = cca.fetch_settled_runs(
            endpoint, self.RUN_ID, self.DEATH,
            now=clock.now, sleep=clock.sleep, deadline_seconds=60, poll_interval=15,
        )
        self.assertFalse(freshness["settled"])
        self.assertIsNotNone(payload)
        self.assertIn("gave up", freshness["detail"])

    def test_settling_can_never_invent_a_superseder_created_after_the_death(self):
        """The bound and the settling are independent, and both must hold.

        Settling only ever ADDS rows. What keeps that safe is the
        `created_at <= death` bound: an added row can only count if it genuinely
        predated the death. Remove the bound and a retry loop becomes a machine
        for excusing every defect, which is the first draft's bug with a longer
        fuse.
        """
        late = {
            "workflow_runs": self.FULL["workflow_runs"]
            + [
                {
                    "id": 99999999999,
                    "event": "push",
                    "head_branch": "main",
                    "created_at": "2026-07-28T08:09:00Z",  # AFTER the 08:08:18Z death
                }
            ]
        }
        clock = _Clock(_epoch(self.DEATH) + 100)
        endpoint = _LaggingEndpoint(clock, [late])
        payload, freshness = cca.fetch_settled_runs(
            endpoint, self.RUN_ID, self.DEATH, now=clock.now, sleep=clock.sleep,
        )
        verdict, _, rows = cca.classify(
            self._canary_only(), payload, self.RUN_ID, run_ref="main", freshness=freshness
        )
        self.assertEqual(0, rows[0]["superseders_same_ref"])
        self.assertEqual(cca.VERDICT_CONTENDED, verdict)

    def test_agreement_is_judged_on_the_runs_that_could_have_caused_the_death(self):
        """Found by mutation: dropping the bound HERE survived every other test.

        `_relevant_ids` is what consecutive reads must agree about. Widen it to
        "all newer runs" and it starts including runs created AFTER the death —
        rows that churn on a busy repo and can never have caused anything — so
        two honest reads disagree and the settler burns its whole deadline
        reporting `settled=false`. That direction is loud rather than silent, so
        it does not fail open; it just makes the freshness signal useless, which
        is how a guard gets deleted as noise a month later.
        """
        base = {
            "workflow_runs": [
                {"id": 200, "event": "push", "head_branch": "main",
                 "created_at": "2026-07-28T08:08:00Z"},
            ]
        }
        after = {
            "workflow_runs": base["workflow_runs"]
            + [{"id": 300, "event": "push", "head_branch": "main",
                "created_at": "2026-07-28T08:09:00Z"}]
        }
        self.assertEqual(
            cca._relevant_ids(base, 100, self.DEATH),
            cca._relevant_ids(after, 100, self.DEATH),
            "a run created after the death changed the agreement projection",
        )
        self.assertEqual((200,), cca._relevant_ids(after, 100, self.DEATH))

    def test_two_reads_differing_only_after_the_death_still_settle(self):
        """The same rule, exercised through the settler rather than the helper."""
        rows = [{"id": 200, "event": "push", "head_branch": "main",
                 "created_at": "2026-07-28T08:08:00Z"}]
        clock = _Clock(_epoch(self.DEATH) + 100)
        endpoint = _LaggingEndpoint(clock, [
            {"workflow_runs": rows},
            {"workflow_runs": rows + [{"id": 300, "event": "push", "head_branch": "main",
                                       "created_at": "2026-07-28T08:30:00Z"}]},
        ])
        _, freshness = cca.fetch_settled_runs(
            endpoint, 100, self.DEATH, now=clock.now, sleep=clock.sleep, agree_polls=2,
        )
        self.assertTrue(freshness["settled"], freshness)
        self.assertEqual(2, endpoint.calls)

    def test_nothing_cancelled_means_nothing_to_wait_for(self):
        clock = _Clock(1000.0)
        endpoint = _LaggingEndpoint(clock, [self.FULL])
        _, freshness = cca.fetch_settled_runs(
            endpoint, self.RUN_ID, None, now=clock.now, sleep=clock.sleep,
        )
        self.assertEqual(1000.0, clock.now(), "it slept with no death to settle against")
        self.assertFalse(freshness["settled"])
        self.assertIn("no death instant", freshness["detail"])


class TheReasonMayNotOverclaim(unittest.TestCase):
    """AC4's other half: a verdict that can flip on replay must not read as fact.

    The published alert said "no newer run of this workflow existed at
    08:08:18Z — nothing legitimate could have superseded it". Every word after
    the dash was an inference from a payload that was missing a row. The tool is
    allowed to say what it saw; it is not allowed to say what was.
    """

    def _defect_reason(self, freshness):
        _, reason, _ = cca.classify(
            _jobs(_job(CANARY, "cancelled")),
            _runs((50, "2026-07-28T06:00:00Z")),  # older ⇒ not a superseder
            100,
            run_ref="main",
            freshness=freshness,
        )
        return reason

    def test_the_old_absolute_claim_is_gone(self):
        for freshness in (None, {"settled": True, "observed_at": "x", "polls": 2, "detail": "d"}):
            reason = self._defect_reason(freshness)
            self.assertNotIn("no newer run of this workflow existed", reason)
            self.assertNotIn("nothing legitimate could have superseded", reason)
            self.assertIn("VISIBLE in the run list", reason)

    def test_an_unsettled_payload_carries_its_own_doubt(self):
        reason = self._defect_reason(
            {"settled": False, "observed_at": "t", "polls": 1, "detail": "gave up after 60s"}
        )
        self.assertIn("could NOT be confirmed settled", reason)
        self.assertIn("propagation lag", reason)
        self.assertIn("claim about the payload", reason)

    def test_a_settled_payload_says_which_read_it_is_talking_about(self):
        reason = self._defect_reason(
            {"settled": True, "observed_at": "2026-07-28T08:09:18Z", "polls": 2, "detail": "d"}
        )
        self.assertIn("confirmed settled", reason)
        self.assertIn("2026-07-28T08:09:18Z", reason)
        self.assertNotIn("could NOT be confirmed", reason)

    def test_no_settling_at_all_is_stated_rather_than_implied(self):
        reason = self._defect_reason(None)
        self.assertIn("no settling was performed", reason)

    def test_freshness_never_changes_the_verdict(self):
        """Purity guard. The replay controls are only controls while the verdict
        depends on nothing but the recorded payloads."""
        args = (_jobs(_job(CANARY, "cancelled")), _runs((50, "2026-07-28T06:00:00Z")), 100)
        verdicts = {
            cca.classify(*args, run_ref="main", freshness=f)[0]
            for f in (
                None,
                {"settled": True, "observed_at": "t", "polls": 2, "detail": "d"},
                {"settled": False, "observed_at": "t", "polls": 9, "detail": "d"},
            )
        }
        self.assertEqual({cca.VERDICT_DEFECT}, verdicts)


if __name__ == "__main__":
    unittest.main(verbosity=2)
