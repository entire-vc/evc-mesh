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
    the one worth measuring. So an eviction with a newer run ON THE SAME REF
    already in existence is expected.
  * `memory-bench-prod` is a REPO-scoped group, so the evictor need not share
    the victim's ref. A newer run on a DIFFERENT ref evicts just as effectively
    while carrying no newer commit for this ref — the premise above never holds,
    so nothing was measured in exchange for the loss. That is its own verdict
    (`contended`), not a supersede.
  * An eviction with NO newer run in existence had no legitimate cause. What
    remains is a sibling job of the same run (the defect this module was written
    for) or an older run's advisory arm re-entering the group.

The ref split is here because the un-split version got it exactly backwards on
the first post-fix push. Run 30341031059 (`main`, the merge of the sibling fix)
had its canary evicted by a `workflow_dispatch` on a feature branch two seconds
earlier. The run list was fetched with `?branch=main`, so the evictor was
invisible, the count came back 0, and the reason printed "evicted by a sibling
job of its own run" — on a run whose sibling was `skipped` in the same payload.
The alert fired for a mechanism its own evidence refuted. Widening the query
alone would have been worse: it turns that page into a silent `superseded`.

The newer-run test is bounded at the moment of death (`created_at <=
completed_at`). Without that bound the answer depends on when this code happens
to run: a push arriving between the eviction and the check would relabel a real
defect as an expected supersede, and the alarm would fall silent exactly on a
busy repo. Only a run that already existed at the instant of death can have
caused it.

## The bound is right; the SOURCE is racy (#13fb482b)

An earlier draft of this docstring claimed the bound "makes the verdict a pure
function of recorded history". It does not, and the difference is not
pedantic — it produced a false alert on this watchdog's first live firing.

`classify()` is a pure function of THE PAYLOAD IT IS HANDED. When that payload
is fetched live, seconds after the death it is being asked about, it is a
function of GitHub API propagation. Run 30341031059's canary died at
08:08:18Z, evicted by run 30341157927 which had been created at 08:08:16Z. The
watchdog listed `/actions/runs` at ~08:08:30Z and the evictor **was not in the
response yet**. Every clause of the newer-run test was satisfied by that run;
the payload simply did not contain it. Re-running the identical code against a
payload fetched later returns the opposite verdict.

The structural part: the run most likely to be the superseder is the NEWEST
one, and the newest one is exactly the row a lagging list endpoint is most
likely to be missing. The race is therefore not a rare tail — it is aimed at
precisely the evidence the verdict turns on.

Two things follow, and both are implemented here:

  * `fetch_settled_runs()` — do not read the list at the instant of death. Wait
    out a settling grace measured FROM the death, then poll until consecutive
    reads agree on the answer-relevant projection. Retrying can only ADD rows,
    and the `created_at <= death` bound means an added row is one that genuinely
    predated the death, so settling can never invent a superseder. It can only
    stop us from missing one.
  * The failure text says what it actually knows. "No newer run was VISIBLE in
    the run list observed at T" is true; "no newer run existed" was not. When
    the payload could not be confirmed settled the reason says so outright, so
    a verdict that may flip on replay is never phrased as a fact about the
    world.

Failure is toward alerting throughout. An unreadable run list yields NO
confirmed superseder, and an unconfirmed supersede is not a supersede — a probe
whose empty output reads as "all clear" is the exact fail-open that has already
cost this harness once.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import time
from datetime import datetime, timezone

# The two jobs that measure the shared live endpoint. Both sit in the
# `memory-bench-prod` concurrency group; nothing else in the workflow does.
PROD_ARMS = (
    "Memory recall canary (prod)",
    "LongMemEval-S end-to-end (advisory)",
)

VERDICT_DEFECT = "defect"
VERDICT_SUPERSEDED = "superseded"
VERDICT_CONTENDED = "contended"
VERDICT_NONE = "none"

# What the watchdog SERVES is "a prod arm died for a reason that is not the one
# documented cost". So the pageable set is named positively, by what belongs in
# it, and the quiet set is the closed enumeration. The predecessor gated the
# alert on `verdict != 'defect'`; adding `contended` to that would have made the
# new verdict silent by default — a negation guard cannot know about an option
# added after it, and the option added after it is always the interesting one.
QUIET_VERDICTS = (VERDICT_NONE, VERDICT_SUPERSEDED)


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


def own_ref(runs_payload: dict, run_id: int) -> str | None:
    """The watched run's own `head_branch`, read from the run list."""
    for run in runs_payload.get("workflow_runs") or []:
        if int(run.get("id", 0)) == run_id:
            return run.get("head_branch")
    return None


