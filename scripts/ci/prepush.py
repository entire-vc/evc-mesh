#!/usr/bin/env python3
"""Pre-push gate: `make ci` stage by stage, tolerating failures main already has.

A red test that is already red on origin/main (latest finished main pipeline)
is not this branch's fault and must not block its push; a NEW red test, or any
lint/build failure, still does. Known failures print as warnings.

Stages run in make-ci order, and a test-stage failure does not stop later
stages, so build/web results are still seen.

    python3 scripts/ci/prepush.py            # run everything
    python3 scripts/ci/prepush.py --known-from-log FILE   # print parsed failures, no run

No bypass flag: lint/build never tolerated, unknown failures always block.
"""
import fcntl
import json
import os
import re
import subprocess
import sys
import time

PROJECT = "entire-vc%2Fevc-mesh"
HARD_STAGES = ["ci-install-tools", "ci-lint", "ci-build"]
TEST_STAGES = ["ci-test", "ci-test-web"]
LOCK_PATH = os.environ.get("PREPUSH_LOCK", "/tmp/evc-mesh-ci-prepush.lock")
LOCK_WAIT_S = int(os.environ.get("PREPUSH_LOCK_WAIT_S", "3600"))
WEB_PREFIX, WEB_FILES = "web/", ("Makefile",)  # a diff touching none of these cannot change ci-test-web
VITEST_TIMEOUT_MS = os.environ.get("VITEST_TIMEOUT_MS", "60000")  # Mini load average 30-70: 20s default flaps
GO_TEST_TIMEOUT = os.environ.get("GO_TEST_TIMEOUT", "30m")


