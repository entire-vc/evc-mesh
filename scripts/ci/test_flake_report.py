#!/usr/bin/env python3
"""Self-checks for flake_report.signature().

The fixtures are trimmed from real job logs of 10-12 Oct 2026. In every one of them
GitLab's final line is `ERROR: Job failed: exit code 1`, which says nothing about the
cause; the cause sits earlier in the trace. Before the signatures were extended these
all collapsed into "last runner error: Job failed: exit code 1", so a real flake could
not be told from a runner or infra failure.
"""

from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from flake_report import signature  # noqa: E402

TS = "2026-10-12T11:30:26.051170Z 01E "
FINAL = "2026-10-12T11:31:00.211716Z 00O ERROR: Job failed: exit code 1\n"


class SignatureTest(unittest.TestCase):
    def test_stall_watchdog_timeout_is_not_a_bare_exit_code_1(self):
        trace = (
            TS + "ERROR: invalid coverage artifact: Command "
            "'['sh', 'scripts/ci/go-test-stall-watchdog.sh', './...', '-race']' "
            "returned non-zero exit status 124.\n" + FINAL
        )
        sig = signature(trace)
        self.assertIn("stall", sig)
        self.assertNotIn("exit code 1", sig)

    def test_watchdog_exit_1_is_left_to_the_go_signatures(self):
        trace = (
            "--- FAIL: TestSomething (0.01s)\n"
            + TS + "ERROR: invalid coverage artifact: Command "
            "'['sh', 'scripts/ci/go-test-stall-watchdog.sh', './...']' "
            "returned non-zero exit status 1.\n" + FINAL
        )
        self.assertEqual(signature(trace), "go test failure: TestSomething")

    def test_data_race_names_the_test(self):
        trace = (
            "WARNING: DATA RACE\nWrite at 0x00c0001 by goroutine 12:\n"
            "--- FAIL: TestClosedFollowUp_ConcurrentClaims (0.05s)\n"
            "    testing.go:1490: race detected during execution of test\n" + FINAL
        )
        self.assertEqual(signature(trace), "go data race: TestClosedFollowUp_ConcurrentClaims")

    def test_data_race_without_a_named_test(self):
        self.assertEqual(signature("WARNING: DATA RACE\n" + FINAL), "go data race")

    def test_enospc_from_the_docker_snapshotter(self):
        trace = (
            "ERROR: Job failed (system failure): Error response from daemon: failed to "
            "create prepare snapshot dir: failed to create temp dir: mkdir "
            "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/new-1: "
            "no space left on device (docker.go:979:1s)\n"
        )
        self.assertEqual(signature(trace), "runner disk full (no space left on device)")

    def test_enospc_inside_postgres(self):
        trace = (
            TS + 'goose run: ERROR 20260305034_x.sql: failed to execute SQL query "CREATE TABLE t (...)": '
            'ERROR: could not extend file "base/16384/17360": No space left on device (SQLSTATE 53100)\n'
            + FINAL
        )
        self.assertEqual(signature(trace), "runner disk full (no space left on device)")

    def test_ci_disk_gate_refusal(self):
        trace = (
            "[ci-disk-gate] overlay 115.2G 110.5G 4.7G 96% /\n"
            "[ci-disk-gate] REFUSING: runner host runner-x is 96% full.\n" + FINAL
        )
        self.assertEqual(signature(trace), "runner disk full (ci-disk-gate refusal)")

    def test_postgres_auth_failure(self):
        trace = 'FATAL: password authentication failed for user "mesh" (SQLSTATE 28P01)\n' + FINAL
        self.assertEqual(signature(trace), "postgres auth failed (28P01)")

    def test_docker_snapshot_failure_without_enospc(self):
        trace = (
            "ERROR: Job failed (system failure): Error response from daemon: "
            "failed to create task for container: failed to create shim task (docker.go:1:1s)\n"
        )
        self.assertEqual(
            signature(trace).split(":")[0], "docker daemon error",
        )

    def test_services_never_came_up(self):
        trace = (
            "Starting service nats:2-alpine...\n"
            "Waiting for services to be up and running (timeout 30 seconds)...\n"
        )
        self.assertEqual(signature(trace), "job stopped while waiting for services")

    def test_playwright_summary_block_without_a_cross_mark_line(self):
        trace = (
            "2026-10-11T01:16:42.712642Z 01O   1 failed\n"
            "2026-10-11T01:16:42.712643Z 01O     board-e2e/board-pagination.spec.ts:8:1 › all 331 cards, Done 282, page two accessible \n"
            "2026-10-11T01:16:42.712643Z 01O   4 passed (29.7s)\n" + FINAL
        )
        self.assertEqual(
            signature(trace),
            "playwright failure: board-e2e/board-pagination.spec.ts:8:1 › all 331 cards, Done 282, page two accessible",
        )

    def test_existing_signatures_keep_working(self):
        self.assertEqual(signature("--- FAIL: TestX (0.00s)\n"), "go test failure: TestX")
        self.assertEqual(
            signature("x carries a hold label y"), "hold label"
        )

    def test_test_that_prints_an_infra_phrase_stays_a_test_failure(self):
        self.assertEqual(
            signature("--- FAIL: TestDiskFull (0.00s)\n got error: no space left on device (expected)\n"),
            "go test failure: TestDiskFull",
        )
        self.assertEqual(
            signature("--- FAIL: TestLogin_BadPassword (0.00s)\n pq: password authentication failed for user x\n"),
            "go test failure: TestLogin_BadPassword",
        )

    def test_test_that_prints_the_exact_infra_marker_stays_a_test_failure(self):
        for line in ("store_test.go:41: pq: out of disk (SQLSTATE 53100)",
                     "store_test.go:52: pq: auth (SQLSTATE 28P01)",
                     "disk_test.go:9: write /tmp/x: no space left on device",
                     "gate_test.go:7: [ci-disk-gate] REFUSING to start"):
            self.assertEqual(
                signature("--- FAIL: TestX (0.00s)\n    " + line + "\n"),
                "go test failure: TestX", line)
        self.assertEqual(signature("pq: out of disk (SQLSTATE 53100)\n--- FAIL: TestX (0.00s)\n"),
                         "runner disk full (no space left on device)")

    def test_indented_continuation_of_a_test_log_is_not_runner_evidence(self):
        trace = ("store_test.go:41: open failed\n        FATAL: password authentication failed (SQLSTATE 28P01)\n"
                 "--- FAIL: TestX (0.00s)\n")
        self.assertEqual(signature(trace), "go test failure: TestX")
        timestamped = ("2026-10-13T05:47:57.9Z 01E store_test.go:41: open failed\n"
                       "2026-10-13T05:47:57.9Z 01E         SQLSTATE 53100\n--- FAIL: TestX (0.00s)\n")
        self.assertEqual(signature(timestamped), "go test failure: TestX")
        # the continuation rule ends at the first unindented line
        self.assertEqual(signature("a_test.go:1: x\nSQLSTATE 53100\n--- FAIL: TestX\n"),
                         "runner disk full (no space left on device)")

    def test_unmarked_stdout_inside_a_verbose_test_run_is_not_runner_evidence(self):
        trace = ("=== RUN   TestX\nSQLSTATE 53100\nno space left on device\n--- FAIL: TestX (0.00s)\n")
        self.assertEqual(signature(trace), "go test failure: TestX")
        timestamped = ("2026-10-13T05:47:57.9Z 01O === RUN   TestY\n2026-10-13T05:47:57.9Z 01O SQLSTATE 28P01\n"
                       "2026-10-13T05:47:57.9Z 01O --- FAIL: TestY (0.00s)\n")
        self.assertEqual(signature(timestamped), "go test failure: TestY")
        # the same text outside any test run is still runner evidence
        self.assertEqual(signature("SQLSTATE 53100\n=== RUN   TestZ\n--- FAIL: TestZ\n"),
                         "runner disk full (no space left on device)")

    def test_docker_error_detail_drops_the_per_job_tail(self):
        a = signature("ERROR: Job failed (system failure): Error response from daemon: failed to create task: oci runtime a1b2c3 (docker.go:979:1s)\n")
        b = signature("ERROR: Job failed (system failure): Error response from daemon: failed to create task: oci runtime ffee99 (docker.go:12:9s)\n")
        self.assertEqual(a, b)
        self.assertNotIn("docker.go", a)

    def test_services_wait_followed_by_cleanup_lines(self):
        trace = "Waiting for services to be up and running (timeout 30 seconds)...\nCleaning up project directory\n"
        self.assertEqual(signature(trace), "job stopped while waiting for services")

    def test_services_wait_followed_by_job_failed_footer(self):
        trace = "Waiting for services to be up and running (timeout 30 seconds)...\nCleaning up project directory\n" + FINAL
        self.assertEqual(signature(trace), "job stopped while waiting for services")

    def test_services_wait_then_script_output_is_not_a_service_stall(self):
        trace = "Waiting for services to be up and running (timeout 30 seconds)...\n$ make test\nboom\n" + FINAL
        self.assertEqual(signature(trace), "last runner error: Job failed: exit code 1")

    def test_race_names_the_race_test(self):
        self.assertEqual(
            signature("WARNING: DATA RACE\n--- FAIL: TestRacy (0.05s)\n"), "go data race: TestRacy"
        )

    def test_race_with_several_failed_tests_does_not_name_one(self):
        trace = "WARNING: DATA RACE\n--- FAIL: TestFirst (0.05s)\n--- FAIL: TestSecond (0.07s)\n"
        self.assertEqual(signature(trace), "go data race")

    def test_docker_error_printed_inside_a_test_names_the_test(self):
        trace = (
            "=== RUN   TestPull\n(system failure): Error response from daemon: boom\n"
            "--- FAIL: TestPull (0.01s)\n"
        )
        self.assertEqual(signature(trace), "go test failure: TestPull")

    def test_docker_error_outside_a_test_stays_docker(self):
        trace = "(system failure): Error response from daemon: boom: more\n"
        self.assertEqual(signature(trace), "docker daemon error: boom")

    def test_request_timeout_printed_inside_a_test_names_the_test(self):
        trace = (
            "=== RUN   TestSlow\nGet \"x\": Client.Timeout exceeded while awaiting headers\n"
            "--- FAIL: TestSlow (0.01s)\n"
        )
        self.assertEqual(signature(trace), "go test failure: TestSlow")

    def test_vitest_and_timeout_still_recognised(self):
        self.assertEqual(signature("FAIL  src/a.test.tsx > Page > renders\n"), "vitest failure: src/a.test.tsx > Page > renders")
        self.assertEqual(signature('Get "x": Client.Timeout exceeded while awaiting headers\n').split(":")[0], "request timeout")

    def test_truly_unknown_stays_unknown(self):
        self.assertEqual(signature("nothing useful here\n"), "unrecognised (read the log)")
        self.assertEqual(signature(FINAL), "last runner error: Job failed: exit code 1")


if __name__ == "__main__":
    unittest.main()