def superseders_alive_at(
    runs_payload: dict, run_id: int, at: str | None, ref: str | None
) -> tuple[int, int]:
    """Newer non-PR runs that already existed at `at`, split by ref.

    Returns `(same_ref, foreign_ref)`.

    Run ids are monotonic, so "newer" is a bigger id. `pull_request` runs are
    excluded because their prod arms are skipped by `if:` — they never enter the
    concurrency group, so they can never supersede anything. RFC3339 in UTC sorts
    lexicographically, so the timestamp comparison is a string comparison.

    The split is the point. `memory-bench-prod` is a REPO-scoped concurrency
    group, so a run on any ref can evict a pending arm — but only a newer run on
    the SAME ref carries the premise that makes ordering (a) acceptable ("the
    newer commit is the one worth measuring"). A `workflow_dispatch` on someone's
    feature branch is not a newer commit on `main`; it is contention. Counting
    both as one number is what let a cross-ref eviction be reported as an
    intra-run sibling kill.

    `ref=None` (the run is absent from the list) collapses both counts into
    `foreign_ref`: unattributable contention is still contention, and this must
    fail toward alerting.
    """
    if not at:
        # No death timestamp means no window to ask about. Refuse rather than
        # widen the question to "ever", which is how the first draft of this
        # scored all three historical defects as harmless.
        return (0, 0)
    same = foreign = 0
    for run in runs_payload.get("workflow_runs") or []:
        if run.get("event") == "pull_request":
            continue
        if int(run.get("id", 0)) <= run_id:
            continue
        if (run.get("created_at") or "") > at:
            continue
        if ref is not None and run.get("head_branch") == ref:
            same += 1
        else:
            foreign += 1
    return (same, foreign)


# How long to let the run list settle before believing "nothing newer existed".
# Measured lag on the incident that motivated this: a run created at 08:08:16Z
# was still absent from `/actions/runs` at ~08:08:30Z (>=14s). The grace is
# counted FROM THE DEATH, not from now, because the death is what the answer is
# about. The whole budget has to fit inside the watchdog job's `timeout-minutes`
# with room for checkout and setup — a watchdog that times out is silent, which
# is the failure mode this entire job exists to remove.
SETTLE_GRACE_SECONDS = 45
SETTLE_POLL_INTERVAL = 15
SETTLE_AGREE_POLLS = 2
SETTLE_DEADLINE_SECONDS = 150


