import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import prepush  # noqa: E402
import flake_report  # noqa: E402

GO_LOG = "2026-10-09T10:00:00Z 01O --- FAIL: TestTaskDurableAudit_X (0.1s)\nFAIL\tgithub.com/x/y\t1s\n"
WEB_LOG = " \x1b[31mFAIL\x1b[0m  src/a/__tests__/foo.test.tsx > suite > renders 12ms\n"


class ParseTest(unittest.TestCase):
    def test_go_and_web(self):
        self.assertEqual(prepush.parse_failures(GO_LOG), {"github.com/x/y:TestTaskDurableAudit_X"})
        self.assertEqual(prepush.parse_failures(WEB_LOG), {"src/a/__tests__/foo.test.tsx > suite > renders"})

    def test_go_package_timeout_is_a_package_failure(self):
        log = ("panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestA (9m)\n"
               "FAIL\tgithub.com/x/slow\t600.1s\nok  \tgithub.com/x/fine\t1s\n")
        got = prepush.parse_failures(log)
        self.assertEqual(got, {"github.com/x/slow:(package timed out)"})
        b, _ = prepush.decide({"ci-test": (1, got)}, set())
        self.assertEqual(len(b), 1)  # blocks with a named package, not "no parsable failure"
        self.assertNotIn("no parsable", b[0])

    def test_parallelism_env_wins_and_is_positive(self):
        os.environ["GO_TEST_P"] = "3"
        try:
            self.assertEqual(prepush.go_test_parallelism(), "3")
        finally:
            del os.environ["GO_TEST_P"]
        self.assertGreaterEqual(int(prepush.go_test_parallelism()), 1)

    def test_parallelism_halves_cores_and_quarters_under_load(self):
        from unittest import mock
        with mock.patch.dict(os.environ, clear=False):
            os.environ.pop("GO_TEST_P", None)
            with mock.patch("os.cpu_count", return_value=16):
                with mock.patch("os.getloadavg", return_value=(2.0, 0, 0)):
                    self.assertEqual(prepush.go_test_parallelism(), "8")
                with mock.patch("os.getloadavg", return_value=(48.0, 0, 0)):  # red control: loaded host gets fewer
                    self.assertEqual(prepush.go_test_parallelism(), "4")
            with mock.patch("os.cpu_count", return_value=1), mock.patch("os.getloadavg", return_value=(9.0, 0, 0)):
                self.assertEqual(prepush.go_test_parallelism(), "1")  # never below 1

    def test_makefile_ci_test_passes_timeout_and_p(self):
        import subprocess
        root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))

        def dry(**env):
            e = {k: v for k, v in os.environ.items() if not k.startswith("GO_TEST_")}
            e.update(env)
            out = subprocess.run(["make", "-n", "ci-test"], cwd=root, env=e, capture_output=True, text=True).stdout
            return next(l for l in out.splitlines() if "go test ./..." in l)

        self.assertIn("-timeout 30m", dry())
        self.assertNotIn(" -p ", dry())  # red control: empty GO_TEST_P adds no -p
        line = dry(GO_TEST_P="5", GO_TEST_TIMEOUT="45m")
        self.assertIn("-timeout 45m", line)
        self.assertIn("-p 5", line)

    def test_subtest_and_case_are_distinct_from_parent(self):
        log = "--- FAIL: TestA (0.1s)\n    --- FAIL: TestA/sub_new (0.0s)\n FAIL  x.test.ts > s > other\n"
        got = prepush.parse_failures(log)
        self.assertEqual(got, {"TestA", "TestA/sub_new", "x.test.ts > s > other"})
        b, _ = prepush.decide({"ci-test": (1, got)}, {"TestA"})
        self.assertEqual(len(b), 2)  # red control: new subtest / new case block although parent is known

    def test_same_test_name_in_another_package_is_a_new_failure(self):  # red control for the package-identity gap
        main = prepush.parse_failures("--- FAIL: TestX (0s)\nFAIL\tpkg/a\t1s\n")
        mine = prepush.parse_failures("--- FAIL: TestX (0s)\nFAIL\tpkg/b\t1s\n")
        b, _ = prepush.decide({"ci-test": (1, mine)}, main)
        self.assertEqual(len(b), 1)

    def test_green_log_has_none(self):
        self.assertEqual(prepush.parse_failures("ok  pkg 1s\n--- PASS: TestA\n"), set())


