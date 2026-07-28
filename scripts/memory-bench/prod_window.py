#!/usr/bin/env python3
"""Is the shared production endpoint free for a memory-bench run?

    python scripts/memory-bench/prod_window.py            # 0 clear / 1 busy / 2 probe failed
    python scripts/memory-bench/prod_window.py --selftest

Every prod-facing job in `memory-bench.yml` ingests a haystack into the single
`secrets.MESH_API_URL` and searches it. They do not queue behind each other by
default, they overlap *inside the server*: a pass that takes ~17 min alone took
30m16s with three others in the window and was killed by `timeout-minutes: 30`
at 14 of 15 checks green (README "way 7"). The `concurrency:` block now
serialises the prod arms with each other, but it cannot serialise a human
deciding to `workflow_dispatch` a proof run on top of someone's required check.
This is the probe for that decision.

Two properties, both learned the hard way on #2a079432:

**A probe that fails must not read as "clear".** The first version of this check
was a shell one-liner whose Python fragment had a syntax error. It printed
nothing, `[ -n "$busy" ]` was false, and the caller read the empty output as an
empty window — dispatching into three live runs, which is the exact incident the
probe was written to prevent. "Zero runs in flight" and "I could not count the
runs in flight" are different facts and empty output renders them identically.
So: exit **0 = clear, 1 = busy, 2 = PROBE FAILED**, and anything the probe cannot
resolve is busy, never clear.

**The job NAME does not identify the arm.** `Memory recall gate` is required, and
a required context is matched as a literal string, so the branch arm had to
inherit that name (README "Why the branch job inherited the old job's name"). The
consequence lands here: the same name means the *prod* arm on a ref carrying the
old one-arm workflow, and the *branch* arm — an ephemeral `cmd/api` over a
throwaway `postgres:16`, contending for nothing — on a ref carrying the new one.
A name-blind probe therefore reports BUSY for a run that cannot touch prod, and
while any PR of this file is iterating, the window is never clear and the probe
is useless in the direction it is consulted.

Resolved without a second API call: a run whose job list contains
`Memory recall canary (prod)` is running the two-arm workflow, so its
`Memory recall gate` is the branch arm. A run without that job is running the
one-arm workflow, where `Memory recall gate` *is* the prod arm. The evidence is
in the jobs list either way.
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys

REPO = "entire-vc/evc-mesh"
WORKFLOW = "memory-bench.yml"

EXIT_CLEAR = 0
EXIT_BUSY = 1
EXIT_PROBE_FAILED = 2

# Jobs that ingest into and search the shared live endpoint.
PROD_CANARY = "Memory recall canary (prod)"
ADVISORY = "LongMemEval-S end-to-end (advisory)"
# Required, and deliberately ambiguous — see the module docstring.
AMBIGUOUS = "Memory recall gate"

# GitHub's jobs API reports status in {queued, in_progress, completed}. Only
# `completed` is terminal; a skipped job is completed with conclusion=skipped.
TERMINAL = "completed"


class ProbeFailed(Exception):
    """The probe could not establish the window's state. Never means 'clear'."""


def prod_job_names(job_names: list[str]) -> set[str]:
    """Which of this run's jobs target the shared prod endpoint.

    `job_names` is every job in the run, whatever its status — the *presence* of
    the canary job identifies the workflow version, so it must be read from the
    full list and not from the live subset.
    """
    prod = {PROD_CANARY, ADVISORY}
    if PROD_CANARY not in job_names:
        # One-arm workflow: nothing builds the branch, so the required check is
        # itself the prod consumer.
        prod.add(AMBIGUOUS)
    return prod


def live_prod_jobs(jobs: list[dict]) -> list[str]:
    """Names of jobs in this run that are live AND target prod.

    An empty job list is not an idle run — it is a run whose jobs have not been
    created yet. Unresolvable ⇒ busy, so it is reported as such by the caller.
    """
    names = [j["name"] for j in jobs]
    prod = prod_job_names(names)
    return [j["name"] for j in jobs if j["name"] in prod and j.get("status") != TERMINAL]


def classify(runs: list[dict]) -> tuple[bool, list[str]]:
    """(busy, human-readable reasons) for in-flight runs with their jobs attached.

    Each run is {"databaseId", "headBranch", "status", "jobs": [...]}.
    """
    reasons: list[str] = []
    for run in runs:
        if run.get("status") == TERMINAL:
            continue
        jobs = run.get("jobs")
        if not jobs:
            reasons.append(
                f"{run['databaseId']} {run.get('headBranch', '?')}: jobs not resolvable "
                f"— counted as busy"
            )
            continue
        live = live_prod_jobs(jobs)
        if live:
            reasons.append(
                f"{run['databaseId']} {run.get('headBranch', '?')}: {', '.join(sorted(live))}"
            )
    return bool(reasons), reasons


def _gh(args: list[str]) -> str:
    if shutil.which("gh") is None:
        raise ProbeFailed("gh is not on PATH")
    proc = subprocess.run(
        ["gh", *args], capture_output=True, text=True, timeout=90, check=False
    )
    if proc.returncode != 0:
        raise ProbeFailed(f"gh {' '.join(args)} exited {proc.returncode}: {proc.stderr.strip()}")
    return proc.stdout


def _json(raw: str, what: str):
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ProbeFailed(f"{what}: unparseable response ({exc})") from exc


