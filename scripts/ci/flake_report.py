#!/usr/bin/env python3
"""Read-only flake report: jobs that failed and then passed on a retry of the same SHA.

Groups every job of every pipeline updated in the window by (sha, job name).
A group with both a failed and a success attempt is a flake. Output: one row per
job name with the number of flaked SHAs, sorted descending. With --causes it also
reads the log of one failed attempt per flaked SHA and counts failure signatures, so
the cause is taken from the job log and not guessed from "it passed on retry".
`hold-gate` fails on purpose while a merge request carries a hold label; it is listed
apart and is not a flake.

    python3 scripts/ci/flake_report.py [--days 14] [--project entire-vc/evc-mesh]
"""
import argparse
import collections
import datetime
import json
import re
import subprocess
import sys
import urllib.parse


def api(path):
    out = subprocess.run(["glab", "api", path], capture_output=True, text=True, check=True).stdout
    return json.loads(out)


def paged(path):
    page = 1
    while True:
        sep = "&" if "?" in path else "?"
        items = api(f"{path}{sep}per_page=100&page={page}")
        if not items:
            return
        yield from items
        if len(items) < 100:
            return
        page += 1


INTENTIONAL = {"hold-gate"}
_ANSI = re.compile(r"\x1b\[[0-9;]*[mK]")
_SIGNATURES = [
    ("hold label", re.compile(r"carries a hold label")),
    # Infra causes first: a full disk or a dead database also fails whatever test was running.
    # Anchored to the error shape, not the bare phrase: a test may print these on purpose.
    ("runner disk full", re.compile(r"SQLSTATE 53100|\b(?:Error response from daemon|mkdir|write|failed to)\b[^\n]*no space left on device|\bENOSPC:"), "no space left on device"),
    ("runner disk full", re.compile(r"\[ci-disk-gate\] REFUSING"), "ci-disk-gate refusal"),
    ("postgres auth failed", re.compile(r"SQLSTATE 28P01"), "28P01"),
    # go-test-stall-watchdog.sh exits 124 when `go test` makes no progress; the job then
    # ends with a bare "exit code 1" because coverage_artifact.py swallows the status.
    ("go test stalled", re.compile(r"go-test-stall-watchdog\.sh[^\n]*returned non-zero exit status 124"), "watchdog exit 124"),
    ("docker daemon error", re.compile(r"\(system failure\): Error response from daemon: (.+)")),
    ("go test failure", re.compile(r"--- FAIL: (\S+)")),
    ("vitest failure", re.compile(r"FAIL\s+(\S+ > .+)")),
    ("playwright failure", re.compile(r"✘\s+\d+ \[[^\]]*\] › (.+?) \(")),
    # The summary block ("  1 failed" then the spec line); each line carries a runner timestamp.
    ("playwright failure", re.compile(r"(?m)^[^\n]*?\b\d+ failed[ \t]*\n[^\n]*?(?:\[[^\]]*\] › )?(\S+\.spec\.ts:\d+:\d+ › .+?)[ \t]*(?:─+)?$")),
    ("request timeout", re.compile(r"(Client\.Timeout exceeded[^\"]*)")),
]
# Only these read the full log (a test failing is what they name); every other signature reads
# runner evidence only, so a test that prints a docker/timeout/disk message is not taken for the runner.
_FULL_TEXT_LABELS = ("go test failure",)
_CONTINUATION = re.compile(r"^(?:\S+Z\s+\d+[A-Z]*\s)?(?:\t|\s{4,})\S")
_RUN_START = re.compile(r"(?:^|\s)=== (?:RUN|CONT)\s")
_RUN_END = re.compile(r"(?:^|\s)--- (?:PASS|FAIL|SKIP):")
_DATA_RACE = re.compile(r"WARNING: DATA RACE")
_GO_FAIL = re.compile(r"--- FAIL: (\S+)")
_WAITING_FOR_SERVICES = re.compile(r"Waiting for services to be up and running")
_RUNNER_TAIL = re.compile(r"Cleaning up|ERROR: Job failed|Running after_script|Uploading artifacts|Cleaning up file based variables|section_(?:start|end)|Terminated|Job succeeded", re.I)