class FlakeGroupingTest(unittest.TestCase):
    def test_same_sha_fail_then_pass_is_flake(self):
        f = flake_report.find_flakes([("s1", "j", "failed", 1), ("s1", "j", "success", 2)])
        self.assertEqual(f, {"j": {"s1": [(1, None)]}})

    def test_failed_attempt_keeps_its_job_id_for_the_log(self):
        f = flake_report.find_flakes([("s1", "j", "failed", 1, 77), ("s1", "j", "success", 2, 78)])
        self.assertEqual(f, {"j": {"s1": [(1, 77)]}})

    def test_only_failed_or_only_passed_is_not_flake(self):
        self.assertEqual(flake_report.find_flakes([("s1", "j", "failed", 1), ("s2", "j", "success", 2)]), {})

    def test_distinct_shas_counted_separately(self):
        rows = [("s1", "j", "failed", 1), ("s1", "j", "success", 2), ("s2", "j", "failed", 3), ("s2", "j", "success", 4)]
        self.assertEqual(len(flake_report.find_flakes(rows)["j"]), 2)


class SignatureTest(unittest.TestCase):
    def test_go_test_failure_is_named(self):
        log = "\x1b[0;31m--- FAIL: TestRemember_Pending (4.06s)\nFAIL\n"
        self.assertEqual(flake_report.signature(log), "go test failure: TestRemember_Pending")

    def test_request_timeout_is_named(self):
        log = 'Post "http://localhost:8005/x": context deadline exceeded (Client.Timeout exceeded while awaiting headers)'
        self.assertTrue(flake_report.signature(log).startswith("request timeout: Client.Timeout exceeded"))

    def test_unknown_log_says_so_instead_of_guessing(self):
        self.assertEqual(flake_report.signature("everything fine\n"), "unrecognised (read the log)")

    def test_unrecognised_failure_falls_back_to_the_last_runner_error(self):
        log = "2026-10-09T00:00:00Z 00O ERROR: Job failed: exit code 137\n"
        self.assertEqual(flake_report.signature(log), "last runner error: Job failed: exit code 137")

    def test_hold_gate_is_marked_intentional(self):
        self.assertIn("hold-gate", flake_report.INTENTIONAL)


class DecideTest(unittest.TestCase):
    def test_known_failure_is_tolerated(self):
        b, t = prepush.decide({"ci-test": (1, {"TestA"})}, {"TestA"})
        self.assertEqual(b, [])
        self.assertEqual(len(t), 1)

    def test_new_failure_blocks(self):  # red control
        b, t = prepush.decide({"ci-test": (1, {"TestA", "TestNew"})}, {"TestA"})
        self.assertEqual(len(b), 1)
        self.assertIn("TestNew", b[0])

    def test_lint_and_build_never_tolerated(self):
        for stage in ("ci-lint", "ci-build"):
            b, _ = prepush.decide({stage: (1, set())}, {"TestA"})
            self.assertEqual(len(b), 1)

    def test_unparsable_test_failure_blocks(self):
        b, _ = prepush.decide({"ci-test": (2, set())}, {"TestA"})
        self.assertEqual(len(b), 1)

    def test_all_green(self):
        self.assertEqual(prepush.decide({s: (0, set()) for s in prepush.ORDER}, set()), ([], []))