def go_test_parallelism():
    """-p for the gate's go test: half the cores, fewer when the host is loaded (1-min load > cores). Env GO_TEST_P wins."""
    if os.environ.get("GO_TEST_P"):
        return os.environ["GO_TEST_P"]
    cores = os.cpu_count() or 4
    try:
        load = os.getloadavg()[0]
    except OSError:
        load = 0
    return str(max(1, cores // (4 if load > cores else 2)))


ORDER = ["ci-install-tools", "ci-lint", "ci-test", "ci-test-web", "ci-build"]

# Full identifiers: Go subtests keep their "/sub" suffix and vitest cases their
# "file > describe > it" path, so a NEW failing case inside an already-red test
# or file is a different identifier and blocks.
GO_FAIL = re.compile(r"--- FAIL: (\S+)")
WEB_FAIL = re.compile(r"\bFAIL\s+(\S+\.test\.[jt]sx?(?: > [^\n]*?)?)(?:\s+\d+ms)?\s*$", re.M)
_ANSI = re.compile(r"\x1b\[[0-9;]*[mK]")


_RUNNER_PREFIX = re.compile(r"^\d{4}-\S+ \d+[OE] ?")  # GitLab trace timestamp prefix
GO_PKG = re.compile(r"^(?:FAIL|ok)\s+(\S+)\s", re.M)
# go test prints this when -timeout fires; the gate treats it as a package failure, not as unparsable output
GO_TIMEOUT = re.compile(r"panic: test timed out after")
TIMEOUT_ID = "(package timed out)"


def _go_failures(log):
    """'pkg:TestName/sub'. go test prints the package line after its tests' output, so
    pending failures take the next package line's path; same-named tests in two packages stay distinct."""
    out, pending = set(), []
    for line in log.splitlines():
        line = _RUNNER_PREFIX.sub("", line)
        if GO_TIMEOUT.search(line):
            pending.append(TIMEOUT_ID)  # attributed to the package by the FAIL line that follows
            continue
        m = GO_FAIL.search(line)
        if m:
            pending.append(m.group(1))
            continue
        p = GO_PKG.match(line)
        if p:
            out |= {f"{p.group(1)}:{t}" for t in pending}
            pending = []
    return out | set(pending)


def parse_failures(log):
    """Failing test identifiers in a go test / vitest log: 'pkg:TestName/sub' or 'path/x.test.tsx > describe > it'."""
    log = _ANSI.sub("", log)
    return {f.strip() for f in _go_failures(log) | set(WEB_FAIL.findall(log))}


def decide(stage_results, known):
    """stage_results: {stage: (exit_code, failures)}. Returns (blocking, tolerated) lists of strings."""
    blocking, tolerated = [], []
    for stage, (code, failures) in stage_results.items():
        if code == 0:
            continue
        if stage in HARD_STAGES:
            blocking.append(f"{stage} failed")
        elif not failures:
            blocking.append(f"{stage} failed with no parsable test failure")
        else:
            new = sorted(f for f in failures if f not in known)
            old = sorted(f for f in failures if f in known)
            tolerated += [f"{stage}: {f} (already red on main)" for f in old]
            blocking += [f"{stage}: {f} (NEW failure)" for f in new]
    return blocking, tolerated


def glab(path):
    return subprocess.run(["glab", "api", path], capture_output=True, text=True, check=True).stdout


def known_red_on_main():
    """Failures in the latest finished main pipeline. Any lookup error -> empty set (fail closed)."""
    try:
        pipes = json.loads(glab(f"projects/{PROJECT}/pipelines?ref=main&per_page=10"))
        done = [p for p in pipes if p["status"] in ("success", "failed")]
        if not done:
            return set()
        pid = done[0]["id"]
        known = set()
        jobs = json.loads(glab(f"projects/{PROJECT}/pipelines/{pid}/jobs?scope[]=failed&per_page=100"))
        for j in jobs:
            known |= parse_failures(glab(f"projects/{PROJECT}/jobs/{j['id']}/trace"))
        print(f"[prepush] main pipeline #{pid}: {len(known)} known failing test(s)")
        return known
    except Exception as e:  # noqa: BLE001 - any failure means "no tolerance"
        print(f"[prepush] cannot read main pipeline ({e}); tolerating nothing")
        return set()


def acquire_lock():
    """One prepush gate per machine: wait for the previous holder instead of failing (parallel
    golangci-lint and vitest from several lanes starve each other). Returns the held fd, or None on timeout."""
    fd = os.open(LOCK_PATH, os.O_CREAT | os.O_RDWR, 0o666)
    start = time.monotonic()
    announced = 0
    while True:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except BlockingIOError:
            waited = time.monotonic() - start
            if waited >= LOCK_WAIT_S:
                os.close(fd)
                return None
            if waited >= announced:
                print(f"[prepush] another prepush gate is running on this machine, waiting ({int(waited)}s)", flush=True)
                announced += 60
            time.sleep(2)


def web_touched():
    """True if the branch diff vs origin/main touches web/ or the root Makefile. Any git error -> True (fail closed)."""
    try:
        base = subprocess.run(["git", "merge-base", "origin/main", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()
        names = subprocess.run(["git", "diff", "--name-only", base, "HEAD"], capture_output=True, text=True, check=True).stdout.split()
        return any(n.startswith(WEB_PREFIX) or n in WEB_FILES for n in names)
    except Exception:  # noqa: BLE001
        return True


def run_stage(stage):
    """Run `make <stage>`, echoing output live; returns (exit code, full output)."""
    env = dict(os.environ, VITEST_TIMEOUT_MS=VITEST_TIMEOUT_MS, GO_TEST_TIMEOUT=GO_TEST_TIMEOUT, GO_TEST_P=go_test_parallelism())
    proc = subprocess.Popen(["make", stage], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, errors="replace", env=env)
    lines = []
    for line in proc.stdout:
        sys.stdout.write(line)
        lines.append(line)
    return proc.wait(), "".join(lines)


def main(argv):
    if len(argv) == 3 and argv[1] == "--known-from-log":
        with open(argv[2]) as f:
            print("\n".join(sorted(parse_failures(f.read()))))
        return 0
    lock = acquire_lock()
    if lock is None:
        print(f"[prepush] BLOCKING: could not get the machine gate lock in {LOCK_WAIT_S}s")
        print("❌  push blocked")
        return 1
    try:
        return _gate(lock)
    finally:
        os.close(lock)


def _gate(lock):
    skip_web = not web_touched()
    results = {}
    for stage in ORDER:
        if stage == "ci-test-web" and skip_web:
            print("[prepush] ci-test-web skipped: diff vs origin/main does not touch web/")
            continue
        code, log = run_stage(stage)
        results[stage] = (code, parse_failures(log) if code else set())
        if code and stage in HARD_STAGES:
            break
    failed_tests = any(c and s in TEST_STAGES for s, (c, _) in results.items())
    known = known_red_on_main() if failed_tests else set()
    blocking, tolerated = decide(results, known)
    for t in tolerated:
        print(f"[prepush] WARNING known red, not blocking: {t}")
    if tolerated:
        print("[prepush] note: this only lets the push through; the merge gate in CI is unchanged and red stays red")
    if blocking:
        for b in blocking:
            print(f"[prepush] BLOCKING: {b}")
        print("❌  push blocked")
        return 1
    print("✅  push allowed" + (" (with known-red warnings)" if tolerated else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