def signature(trace):
    """First recognisable failure in a job log, or the generic reason."""
    text = _ANSI.sub("", trace)
    # Lines a Go test printed itself (`foo_test.go:12: ...`) are not runner evidence: a test may
    # print SQLSTATE 53100 or a disk-gate marker on purpose and must stay a test failure.
    # Go indents the continuation of a multi-line t.Log/t.Errorf, so those lines are dropped too.
    # Best effort: a real infra error reported through t.Fatalf is read as a test failure.
    # Under `go test -v` everything between `=== RUN` and the test's `--- PASS/FAIL` line is the
    # test's own stdout, with or without a `_test.go:` prefix.
    kept, in_test_output, in_run = [], False, False
    for l in text.splitlines():
        if _RUN_START.search(l):
            in_run = True
        elif in_run:
            if _RUN_END.search(l):
                in_run = False
            continue
        if "_test.go:" in l:
            in_test_output = True
        elif in_test_output and _CONTINUATION.match(l):
            pass
        else:
            in_test_output = False
            kept.append(l)
    infra_text = "\n".join(kept)
    for label, rx, *fixed in _SIGNATURES:
        m = rx.search(text if label in _FULL_TEXT_LABELS else infra_text)
        if not m:
            continue
        if label == "go test failure" and _DATA_RACE.search(text):
            # the race is not necessarily in the first failed test: name it only when unambiguous
            if len(_GO_FAIL.findall(text)) > 1:
                return "go data race"
            return f"go data race: {m.group(1)}"
        detail = fixed[0] if fixed else (m.group(1) if m.groups() else "")
        if label == "docker daemon error":
            # first clause only: ids, paths and the (docker.go:N:Ns) tail differ per job
            detail = detail.split(": ")[0]
        if label in ("runner disk full", "postgres auth failed") and detail:
            return f"{label} ({detail})"
        return f"{label}: {detail}".rstrip(": ").strip()[:120]
    if _DATA_RACE.search(text):
        return "go data race"
    lines = [l for l in text.splitlines() if l.strip()]
    waits = [i for i, l in enumerate(lines) if _WAITING_FOR_SERVICES.search(l)]
    if waits and all(_RUNNER_TAIL.search(l) for l in lines[waits[-1] + 1:]):
        # nothing but runner housekeeping/footer after the wait: the job never got to its script
        return "job stopped while waiting for services"
    errors = [l for l in text.splitlines() if "ERROR:" in l]
    if errors:
        return "last runner error: " + errors[-1].split("ERROR:", 1)[1].strip()[:90]
    return "unrecognised (read the log)"


def find_flakes(attempts):
    """attempts: iterable of (sha, job_name, status, pipeline_id[, job_id]).
    Returns {job: {sha: [(pipeline id, job id of a failed attempt)]}}."""
    by_key = collections.defaultdict(list)
    for sha, name, status, pid, *jid in attempts:
        by_key[(sha, name)].append((status, pid, jid[0] if jid else None))
    flakes = collections.defaultdict(dict)
    for (sha, name), rows in by_key.items():
        statuses = {r[0] for r in rows}
        if "failed" in statuses and "success" in statuses:
            flakes[name][sha] = sorted({(r[1], r[2]) for r in rows if r[0] == "failed"}, key=lambda x: (x[0], x[1] or 0))
    return flakes


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--days", type=int, default=14)
    ap.add_argument("--project", default="entire-vc/evc-mesh")
    ap.add_argument("--causes", action="store_true", help="read one failed log per flaked SHA and count signatures")
    a = ap.parse_args()
    proj = urllib.parse.quote(a.project, safe="")
    since = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=a.days)).strftime("%Y-%m-%dT%H:%M:%SZ")
    attempts = []
    n = 0
    for p in paged(f"projects/{proj}/pipelines?updated_after={since}"):
        n += 1
        for j in paged(f"projects/{proj}/pipelines/{p['id']}/jobs?include_retried=true"):
            if j["status"] in ("failed", "success"):
                attempts.append((p["sha"], j["name"], j["status"], p["id"], j["id"]))
    flakes = find_flakes(attempts)
    print(f"pipelines scanned: {n}, window: {a.days}d")
    for name, shas in sorted(flakes.items(), key=lambda kv: -len(kv[1])):
        first = next(iter(shas.values()))
        tag = "  (intentional refusal, not a flake)" if name in INTENTIONAL else ""
        print(f"{len(shas):3d}  {name}  e.g. pipelines {sorted({p for p, _ in first})}{tag}")
        if a.causes and name not in INTENTIONAL:
            sigs = collections.Counter()
            for rows in shas.values():
                jid = rows[0][1]
                trace = subprocess.run(["glab", "api", f"projects/{proj}/jobs/{jid}/trace"], capture_output=True, text=True).stdout
                sigs[signature(trace)] += 1
            for sig, cnt in sigs.most_common(5):
                print(f"       {cnt:3d} x {sig}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
