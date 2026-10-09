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
    ("go test failure", re.compile(r"--- FAIL: (\S+)")),
    ("vitest failure", re.compile(r"FAIL\s+(\S+ > .+)")),
    ("playwright failure", re.compile(r"✘\s+\d+ \[[^\]]*\] › (.+?) \(")),
    ("request timeout", re.compile(r"(Client\.Timeout exceeded[^\"]*)")),
]


def signature(trace):
    """First recognisable failure in a job log, or the generic reason."""
    text = _ANSI.sub("", trace)
    for label, rx in _SIGNATURES:
        m = rx.search(text)
        if m:
            return f"{label}: {m.group(1) if m.groups() else ''}".rstrip(": ").strip()[:120]
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
