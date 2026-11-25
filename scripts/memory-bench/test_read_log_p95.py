#!/usr/bin/env python3
"""The p95 receipt must reject missing controls and out-of-band latency."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from read_log_p95 import Client, percentile95, report

ROOT = Path(__file__).resolve().parents[2]
WATCHED = ["internal/middleware/memory_read_log.go",
           "internal/middleware/tool_breakdown_tracker.go",
           "internal/repository/postgres/memory_read_event_repo.go",
           "scripts/memory-bench/read_log_p95.py",
           "scripts/memory-bench/test_read_log_p95.py"]


def ci_p95_condition() -> str:
    """The shell test recall-gate-branch uses to decide whether p95 runs."""
    ci = (ROOT / ".gitlab/ci/memory.yml").read_text()
    match = re.search(r"if (! git [^\n]*diff --quiet .*?); then", ci, re.S)
    if match is None:
        raise AssertionError("recall-gate-branch has no read-log p95 diff condition")
    return match.group(1)


class ReadLogP95Test(unittest.TestCase):
    def test_nearest_rank_and_no_empty_measurements(self):
        self.assertEqual(percentile95(list(range(1, 101))), 95)
        with self.assertRaises(ValueError):
            percentile95([])

    def test_both_sides_of_five_percent_band_and_receipt(self):
        for latency, expected in [(94.9, False), (95, True), (100, True),
                                  (105, True), (105.1, False)]:
            with self.subTest(latency=latency):
                receipt = report([100] * 20, [latency] * 20, "main-sha", "branch-sha")
                self.assertIs(receipt["pass"], expected)
                self.assertEqual(receipt["main_p95_ms"], 100)
                self.assertEqual(receipt["branch_p95_ms"], latency)
                self.assertEqual(receipt["main_sha"], "main-sha")
                self.assertEqual(receipt["branch_sha"], "branch-sha")

    def test_measurements_cannot_seed_a_remote_api(self):
        for url in ["https://127.0.0.1:8005", "http://example.com:8005"]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                Client(url, "fixture")


class CiConditionTest(unittest.TestCase):
    """recall-gate-branch runs this test from scripts/memory-bench: on MR !2017
    the root-relative pathspecs matched nothing there and the p95 step was
    silently skipped on the very MR it was written for."""

    def run_condition(self, changed: str) -> bool:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
                   "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}

            def git(*args: str) -> str:
                return subprocess.run(["git", "-C", tmp, *args], check=True, env=env,
                                      capture_output=True, text=True).stdout.strip()

            git("init", "-q")
            for path in WATCHED + ["README.md"]:
                (repo / path).parent.mkdir(parents=True, exist_ok=True)
                (repo / path).write_text("base\n")
            git("add", "-A")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (repo / changed).write_text("changed\n")
            git("commit", "-qam", "change")
            head = git("rev-parse", "HEAD")
            result = subprocess.run(
                ["bash", "-c", ci_p95_condition()], cwd=repo / "scripts/memory-bench",
                env={**env, "CI_PROJECT_DIR": tmp, "CI_MERGE_REQUEST_DIFF_BASE_SHA": base,
                     "CI_COMMIT_SHA": head}, capture_output=True, text=True)
            self.assertIn(result.returncode, (0, 1), result.stderr)
            return result.returncode == 0

    def test_each_watched_file_runs_p95_from_the_bench_directory(self):
        for path in WATCHED:
            with self.subTest(path=path):
                self.assertTrue(self.run_condition(path))

    def test_an_unrelated_change_skips_p95(self):
        self.assertFalse(self.run_condition("README.md"))

    def test_main_api_starts_from_its_own_tree(self):
        # Pipeline 36269: started from scripts/memory-bench, the main API found
        # no `migrations` dir (goose.Up reads it relative to the cwd) and exited.
        ci = (ROOT / ".gitlab/ci/memory.yml").read_text()
        start = ci.index("## Recall p95: main and branch")
        block = ci[start:ci.index("## Recall p95: skipped", start)]
        launch = re.search(r"^(.*)\n(.*)/tmp/mesh-api-main \)? *>", block, re.M)
        self.assertIsNotNone(launch, "no main API launch in the p95 block")
        self.assertIn("cd /tmp/mesh-readlog-main &&", launch.group(0))
        self.assertIn("exec nohup /tmp/mesh-api-main", launch.group(0))

    def test_every_git_call_of_the_p95_step_is_rooted(self):
        # Pipeline 36244: `git archive` from scripts/memory-bench archived only
        # that subtree, so the main build failed with "go.mod file not found".
        ci = (ROOT / ".gitlab/ci/memory.yml").read_text()
        start = ci.index("## Recall p95: main and branch")
        block = ci[start:ci.index("## Recall p95: skipped", start)]
        calls = re.findall(r"(?:^|[|(;&]|\$\()\s*(git\s[^\n]*)", block, re.M)
        self.assertGreaterEqual(len(calls), 3, calls)
        for call in calls:
            with self.subTest(call=call):
                self.assertTrue(call.startswith('git -C "$CI_PROJECT_DIR" '), call)


if __name__ == "__main__":
    unittest.main()