def _epoch(rfc3339: str | None) -> float | None:
    """RFC3339 (as GitHub emits it, `...Z`) -> epoch seconds."""
    if not rfc3339:
        return None
    try:
        return datetime.fromisoformat(rfc3339.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None


def latest_death(jobs_payload: dict) -> str | None:
    """The last instant a cancelled prod arm died — what the query is about."""
    stamps = [
        job.get("completed_at")
        for job in cancelled_prod_arms(jobs_payload)
        if job.get("completed_at")
    ]
    return max(stamps) if stamps else None


def _relevant_ids(runs_payload: dict, run_id: int, at: str | None) -> tuple[int, ...]:
    """The projection the verdict actually depends on.

    Two payloads that agree here produce the same verdict, so this — not the
    whole response — is what consecutive polls must agree about. Comparing raw
    payloads would never converge: `/actions/runs` carries live per-run status
    that changes between any two reads.
    """
    if not at:
        return ()
    return tuple(
        sorted(
            int(run.get("id", 0))
            for run in (runs_payload.get("workflow_runs") or [])
            if run.get("event") != "pull_request"
            and int(run.get("id", 0)) > run_id
            and (run.get("created_at") or "") <= at
        )
    )


def fetch_settled_runs(
    fetch,
    run_id: int,
    death: str | None,
    *,
    now=time.time,
    sleep=time.sleep,
    grace_seconds: int = SETTLE_GRACE_SECONDS,
    poll_interval: int = SETTLE_POLL_INTERVAL,
    agree_polls: int = SETTLE_AGREE_POLLS,
    deadline_seconds: int = SETTLE_DEADLINE_SECONDS,
) -> tuple[dict | None, dict]:
    """Read the run list only once it has plausibly caught up with `death`.

    Returns `(payload, freshness)`. `fetch()` is injected so the settling logic
    is testable against a simulated lagging endpoint instead of only against a
    real one — the same reason the classifier is a script and not thirty lines
    of YAML.

    Two independent conditions, and both are needed:

      * a GRACE measured from `death`. Polling immediately and calling two
        equally-stale reads "agreement" is the failure this guards; the endpoint
        is consistently stale for seconds, so two reads 15s apart taken 1s after
        the death agree with each other and are both wrong.
      * AGREEMENT on `_relevant_ids` across consecutive successful reads. The
        grace alone is a guess about propagation; agreement is evidence.

    A failed read is NOT agreement and does not extend the streak — it resets
    it, and the last SUCCESSFUL payload is what is returned. Treating a failure
    as a quiet confirmation is the fail-open shape this harness keeps paying for.

    `settled=False` is not an error and does not by itself change the verdict.
    It changes what the verdict is allowed to CLAIM: an unsettled payload can
    still say "nothing newer was visible", it just may not say "nothing newer
    existed".
    """
    # A zero interval would spin without ever advancing a fake clock, and would
    # hammer the API with a real one. The floor is not cosmetic.
    poll_interval = max(1, poll_interval)
    started = now()
    freshness: dict = {
        "settled": False,
        "polls": 0,
        "observed_at": None,
        "death": death,
        "grace_seconds": grace_seconds,
        "agree_polls": agree_polls,
        "deadline_seconds": deadline_seconds,
        "waited_seconds": 0,
        "detail": "",
    }

    if not death:
        # Nothing died, so there is no instant to be fresh with respect to.
        # Read once and say plainly that no settling was attempted.
        payload = fetch()
        freshness["polls"] = 1
        freshness["detail"] = "no cancelled prod arm — no death instant to settle against"
        return payload, freshness

    death_epoch = _epoch(death)
    target = (death_epoch + grace_seconds) if death_epoch is not None else None

    last_ok: dict | None = None
    last_ids: tuple[int, ...] | None = None
    streak = 0

    while True:
        if target is not None and now() < target:
            # Sleep out the REMAINING grace in one go, not one poll_interval of
            # it. Sleeping a slice and then reading anyway is how a first draft
            # of this took its first read 17s after a death it was told to give
            # 45s — the grace was configured, logged, and not actually applied.
            # Capped by the deadline so a long grace can never eat the whole
            # budget and leave no read at all.
            sleep(max(0.0, min(target - now(), deadline_seconds - (now() - started))))
        elif freshness["polls"]:
            sleep(poll_interval)

        payload = fetch()
        freshness["polls"] += 1
        freshness["waited_seconds"] = round(now() - started)

        if payload is None:
            streak = 0
            last_ids = None
        else:
            last_ok = payload
            ids = _relevant_ids(payload, run_id, death)
            streak = streak + 1 if ids == last_ids else 1
            last_ids = ids
            past_grace = target is None or now() >= target
            if streak >= agree_polls and past_grace:
                freshness["settled"] = True
                freshness["observed_at"] = _stamp(now())
                freshness["detail"] = (
                    f"{streak} consecutive reads agreed on the answer-relevant runs, "
                    f"the first taken at least {grace_seconds}s after the death"
                )
                return last_ok, freshness

        if now() - started >= deadline_seconds:
            freshness["observed_at"] = _stamp(now())
            freshness["detail"] = (
                f"gave up after {freshness['waited_seconds']}s and "
                f"{freshness['polls']} read(s) without {agree_polls} agreeing "
                f"reads past the {grace_seconds}s grace"
            )
            return last_ok, freshness


def _stamp(epoch: float) -> str:
    return datetime.fromtimestamp(epoch, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _visibility_clause(freshness: dict | None) -> str:
    """How much the "nothing newer" branch is entitled to claim.

    The verdict is a function of the payload. When that payload was confirmed
    settled, saying so is what upgrades "not visible" to something worth
    alerting on. When it was not, the sentence has to carry its own doubt — a
    verdict that flips on replay must not be phrased as a fact about the world.
    """
    if not freshness:
        return (
            " This is a statement about the run list as supplied, not about "
            "history: no settling was performed, so a run that existed but had "
            "not yet propagated to the list endpoint would be invisible here."
        )
    if freshness.get("settled"):
        return (
            f" The run list was confirmed settled first (observed "
            f"{freshness.get('observed_at')}, {freshness.get('polls')} read(s), "
            f"{freshness.get('detail')}), so this is not the propagation lag that "
            "made the first firing of this watchdog wrong."
        )
    return (
        f" ⚠ The run list could NOT be confirmed settled ({freshness.get('detail')}), "
        "so 'not visible' may be API propagation lag rather than absence — the run "
        "most likely to be the superseder is the newest one, which is exactly the "
        "row a lagging list endpoint is most likely to be missing. Treat this "
        "verdict as a claim about the payload; replaying it later may flip it."
    )


def classify(
    jobs_payload: dict,
    runs_payload: dict | None,
    run_id: int,
    run_ref: str | None = None,
    freshness: dict | None = None,
) -> tuple[str, str, list[dict]]:
    """Return (verdict, human reason, per-arm rows).

    `runs_payload=None` means the run list could not be read. That is not the
    same as "there were no newer runs" in intent, but it is deliberately the
    same in effect: without the list a supersede cannot be confirmed, and this
    must fail toward alerting.

    `freshness` is the metadata from `fetch_settled_runs()`. It never changes
    the verdict — the verdict stays a pure function of `(jobs_payload,
    runs_payload, run_id, run_ref)`, which is what keeps the replay tests a
    control. It changes only what the reason is allowed to assert.
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
    ref = run_ref or own_ref(runs_payload, run_id)

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
        same, foreign = superseders_alive_at(runs_payload, run_id, completed, ref)
        rows.append(
            {
                "name": name,
                "steps": steps,
                "started_at": job.get("started_at"),
                "completed_at": completed,
                "superseders_alive": same + foreign,
                "superseders_same_ref": same,
                "superseders_foreign_ref": foreign,
            }
        )

        if steps > 0:
            verdict = VERDICT_DEFECT
            reasons.append(
                f"`{name}` was cancelled after executing {steps} step(s) — it was "
                "RUNNING, so this is a timeout or a mid-run cancel, not a supersede."
            )
        elif same > 0:
            # Ordering (a) with its premise intact: a newer commit on this same
            # ref. The only cause that must not page.
            continue
        elif foreign > 0:
            # Ordering (f). The group is repo-scoped, so a run on another ref
            # evicts a pending arm just as effectively — but it carries no newer
            # commit for this ref, so "the newer commit is the one worth
            # measuring" does not hold and the loss is not paid for.
            if verdict != VERDICT_DEFECT:
                verdict = VERDICT_CONTENDED
            reasons.append(
                f"`{name}` was cancelled before executing a single step. {foreign} "
                f"newer run(s) on a DIFFERENT ref were visible at {completed}{note}, and "
                f"none on `{ref}` itself — so it was evicted from the repo-scoped "
                "`memory-bench-prod` group by work on another branch. That is "
                "ordering (f): a real eviction with no newer commit on this ref to "
                "justify it, so this ref's canary produced no verdict and nothing "
                "was measured in exchange."
            )
        else:
            verdict = VERDICT_DEFECT
            reasons.append(
                f"`{name}` was cancelled before executing a single step, and no "
                f"newer run of this workflow was VISIBLE in the run list at "
                f"{completed}{note} — so the remaining causes are a sibling job of "
                "its own run or an older run's advisory arm re-entering "
                f"`memory-bench-prod`.{_visibility_clause(freshness)}"
            )

    if verdict == VERDICT_SUPERSEDED:
        reasons.append(
            "every cancelled prod arm was still pending and a newer run of this "
            "workflow on the same ref was already visible when it died — ordering "
            "(a), expected"
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


def gh_runs_fetcher(repo: str, workflow: str, per_page: int = 100):
    """A `fetch()` for `fetch_settled_runs` backed by `gh api`.

    Fixed argv, no shell — the arguments are workflow-controlled today, but a
    probe that interpolates into a shell is a shape this repo has agreed not to
    write. A non-zero exit or unparseable body returns `None`, which the settler
    counts as a failed read (never as agreement) and `classify` treats as "no
    confirmed superseder".
    """

    def fetch() -> dict | None:
        path = f"repos/{repo}/actions/workflows/{workflow}/runs?per_page={per_page}"
        try:
            out = subprocess.run(
                ["gh", "api", path],
                capture_output=True,
                text=True,
                timeout=60,
                check=False,
            )
        except (OSError, subprocess.SubprocessError) as exc:
            print(f"run-list fetch failed: {exc}", file=sys.stderr)
            return None
        if out.returncode != 0:
            print(f"run-list fetch exited {out.returncode}: {out.stderr[-400:]}", file=sys.stderr)
            return None
        try:
            return json.loads(out.stdout)
        except json.JSONDecodeError as exc:
            print(f"run-list body was not JSON: {exc}", file=sys.stderr)
            return None

    return fetch


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--jobs-json", required=True, help="GET /actions/runs/{id}/attempts/{n}/jobs")
    ap.add_argument(
        "--runs-json",
        default=None,
        help="GET /actions/workflows/{f}/runs, pre-fetched. Replay/offline mode: no "
        "settling is performed and the reason says so. Live callers should pass "
        "--settle-repo instead.",
    )
    ap.add_argument(
        "--settle-repo",
        default=None,
        help="OWNER/REPO — fetch the run list here, with settling, instead of "
        "reading --runs-json. This is the live path.",
    )
    ap.add_argument("--settle-workflow", default="memory-bench.yml")
    ap.add_argument("--settle-grace-seconds", type=int, default=SETTLE_GRACE_SECONDS)
    ap.add_argument("--settle-poll-interval", type=int, default=SETTLE_POLL_INTERVAL)
    ap.add_argument("--settle-agree-polls", type=int, default=SETTLE_AGREE_POLLS)
    ap.add_argument("--settle-deadline-seconds", type=int, default=SETTLE_DEADLINE_SECONDS)
    ap.add_argument("--run-id", required=True, type=int)
    ap.add_argument(
        "--run-ref",
        default=None,
        help="head ref of the watched run (github.ref_name); inferred from the run "
        "list when omitted",
    )
    ap.add_argument("--github-output", default=None, help="append verdict/reason here")
    ap.add_argument("--step-summary", default=None, help="append a markdown table here")
    args = ap.parse_args()

    if not args.runs_json and not args.settle_repo:
        ap.error("one of --runs-json (replay) or --settle-repo (live) is required")

    jobs_payload = _load(args.jobs_json)
    freshness: dict | None = None
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
        if args.settle_repo:
            runs_payload, freshness = fetch_settled_runs(
                gh_runs_fetcher(args.settle_repo, args.settle_workflow),
                args.run_id,
                latest_death(jobs_payload),
                grace_seconds=args.settle_grace_seconds,
                poll_interval=args.settle_poll_interval,
                agree_polls=args.settle_agree_polls,
                deadline_seconds=args.settle_deadline_seconds,
            )
        else:
            runs_payload = _load(args.runs_json)
        verdict, reason, rows = classify(
            jobs_payload, runs_payload, args.run_id, args.run_ref, freshness
        )

    must_page = verdict not in QUIET_VERDICTS

    settled = bool(freshness and freshness.get("settled"))

    print(f"verdict: {verdict}")
    print(f"must_page: {str(must_page).lower()}")
    print(f"payload_settled: {str(settled).lower()}")
    if freshness:
        print(
            f"  run list: polls={freshness['polls']} waited={freshness['waited_seconds']}s "
            f"observed_at={freshness['observed_at']} death={freshness['death']} "
            f"— {freshness['detail']}"
        )
    print(f"reason: {reason}")
    for row in rows:
        print(
            f"  {row['name']}: steps={row['steps']} "
            f"started={row['started_at']} completed={row['completed_at']} "
            f"superseders_alive={row['superseders_alive']} "
            f"(same_ref={row.get('superseders_same_ref')} "
            f"foreign_ref={row.get('superseders_foreign_ref')})"
        )

    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as fh:
            fh.write(f"verdict={verdict}\n")
            fh.write(f"must_page={str(must_page).lower()}\n")
            fh.write(f"payload_settled={str(settled).lower()}\n")
            fh.write(f"reason={reason}\n")

    if args.step_summary:
        with open(args.step_summary, "a", encoding="utf-8") as fh:
            fh.write("### Prod arm cancelled\n\n")
            fh.write(
                "| arm | steps executed | started | completed | superseders "
                "(same ref) | superseders (other ref) |\n"
            )
            fh.write("|---|---|---|---|---|---|\n")
            for row in rows:
                fh.write(
                    f"| `{row['name']}` | {row['steps']} | {row['started_at']} | "
                    f"{row['completed_at']} | {row.get('superseders_same_ref')} | "
                    f"{row.get('superseders_foreign_ref')} |\n"
                )
            fh.write(f"\n**Verdict:** `{verdict}` — {reason}\n")
            if freshness:
                fh.write(
                    f"\n**Run-list freshness:** settled=`{str(settled).lower()}`, "
                    f"{freshness['polls']} read(s) over {freshness['waited_seconds']}s, "
                    f"observed {freshness['observed_at']} against a death at "
                    f"{freshness['death']} — {freshness['detail']}\n"
                )

    # Always 0: the workflow decides what to do with the verdict. A classifier
    # that also enforces makes "the probe broke" and "the thing is broken"
    # indistinguishable at the call site, which is the disease being cured here.
    return 0


if __name__ == "__main__":
    sys.exit(main())
