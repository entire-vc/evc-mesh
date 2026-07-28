#!/usr/bin/env python3
"""Why did a prod arm of the memory benchmark end `cancelled`?

`cancelled` is the least informative conclusion GitHub produces. A job that was
legitimately superseded by a newer commit, a job killed by `timeout-minutes`
mid-measurement, and a job evicted from its concurrency group by a SIBLING JOB
OF ITS OWN RUN all report the same word, in the same grey, next to the same
icon. `Memory recall canary (prod)` was in the third state for its entire
existence — 3 of 3 pushes, twice dead at 0s — and nobody noticed, because the
one word it produced is the word everyone has learned to read as weather
(#3ce651a0).

This module is the discriminator. It takes what GitHub records and returns which
of those it was, so the workflow can go red for the two that are defects and stay
quiet for the one that is expected.

## The rules, and why they are these rules

`duration` is NOT a discriminator, though it is the first thing one reaches for.
GitHub stamps `started_at` when a job is QUEUED, not when a runner picks it up,
so a canary that sat pending 9m22s and was then evicted reports the same "9
minutes" as one that measured prod for 9 minutes and hit its ceiling. Measured
on the real incident (run 30334077279).

What separates them is whether the job ever EXECUTED anything:

  * cancelled while pending -> never reached a runner -> no steps with a
    conclusion. It was evicted from the concurrency group.
  * cancelled while running -> `timeout-minutes`, or a human, or a
    `cancel-in-progress` peer -> a trail of finished steps.

And for an eviction, whether anything legitimate could have done it:

  * A group holds one running + one pending member; a third arrival cancels the
    pending one. For a post-deploy canary that is CORRECT — the newer commit is
    the one worth measuring. So an eviction with a newer run already in
    existence is expected.
  * An eviction with NO newer run in existence had no legitimate cause. What
    remains is a sibling job of the same run (the defect this module was written
    for) or an older run's advisory arm re-entering the group.

The newer-run test is bounded at the moment of death (`created_at <=
completed_at`). Without that bound the answer depends on when this code happens
to run: a push arriving between the eviction and the check would relabel a real
defect as an expected supersede, and the alarm would fall silent exactly on a
busy repo. Bounding it also makes the verdict a pure function of recorded
history, which is what makes the replay in `test_classify_cancelled_arm.py` a
control rather than a re-enactment.

Failure is toward alerting throughout. An unreadable run list yields NO
confirmed superseder, and an unconfirmed supersede is not a supersede — a probe
whose empty output reads as "all clear" is the exact fail-open that has already
cost this harness once.
"""

from __future__ import annotations

import argparse
import json
import sys

# The two jobs that measure the shared live endpoint. Both sit in the
# `memory-bench-prod` concurrency group; nothing else in the workflow does.
PROD_ARMS = (
    "Memory recall canary (prod)",
    "LongMemEval-S end-to-end (advisory)",
)

VERDICT_DEFECT = "defect"
VERDICT_SUPERSEDED = "superseded"
VERDICT_NONE = "none"


def executed_steps(job: dict) -> int:
    """Steps that actually reached a conclusion.

    `skipped` does not count as execution: a step skipped by its `if:` proves
    only that the job was evaluated, not that it ran. A job cancelled while
    pending has no steps at all.
    """
    return sum(
        1
        for step in (job.get("steps") or [])
        if step.get("conclusion") not in (None, "skipped")
    )


def cancelled_prod_arms(jobs_payload: dict) -> list[dict]:
    return [
        job
        for job in (jobs_payload.get("jobs") or [])
        if job.get("name") in PROD_ARMS and job.get("conclusion") == "cancelled"
    ]


def superseders_alive_at(runs_payload: dict, run_id: int, at: str | None) -> int:
    """Non-PR runs of this workflow that were newer AND already existed at `at`.

    Run ids are monotonic, so "newer" is a bigger id. `pull_request` runs are
    excluded because their prod arms are skipped by `if:` — they never enter the
    concurrency group, so they can never supersede anything. RFC3339 in UTC sorts
    lexicographically, so the timestamp comparison is a string comparison.
    """
    if not at:
        # No death timestamp means no window to ask about. Refuse rather than
        # widen the question to "ever", which is how the first draft of this
        # scored all three historical defects as harmless.
        return 0
    return sum(
        1
        for run in (runs_payload.get("workflow_runs") or [])
        if run.get("event") != "pull_request"
        and int(run.get("id", 0)) > run_id
        and (run.get("created_at") or "") <= at
    )


