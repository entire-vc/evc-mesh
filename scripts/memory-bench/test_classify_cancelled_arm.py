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


if __name__ == "__main__":
    unittest.main(verbosity=2)
