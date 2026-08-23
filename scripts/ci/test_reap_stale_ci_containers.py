#!/usr/bin/env python3
"""Self-checks for the stale-CI-container reaper.

The reaper exists specifically to catch the case where the process that was
supposed to clean up got SIGKILLed and never ran its own trap. That means the
one thing these tests must not do is call `pid_is_the_recorded_run` on a
*fake* PID and hope for the best — a wrong-by-construction liveness check
(e.g. a bare `kill -0` with no lstart cross-check) would pass every test here
and still misclassify a reused PID as a live run on real hardware. So the
liveness tests spawn and reap genuine child processes and read their real
`ps -o lstart=` output, the same way the reaper itself does.
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from reap_stale_ci_containers import (  # noqa: E402
    Decision,
    decide,
    parse_docker_timestamp,
    pid_is_the_recorded_run,
    process_lstart,
)


def docker_ts(dt: datetime) -> str:
    """Render a datetime the way `docker inspect -f '{{.Created}}'` does."""
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f000") + "Z"


def write_marker(working_dir: str, pid: int, lstart: str) -> None:
    with open(os.path.join(working_dir, ".ci-active-pid"), "w", encoding="utf-8") as f:
        f.write(f"{pid}\n{lstart}\n")


class TestParseDockerTimestamp(unittest.TestCase):
    def test_nanosecond_fraction_and_trailing_z(self):
        epoch = parse_docker_timestamp("2026-08-23T12:00:00.123456789Z")
        expected = datetime(2026, 8, 23, 12, 0, 0, 123456, tzinfo=timezone.utc).timestamp()
        self.assertAlmostEqual(epoch, expected, places=3)

    def test_no_fraction(self):
        epoch = parse_docker_timestamp("2026-08-23T12:00:00Z")
        expected = datetime(2026, 8, 23, 12, 0, 0, tzinfo=timezone.utc).timestamp()
        self.assertEqual(epoch, expected)


class TestLivenessCheckUsesRealProcesses(unittest.TestCase):
    """These do NOT mock ps/kill — a mocked liveness check can't catch the PID-
    reuse bug the whole `lstart` comparison exists to prevent."""

    def test_live_process_with_matching_lstart_is_recognised_as_alive(self):
        proc = subprocess.Popen(["sleep", "30"])
        try:
            lstart = process_lstart(proc.pid)
            self.assertIsNotNone(lstart)
            self.assertTrue(pid_is_the_recorded_run(proc.pid, lstart))
        finally:
            proc.kill()
            proc.wait()

    def test_dead_pid_is_not_recognised_as_alive(self):
        proc = subprocess.Popen(["true"])
        proc.wait()  # reaped: this exact pid slot is now free, not just "the process exited"
        # A dead pid has no ps -o lstart= output of its own; feed it something
        # syntactically plausible so a naive "no lstart recorded" bypass can't
        # accidentally pass this test.
        self.assertFalse(pid_is_the_recorded_run(proc.pid, "Mon Jan  1 00:00:00 1970"))

    def test_live_process_but_mismatched_lstart_is_treated_as_a_reused_pid(self):
        """The actual mutation this suite exists to catch: kill -0 alone would
        call this 'alive' and skip reaping a dead run's containers forever
        once its pid gets recycled by an unrelated process."""
        proc = subprocess.Popen(["sleep", "30"])
        try:
            self.assertFalse(
                pid_is_the_recorded_run(proc.pid, "Mon Jan  1 00:00:00 1970")
            )
        finally:
            proc.kill()
            proc.wait()


class TestDecide(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.working_dir = self.tmp.name
        self.now = datetime.now(tz=timezone.utc).timestamp()

    def tearDown(self):
        self.tmp.cleanup()

    def test_reaps_when_checkout_directory_is_gone(self):
        missing = os.path.join(self.working_dir, "does-not-exist")
        decision, reason = decide(
            "mesh-ci-x", missing, docker_ts(datetime.now(timezone.utc)), self.now, 3600
        )
        self.assertEqual(decision, Decision.REAP)
        self.assertIn("no longer exists", reason)

    def test_reaps_when_working_dir_is_none(self):
        decision, _ = decide(
            "mesh-ci-x", None, docker_ts(datetime.now(timezone.utc)), self.now, 3600
        )
        self.assertEqual(decision, Decision.REAP)

    def test_no_marker_within_grace_window_is_skipped(self):
        created = datetime.now(timezone.utc) - timedelta(minutes=5)
        decision, reason = decide(
            "mesh-ci-x", self.working_dir, docker_ts(created), self.now, max_age_seconds=3600
        )
        self.assertEqual(decision, Decision.SKIP)
        self.assertIn("within grace window", reason)

    def test_no_marker_past_grace_window_is_reaped(self):
        created = datetime.now(timezone.utc) - timedelta(hours=5)
        decision, reason = decide(
            "mesh-ci-x", self.working_dir, docker_ts(created), self.now, max_age_seconds=3600
        )
        self.assertEqual(decision, Decision.REAP)
        self.assertIn("exceeds grace window", reason)

    def test_marker_with_live_matching_pid_is_skipped_regardless_of_age(self):
        proc = subprocess.Popen(["sleep", "30"])
        try:
            lstart = process_lstart(proc.pid)
            write_marker(self.working_dir, proc.pid, lstart)
            created = datetime.now(timezone.utc) - timedelta(hours=10)  # older than any age grace
            decision, reason = decide(
                "mesh-ci-x", self.working_dir, docker_ts(created), self.now, max_age_seconds=3600
            )
            self.assertEqual(decision, Decision.SKIP)
            self.assertIn("live run", reason)
        finally:
            proc.kill()
            proc.wait()

    def test_marker_with_dead_pid_is_reaped_even_within_grace_window(self):
        proc = subprocess.Popen(["true"])
        proc.wait()
        write_marker(self.working_dir, proc.pid, "Mon Jan  1 00:00:00 1970")
        created = datetime.now(timezone.utc)  # brand new by age -- must not matter, this is SIGKILL's case
        decision, reason = decide(
            "mesh-ci-x", self.working_dir, docker_ts(created), self.now, max_age_seconds=3600
        )
        self.assertEqual(decision, Decision.REAP)
        self.assertIn("dead or reused", reason)

    def test_marker_with_reused_pid_is_reaped_not_mistaken_for_the_original_run(self):
        proc = subprocess.Popen(["sleep", "30"])
        try:
            write_marker(self.working_dir, proc.pid, "Mon Jan  1 00:00:00 1970")
            created = datetime.now(timezone.utc)
            decision, reason = decide(
                "mesh-ci-x", self.working_dir, docker_ts(created), self.now, max_age_seconds=3600
            )
            self.assertEqual(decision, Decision.REAP)
            self.assertIn("dead or reused", reason)
        finally:
            proc.kill()
            proc.wait()


if __name__ == "__main__":
    unittest.main()