class MainFlowTest(unittest.TestCase):
    """End to end through main(): stages stubbed, the decision logic is the real one."""

    def run_main(self, test_log, known):
        def fake_stage(stage):
            return (1, test_log) if stage == "ci-test" else (0, "")

        orig = (prepush.run_stage, prepush.known_red_on_main)
        prepush.run_stage, prepush.known_red_on_main = fake_stage, lambda: known
        try:
            return prepush.main(["prepush.py"])
        finally:
            prepush.run_stage, prepush.known_red_on_main = orig

    def test_known_red_exits_zero(self):
        self.assertEqual(self.run_main(GO_LOG, {"github.com/x/y:TestTaskDurableAudit_X"}), 0)

    def test_new_red_exits_one(self):
        self.assertEqual(self.run_main(GO_LOG, set()), 1)

    def test_stages_after_failed_tests_still_run(self):
        seen = []
        orig, orig_web, orig_known = prepush.run_stage, prepush.web_touched, prepush.known_red_on_main
        prepush.run_stage = lambda st: (seen.append(st) or ((1, GO_LOG) if st == "ci-test" else (0, "")))
        prepush.known_red_on_main = lambda: {"github.com/x/y:TestTaskDurableAudit_X"}
        prepush.web_touched = lambda: True
        try:
            prepush.main(["prepush.py"])
        finally:
            prepush.run_stage, prepush.web_touched, prepush.known_red_on_main = orig, orig_web, orig_known
        self.assertEqual(seen, prepush.ORDER)



def _lock_file():
    import tempfile
    fd, path = tempfile.mkstemp(suffix=".lock")
    os.close(fd)
    return path


class GateLockTest(unittest.TestCase):
    def test_second_gate_waits_then_gets_lock(self):
        import threading, time
        orig = prepush.LOCK_PATH
        prepush.LOCK_PATH = _lock_file()
        try:
            first = prepush.acquire_lock()
            got = []
            t = threading.Thread(target=lambda: got.append(prepush.acquire_lock()))
            t.start()
            time.sleep(0.5)
            self.assertEqual(got, [])  # red control: blocked while the first holds it
            os.close(first)
            t.join(10)
            self.assertEqual(len(got), 1)
            self.assertIsNotNone(got[0])
            os.close(got[0])
        finally:
            prepush.LOCK_PATH = orig

    def test_lock_timeout_fails_closed(self):
        orig = (prepush.LOCK_PATH, prepush.LOCK_WAIT_S)
        prepush.LOCK_PATH, prepush.LOCK_WAIT_S = _lock_file(), 0
        try:
            first = prepush.acquire_lock()
            self.assertIsNone(prepush.acquire_lock())
            os.close(first)
        finally:
            prepush.LOCK_PATH, prepush.LOCK_WAIT_S = orig


class WebSkipTest(unittest.TestCase):
    def touched_by(self, names):
        orig = prepush.subprocess.run
        prepush.subprocess.run = lambda cmd, **kw: type("R", (), {"stdout": "abc\n" if "merge-base" in cmd else "\n".join(names)})()
        try:
            return prepush.web_touched()
        finally:
            prepush.subprocess.run = orig

    def test_path_matching_is_exact(self):
        self.assertFalse(self.touched_by(["Makefile.notes", "internal/web/x.go", "docs/web/a.md"]))  # red control: look-alikes do not count
        self.assertTrue(self.touched_by(["Makefile"]))
        self.assertTrue(self.touched_by(["web/src/a.tsx"]))

    def run_stages(self, touched):
        seen = []
        orig = (prepush.run_stage, prepush.web_touched)
        prepush.run_stage = lambda st: (seen.append(st) or (0, ""))
        prepush.web_touched = lambda: touched
        try:
            self.assertEqual(prepush.main(["prepush.py"]), 0)
        finally:
            prepush.run_stage, prepush.web_touched = orig
        return seen

    def test_go_only_diff_does_not_run_vitest(self):
        self.assertNotIn("ci-test-web", self.run_stages(False))

    def test_web_diff_runs_vitest(self):
        self.assertIn("ci-test-web", self.run_stages(True))


if __name__ == "__main__":
    unittest.main()