def classify(
    jobs_payload: dict,
    runs_payload: dict | None,
    run_id: int,
) -> tuple[str, str, list[dict]]:
    """Return (verdict, human reason, per-arm rows).

    `runs_payload=None` means the run list could not be read. That is not the
    same as "there were no newer runs" in intent, but it is deliberately the
    same in effect: without the list a supersede cannot be confirmed, and this
    must fail toward alerting.
    """
    probe_broken = runs_payload is None
    runs_payload = runs_payload or {}

    arms = cancelled_prod_arms(jobs_payload)
    if not arms:
        return (
            VERDICT_NONE,
            "no cancelled prod arm found in this attempt",
            [],
        )

    rows: list[dict] = []
    reasons: list[str] = []
    verdict = VERDICT_SUPERSEDED

    note = (
        " (the run list was unreadable, so a superseding run could not be"
        " confirmed — this verdict fails toward alerting on purpose)"
        if probe_broken
        else ""
    )

    for job in arms:
        name = job.get("name", "?")
        steps = executed_steps(job)
        completed = job.get("completed_at")
        alive = superseders_alive_at(runs_payload, run_id, completed)
        rows.append(
            {
                "name": name,
                "steps": steps,
                "started_at": job.get("started_at"),
                "completed_at": completed,
                "superseders_alive": alive,
            }
        )

        if steps > 0:
            verdict = VERDICT_DEFECT
            reasons.append(
                f"`{name}` was cancelled after executing {steps} step(s) — it was "
                "RUNNING, so this is a timeout or a mid-run cancel, not a supersede."
            )
        elif alive == 0:
            verdict = VERDICT_DEFECT
            reasons.append(
                f"`{name}` was cancelled before executing a single step, and no "
                f"newer run of this workflow existed at {completed}{note} — nothing "
                "legitimate could have superseded it, so it was evicted from "
                "`memory-bench-prod` by a sibling job of its own run or by an older "
                "run's advisory arm."
            )

    if verdict == VERDICT_SUPERSEDED:
        reasons.append(
            "every cancelled prod arm was still pending and a newer run of this "
            "workflow already existed when it died — ordering (a), expected"
        )

    return verdict, " ".join(reasons), rows


def _load(path: str) -> dict | None:
    if path == "-":
        return json.load(sys.stdin)
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"could not read {path}: {exc}", file=sys.stderr)
        return None


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--jobs-json", required=True, help="GET /actions/runs/{id}/attempts/{n}/jobs")
    ap.add_argument("--runs-json", required=True, help="GET /actions/workflows/{f}/runs")
    ap.add_argument("--run-id", required=True, type=int)
    ap.add_argument("--github-output", default=None, help="append verdict/reason here")
    ap.add_argument("--step-summary", default=None, help="append a markdown table here")
    args = ap.parse_args()

    jobs_payload = _load(args.jobs_json)
    if jobs_payload is None:
        # The job list is the subject of the question, not context for it. If it
        # cannot be read there is nothing to classify and silence would be a lie.
        verdict, reason, rows = (
            VERDICT_DEFECT,
            "probe-unreadable — could not list this run's jobs, so a cancelled prod "
            "arm could not be classified",
            [],
        )
    else:
        verdict, reason, rows = classify(jobs_payload, _load(args.runs_json), args.run_id)

    print(f"verdict: {verdict}")
    print(f"reason: {reason}")
    for row in rows:
        print(
            f"  {row['name']}: steps={row['steps']} "
            f"started={row['started_at']} completed={row['completed_at']} "
            f"superseders_alive={row['superseders_alive']}"
        )

    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as fh:
            fh.write(f"verdict={verdict}\n")
            fh.write(f"reason={reason}\n")

    if args.step_summary:
        with open(args.step_summary, "a", encoding="utf-8") as fh:
            fh.write("### Prod arm cancelled\n\n")
            fh.write("| arm | steps executed | started | completed | superseders alive at death |\n")
            fh.write("|---|---|---|---|---|\n")
            for row in rows:
                fh.write(
                    f"| `{row['name']}` | {row['steps']} | {row['started_at']} | "
                    f"{row['completed_at']} | {row['superseders_alive']} |\n"
                )
            fh.write(f"\n**Verdict:** `{verdict}` — {reason}\n")

    # Always 0: the workflow decides what to do with the verdict. A classifier
    # that also enforces makes "the probe broke" and "the thing is broken"
    # indistinguishable at the call site, which is the disease being cured here.
    return 0


if __name__ == "__main__":
    sys.exit(main())
