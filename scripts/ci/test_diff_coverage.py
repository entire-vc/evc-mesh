#!/usr/bin/env python3
"""Self-checks for the diff-coverage gate.

A gate is only worth having if it fails on bad input. This suite therefore spends
most of its effort on the failing directions — the previous coverage gate was green
for six weeks for the wrong reason, and nobody noticed because nobody tested that it
could go red.
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from diff_coverage import (  # noqa: E402
    EXIT_BELOW_THRESHOLD,
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    changed_line_ranges,
    main,
    measure,
    parse_profile,
)

MODULE = "github.com/entire-vc/evc-mesh"


def write(path: str, text: str) -> str:
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)
    return path


class TestProfileParsing(unittest.TestCase):
    def test_strips_the_module_prefix_so_paths_match_the_diff(self):
        with tempfile.TemporaryDirectory() as d:
            p = write(os.path.join(d, "c.out"),
                      "mode: atomic\n"
                      f"{MODULE}/internal/a/b.go:10.1,12.2 3 1\n")
            blocks = parse_profile(p, MODULE)
        self.assertIn("internal/a/b.go", blocks)
        self.assertEqual(blocks["internal/a/b.go"], [(10, 12, 3, 1)])

    def test_test_files_are_not_measured(self):
        with tempfile.TemporaryDirectory() as d:
            p = write(os.path.join(d, "c.out"),
                      "mode: atomic\n"
                      f"{MODULE}/internal/a/b_test.go:1.1,2.2 1 1\n")
            self.assertEqual(parse_profile(p, MODULE), {})

    def test_malformed_lines_are_skipped_not_fatal(self):
        with tempfile.TemporaryDirectory() as d:
            p = write(os.path.join(d, "c.out"),
                      "mode: atomic\ngarbage\n"
                      f"{MODULE}/internal/a/b.go:1.1,2.2 1 0\n")
            self.assertEqual(len(parse_profile(p, MODULE)["internal/a/b.go"]), 1)


class TestMeasure(unittest.TestCase):
    def test_counts_only_blocks_the_change_touches(self):
        changed = {"a.go": {11}}
        blocks = {"a.go": [(10, 12, 3, 1), (50, 60, 7, 0)]}
        covered, total, uncovered = measure(changed, blocks)
        self.assertEqual((covered, total), (3, 3))
        self.assertEqual(uncovered, [], "an untouched uncovered block must not count")

    def test_untested_new_code_is_reported_uncovered(self):
        changed = {"a.go": {51}}
        blocks = {"a.go": [(50, 60, 7, 0)]}
        covered, total, uncovered = measure(changed, blocks)
        self.assertEqual((covered, total), (0, 7))
        self.assertEqual(len(uncovered), 1)

    def test_a_changed_file_absent_from_the_profile_contributes_nothing(self):
        covered, total, _ = measure({"missing.go": {1}}, {"a.go": [(1, 2, 1, 1)]})
        self.assertEqual((covered, total), (0, 0))

    def test_pre_existing_debt_elsewhere_cannot_drag_the_number_down(self):
        # The whole point of the change: a huge uncovered file that the diff does
        # not touch must not appear in the denominator.
        changed = {"mine.go": {5}}
        blocks = {
            "mine.go": [(1, 10, 4, 1)],
            "legacy.go": [(1, 900, 800, 0)],
        }
        covered, total, _ = measure(changed, blocks)
        self.assertEqual((covered, total), (4, 4))


class TestDiffParsing(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        run = lambda *a: subprocess.run(a, cwd=self.dir, check=True,
                                        capture_output=True, text=True)
        run("git", "init", "-q")
        run("git", "config", "user.email", "t@t.t")
        run("git", "config", "user.name", "t")
        write(os.path.join(self.dir, "keep.go"), "package a\n\nfunc A() {}\n")
        run("git", "add", "-A")
        run("git", "commit", "-qm", "base")
        self.base = run("git", "rev-parse", "HEAD").stdout.strip()
        self.run = run

    def test_added_lines_are_detected_and_context_is_not(self):
        write(os.path.join(self.dir, "keep.go"),
              "package a\n\nfunc A() {}\n\nfunc B() {\n\tprintln(1)\n}\n")
        self.run("git", "add", "-A")
        self.run("git", "commit", "-qm", "add B")
        changed = changed_line_ranges(self.base, self.dir)
        self.assertIn("keep.go", changed)
        # Only the appended lines; the untouched first three must be absent.
        self.assertTrue(changed["keep.go"].issuperset({5, 6, 7}))
        self.assertNotIn(1, changed["keep.go"])

    def test_test_files_are_excluded_from_the_diff_side_too(self):
        write(os.path.join(self.dir, "a_test.go"), "package a\n\nfunc TestX() {}\n")
        self.run("git", "add", "-A")
        self.run("git", "commit", "-qm", "add test")
        self.assertEqual(changed_line_ranges(self.base, self.dir), {})

    def test_a_pure_deletion_yields_no_lines_to_cover(self):
        write(os.path.join(self.dir, "keep.go"), "package a\n")
        self.run("git", "add", "-A")
        self.run("git", "commit", "-qm", "shrink")
        changed = changed_line_ranges(self.base, self.dir)
        self.assertEqual(changed.get("keep.go", set()), set())


class TestExitCodes(unittest.TestCase):
    """The contract the workflow depends on: 1 and 2 must not be interchangeable."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        run = lambda *a: subprocess.run(a, cwd=self.dir, check=True,
                                        capture_output=True, text=True)
        run("git", "init", "-q")
        run("git", "config", "user.email", "t@t.t")
        run("git", "config", "user.name", "t")
        write(os.path.join(self.dir, "go.mod"), f"module {MODULE}\n\ngo 1.22\n")
        write(os.path.join(self.dir, "a.go"), "package a\n")
        run("git", "add", "-A")
        run("git", "commit", "-qm", "base")
        self.base = run("git", "rev-parse", "HEAD").stdout.strip()
        self.run = run

    def _commit_lines(self, n: int):
        body = "package a\n" + "".join(f"// line {i}\n" for i in range(n))
        write(os.path.join(self.dir, "a.go"), body)
        self.run("git", "add", "-A")
        self.run("git", "commit", "-qm", "grow")

    def _invoke(self, profile_text: str | None, threshold: float = 80.0) -> int:
        prof = os.path.join(self.dir, "c.out")
        if profile_text is not None:
            write(prof, profile_text)
        argv = sys.argv
        sys.argv = ["diff_coverage.py", "--profile", prof, "--base", self.base,
                    "--threshold", str(threshold), "--repo-root", self.dir]
        try:
            return main()
        finally:
            sys.argv = argv

    def test_missing_profile_is_inconclusive_not_a_failure(self):
        self._commit_lines(5)
        self.assertEqual(self._invoke(None), EXIT_INCONCLUSIVE)

    def test_uncovered_new_code_fails(self):
        self._commit_lines(5)
        code = self._invoke("mode: atomic\n"
                            f"{MODULE}/a.go:2.1,5.2 4 0\n")
        self.assertEqual(code, EXIT_BELOW_THRESHOLD)

    def test_covered_new_code_passes(self):
        self._commit_lines(5)
        code = self._invoke("mode: atomic\n"
                            f"{MODULE}/a.go:2.1,5.2 4 3\n")
        self.assertEqual(code, EXIT_OK)

    def test_no_go_change_at_all_passes(self):
        write(os.path.join(self.dir, "README.md"), "hi\n")
        self.run("git", "add", "-A")
        self.run("git", "commit", "-qm", "docs")
        self.assertEqual(self._invoke("mode: atomic\n"), EXIT_OK)

    def test_profile_that_misses_every_changed_file_is_inconclusive(self):
        # Silent mis-measurement is the failure mode that made the old gate
        # useless, so "measured nothing at all" must not read as success.
        self._commit_lines(5)
        code = self._invoke("mode: atomic\n"
                            f"{MODULE}/somewhere/else.go:1.1,2.2 2 1\n")
        self.assertEqual(code, EXIT_INCONCLUSIVE)

    def test_threshold_boundary_is_inclusive(self):
        self._commit_lines(5)
        # exactly 80%
        code = self._invoke("mode: atomic\n"
                            f"{MODULE}/a.go:2.1,3.2 4 1\n"
                            f"{MODULE}/a.go:4.1,5.2 1 0\n", threshold=80.0)
        self.assertEqual(code, EXIT_OK, "exactly at the threshold must pass")


if __name__ == "__main__":
    unittest.main(verbosity=2)