def _negative_control() -> None:
    """The same query with a value that cannot match must return nothing.

    An ignored filter returns the unfiltered set or an empty one depending on the
    endpoint, and either way a green here would be uninformative. Asserting that
    a bogus value yields zero rows is what makes the real query's zero mean
    something.
    """
    raw = _gh([
        "run", "list", "--repo", REPO, "--workflow", WORKFLOW, "--limit", "20",
        "--json", "databaseId,status",
        "--jq", '[.[]|select(.status=="__nonesuch__")|.databaseId]',
    ])
    rows = _json(raw or "[]", "negative control")
    if rows:
        raise ProbeFailed(f"negative control returned rows ({rows}) — the filter is ignored")


def probe() -> tuple[int, list[str]]:
    _negative_control()
    raw = _gh([
        "run", "list", "--repo", REPO, "--workflow", WORKFLOW, "--limit", "20",
        "--json", "databaseId,headBranch,status",
        "--jq", '[.[]|select(.status!="completed")]',
    ])
    in_flight = _json(raw or "[]", "run list")
    for run in in_flight:
        jraw = _gh([
            "api", f"repos/{REPO}/actions/runs/{run['databaseId']}/jobs",
            "--jq", "[.jobs[]|{name,status,conclusion}]",
        ])
        run["jobs"] = _json(jraw or "[]", f"jobs of {run['databaseId']}")
    busy, reasons = classify(in_flight)
    return (EXIT_BUSY if busy else EXIT_CLEAR), reasons


# --------------------------------------------------------------------------- #
# self-checks
# --------------------------------------------------------------------------- #

def _run_selftest() -> int:
    failures: list[str] = []

    def check(label: str, got, want):
        if got != want:
            failures.append(f"{label}: got {got!r}, want {want!r}")

    branch_arm_run = {
        "databaseId": 1, "headBranch": "feat/x", "status": "in_progress",
        "jobs": [
            {"name": AMBIGUOUS, "status": "in_progress", "conclusion": None},
            {"name": PROD_CANARY, "status": "completed", "conclusion": "skipped"},
            {"name": ADVISORY, "status": "completed", "conclusion": "skipped"},
        ],
    }
    # THE case this probe exists for: the required check is live, but on the
    # two-arm workflow it is the branch arm and touches nothing shared.
    check("branch arm is not prod", classify([branch_arm_run]), (False, []))

    one_arm_run = {
        "databaseId": 2, "headBranch": "main", "status": "in_progress",
        "jobs": [
            {"name": AMBIGUOUS, "status": "in_progress", "conclusion": None},
            {"name": ADVISORY, "status": "completed", "conclusion": "skipped"},
        ],
    }
    # Same job NAME, same status, opposite verdict — the discrimination is the
    # absence of the canary job, not anything about the gate job itself.
    busy, reasons = classify([one_arm_run])
    check("one-arm gate IS prod", busy, True)
    check("one-arm reason names the job", AMBIGUOUS in (reasons[0] if reasons else ""), True)

    live_canary = {
        "databaseId": 3, "headBranch": "main", "status": "in_progress",
        "jobs": [
            {"name": AMBIGUOUS, "status": "completed", "conclusion": "success"},
            {"name": PROD_CANARY, "status": "in_progress", "conclusion": None},
        ],
    }
    check("live canary is prod", classify([live_canary])[0], True)

    queued_canary = {
        "databaseId": 4, "headBranch": "main", "status": "queued",
        "jobs": [
            {"name": PROD_CANARY, "status": "queued", "conclusion": None},
        ],
    }
    check("queued canary is prod", classify([queued_canary])[0], True)

    # Unresolvable ⇒ busy. This is the fail-CLOSED property; if it ever flips,
    # the probe reports an empty window whenever the API is slow to create jobs.
    no_jobs = {"databaseId": 5, "headBranch": "main", "status": "queued", "jobs": []}
    check("jobs-not-yet-created is busy", classify([no_jobs])[0], True)

    completed = {
        "databaseId": 6, "headBranch": "main", "status": "completed",
        "jobs": [{"name": PROD_CANARY, "status": "completed", "conclusion": "success"}],
    }
    check("completed run is ignored", classify([completed]), (False, []))

    check("empty window is clear", classify([]), (False, []))

    # Mixed: the branch-arm run must not mask a genuinely busy peer, and the
    # reason list must name only the peer.
    busy, reasons = classify([branch_arm_run, one_arm_run])
    check("mixed window is busy", busy, True)
    check("mixed names exactly one run", len(reasons), 1)
    check("mixed blames the right run", reasons[0].startswith("2 "), True)

    if failures:
        print("prod_window selftest FAILED:", file=sys.stderr)
        for f in failures:
            print(f"  ✗ {f}", file=sys.stderr)
        return 1
    print("prod_window selftest OK (9 checks)")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--selftest", action="store_true", help="run the offline self-checks")
    args = ap.parse_args()

    if args.selftest:
        return _run_selftest()

    try:
        code, reasons = probe()
    except ProbeFailed as exc:
        # Deliberately not EXIT_BUSY: a caller that retries on busy would spin
        # forever on a broken probe, and one that treats non-zero as busy is
        # still safe. What must never happen is EXIT_CLEAR.
        print(f"PROBE FAILED: {exc}", file=sys.stderr)
        return EXIT_PROBE_FAILED

    if code == EXIT_BUSY:
        print("BUSY — the shared prod endpoint has live consumers:")
        for r in reasons:
            print(f"  {r}")
    else:
        print("CLEAR")
    return code


if __name__ == "__main__":
    sys.exit(main())
