#!/usr/bin/env python3
"""Remove `mesh-ci-*` docker compose projects left behind by a killed CI run.

`make ci-test`'s `trap ... EXIT` (see #03eed881) only fires on a normal exit or a
signal bash chooses to run it for. SIGKILL cannot be trapped by any process, and
that is exactly how most of our runs actually die on this fleet: a session gets
killed by hung-turn-watchdog, a fiddler relaunch, `/switch`, Ctrl+C, or the rm
guard wedging a lane until it is force-killed. The trap never runs, the compose
stack it was supposed to tear down sits there forever, and the leak is silent
until it piles up enough to exhaust disk/address space for everyone on the host
(#8416e39c — 29 containers, ext4 journal aborted under it).

This script is the safety net that does not depend on the dying process running
any cleanup code at all. It looks at what is actually left on the host and
decides, per compose project, whether it is a live run or a corpse:

  1. No checkout directory left on disk (`com.docker.compose.project.working_dir`
     label points at a path that no longer exists) -> the compose file itself is
     gone, nothing can possibly still be using this project. Reap immediately,
     regardless of age.
  2. Checkout directory exists and carries a `.ci-active-pid` marker (written by
     `make ci-test` at the start of its trapped recipe) -> read the PID and the
     `ps -o lstart=` string recorded next to it. If a process with that PID is
     alive AND its own `lstart` still matches the recorded one, the original run
     is still live -> skip it. Comparing `lstart` (not just PID liveness) guards
     against PID reuse: a long-idle host can recycle a PID to an unrelated
     process between the run dying and the reaper running, and a bare
     `kill -0 pid` would misread that as "still alive" (the same self-matching
     trap as `ps | grep <literal in your own command>` -- see
     solution-null-test-... / the dispatcher double-spawn incident: a detector
     that can match something other than what it's looking for will eventually
     report a false positive on live prod data, not just in a demo).
  3. Checkout directory exists but no marker (older leak predating this script,
     or someone ran `docker compose up` directly without going through
     `make ci-test`) -> fall back to age: reap only past MAX_AGE_SECONDS, so a
     run that hasn't reached the point of writing its marker yet (or was never
     going to write one) still gets a grace window instead of being torn down
     out from under it.

Invoked from `make ci-services-up` (cheap, runs exactly when someone is about to
start a new CI stack anyway) so steady-state container count self-corrects on
ordinary fleet usage without needing a separate cron.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from datetime import datetime, timezone

PROJECT_PREFIX = "mesh-ci-"
MARKER_NAME = ".ci-active-pid"
DEFAULT_MAX_AGE_SECONDS = 4 * 60 * 60  # grace window for marker-less projects

WORKING_DIR_LABEL = "com.docker.compose.project.working_dir"
PROJECT_LABEL = "com.docker.compose.project"


class Decision:
    REAP = "reap"
    SKIP = "skip"


def docker(*args: str) -> str:
    result = subprocess.run(
        ["docker", *args], capture_output=True, text=True, check=True
    )
    return result.stdout


def list_mesh_ci_projects() -> list[str]:
    """Distinct mesh-ci-* project names with at least one container (any state)."""
    out = docker(
        "ps", "-a", "--filter", f"label={PROJECT_LABEL}",
        "--format", f"{{{{.Label \"{PROJECT_LABEL}\"}}}}",
    )
    names = {line.strip() for line in out.splitlines() if line.strip()}
    return sorted(n for n in names if n.startswith(PROJECT_PREFIX))


def first_container_for_project(project: str) -> str | None:
    out = docker("ps", "-a", "--filter", f"label={PROJECT_LABEL}={project}", "-q")
    lines = [l.strip() for l in out.splitlines() if l.strip()]
    return lines[0] if lines else None


def inspect_labels(container_id: str) -> dict:
    out = docker("inspect", "-f", "{{ json .Config.Labels }}", container_id)
    return json.loads(out)


def inspect_created(container_id: str) -> str:
    return docker("inspect", "-f", "{{.Created}}", container_id).strip()


def parse_docker_timestamp(ts: str) -> float:
    """Parse a docker `.Created`-style RFC3339 timestamp into a unix epoch.

    Handles the variable-length fractional-second + trailing 'Z' that
    `datetime.fromisoformat` only started accepting in 3.11 -- this fleet's
    prod interpreter is 3.9.6.
    """
    ts = ts.strip()
    if ts.endswith("Z"):
        ts = ts[:-1]
    if "." in ts:
        base, frac = ts.split(".", 1)
        frac = (frac + "000000")[:6]
        ts = f"{base}.{frac}"
        fmt = "%Y-%m-%dT%H:%M:%S.%f"
    else:
        fmt = "%Y-%m-%dT%H:%M:%S"
    dt = datetime.strptime(ts, fmt).replace(tzinfo=timezone.utc)
    return dt.timestamp()


def read_marker(marker_path: str) -> tuple[int, str] | None:
    try:
        with open(marker_path, encoding="utf-8") as f:
            lines = f.read().splitlines()
    except OSError:
        return None
    if len(lines) < 2:
        return None
    try:
        pid = int(lines[0].strip())
    except ValueError:
        return None
    return pid, lines[1]


def process_lstart(pid: int) -> str | None:
    result = subprocess.run(
        ["ps", "-o", "lstart=", "-p", str(pid)], capture_output=True, text=True
    )
    if result.returncode != 0:
        return None
    out = result.stdout.strip()
    return out or None


def pid_is_the_recorded_run(pid: int, recorded_lstart: str) -> bool:
    """True iff `pid` is alive AND is (almost certainly) the same process
    instance that wrote the marker, not an unrelated process that reused the
    PID after the original run died."""
    try:
        os.kill(pid, 0)
    except OSError:
        return False
    except ValueError:
        return False
    live_lstart = process_lstart(pid)
    if live_lstart is None:
        return False
    return live_lstart.strip() == recorded_lstart.strip()


def decide(
    project: str,
    working_dir: str | None,
    created_ts: str,
    now: float,
    max_age_seconds: int,
) -> tuple[str, str]:
    """Return (Decision, reason) for a single project. Pure function — no I/O
    beyond what the caller already gathered — so this is what the unit tests
    exercise directly."""
    if not working_dir or not os.path.isdir(working_dir):
        return Decision.REAP, "checkout directory no longer exists on disk"

    marker_path = os.path.join(working_dir, MARKER_NAME)
    marker = read_marker(marker_path)
    if marker is not None:
        pid, recorded_lstart = marker
        if pid_is_the_recorded_run(pid, recorded_lstart):
            return Decision.SKIP, f"live run (pid {pid} matches marker)"
        return Decision.REAP, f"marker present but pid {pid} is dead or reused"

    age = now - parse_docker_timestamp(created_ts)
    if age > max_age_seconds:
        return Decision.REAP, f"no marker, age {int(age)}s exceeds grace window"
    return Decision.SKIP, f"no marker, age {int(age)}s within grace window"


def reap_project(project: str) -> None:
    # `-p <project>` alone (no `-f`/compose file) is enough for `down` to find
    # and remove everything docker compose labeled for this project — this is
    # what lets us reap a project whose compose file's directory is already
    # gone (case 1 above).
    subprocess.run(
        ["docker", "compose", "-p", project, "down", "-v", "--remove-orphans"],
        check=False,
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--max-age-seconds",
        type=int,
        default=DEFAULT_MAX_AGE_SECONDS,
        help="grace window for marker-less projects (default: %(default)s)",
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="print decisions, reap nothing"
    )
    args = parser.parse_args(argv)

    try:
        projects = list_mesh_ci_projects()
    except (subprocess.CalledProcessError, FileNotFoundError) as exc:
        print(f"reap-stale-ci-containers: could not list docker state: {exc}", file=sys.stderr)
        return 0  # best-effort: never block `make ci-services-up` on this

    if not projects:
        return 0

    now = datetime.now(tz=timezone.utc).timestamp()
    reaped = 0
    for project in projects:
        try:
            cid = first_container_for_project(project)
            if cid is None:
                continue
            labels = inspect_labels(cid)
            working_dir = labels.get(WORKING_DIR_LABEL)
            created_ts = inspect_created(cid)
            decision, reason = decide(project, working_dir, created_ts, now, args.max_age_seconds)
        except Exception as exc:  # noqa: BLE001 - best-effort, one bad project must not stop the rest
            print(f"reap-stale-ci-containers: {project}: skipping, inspect failed: {exc}", file=sys.stderr)
            continue

        if decision == Decision.REAP:
            print(f"reap-stale-ci-containers: REAP {project} — {reason}")
            if not args.dry_run:
                reap_project(project)
                marker_path = os.path.join(working_dir, MARKER_NAME) if working_dir else None
                if marker_path and os.path.exists(marker_path):
                    try:
                        os.remove(marker_path)
                    except OSError:
                        pass
            reaped += 1
        else:
            print(f"reap-stale-ci-containers: skip {project} — {reason}")

    if reaped:
        print(f"reap-stale-ci-containers: reaped {reaped} stale project(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
