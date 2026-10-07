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
    instrumented_blocks,
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
        self.assertEqual(blocks["internal/a/b.go"], [(10, 1, 12, 2, 3, 1)])

    def test_test_files_are_not_measured(self):
        with tempfile.TemporaryDirectory() as d:
            p = write(os.path.join(d, "c.out"),
                      "mode: atomic\n"
                      f"{MODULE}/internal/a/b_test.go:1.1,2.2 1 1\n")
            self.assertEqual(parse_profile(p, MODULE), {})

    def test_malformed_lines_are_measurement_errors(self):
        with tempfile.TemporaryDirectory() as d:
            p = write(os.path.join(d, "c.out"),
                      "mode: atomic\ngarbage\n"
                      f"{MODULE}/internal/a/b.go:1.1,2.2 1 0\n")
            with self.assertRaises(ValueError):
                parse_profile(p, MODULE)

    def test_empty_or_duplicate_header_is_an_error(self):
        with tempfile.TemporaryDirectory() as d:
            for text in ("", "garbage\n", "mode: atomic\nmode: atomic\n"):
                with self.subTest(text=text), self.assertRaises(ValueError):
                    parse_profile(write(os.path.join(d, "c.out"), text), MODULE)


class TestMeasure(unittest.TestCase):
    def test_counts_only_blocks_the_change_touches(self):
        changed = {"a.go": {11}}
        blocks = {"a.go": [(10, 1, 12, 2, 3, 1), (50, 1, 60, 2, 7, 0)]}
        covered, total, uncovered = measure(changed, blocks)
        self.assertEqual((covered, total), (3, 3))
        self.assertEqual(uncovered, [], "an untouched uncovered block must not count")

    def test_untested_new_code_is_reported_uncovered(self):
        changed = {"a.go": {51}}
        blocks = {"a.go": [(50, 1, 60, 2, 7, 0)]}
        covered, total, uncovered = measure(changed, blocks)
        self.assertEqual((covered, total), (0, 7))
        self.assertEqual(len(uncovered), 1)

    def test_a_changed_file_absent_from_the_profile_contributes_nothing(self):
        covered, total, _ = measure({"missing.go": {1}}, {"a.go": [(1, 1, 2, 2, 1, 1)]})
        self.assertEqual((covered, total), (0, 0))

    def test_pre_existing_debt_elsewhere_cannot_drag_the_number_down(self):
        # The whole point of the change: a huge uncovered file that the diff does
        # not touch must not appear in the denominator.
        changed = {"mine.go": {5}}
        blocks = {
            "mine.go": [(1, 1, 10, 2, 4, 1)],
            "legacy.go": [(1, 1, 900, 2, 800, 0)],
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
        body = "package a\nfunc F() {\n" + "".join(f"println({i})\n" for i in range(n)) + "}\n"
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

    def _profile(self, path: str = "a.go", *, include=None, hit=None) -> str:
        """Build fixtures from the installed Go toolchain's block positions."""
        records = ["mode: atomic"]
        for index, (start, start_col, end, end_col, stmts) in enumerate(
            instrumented_blocks(path, self.dir)
        ):
            if include is not None and not include(index, start, end):
                continue
            count = 1 if hit is None or hit(index) else 0
            records.append(
                f"{MODULE}/{path}:{start}.{start_col},{end}.{end_col} {stmts} {count}"
            )
        return "\n".join(records) + "\n"

    def test_missing_profile_is_inconclusive_not_a_failure(self):
        self._commit_lines(5)
        self.assertEqual(self._invoke(None), EXIT_INCONCLUSIVE)

    def test_uncovered_new_code_fails(self):
        self._commit_lines(5)
        code = self._invoke(self._profile(hit=lambda _: False))
        self.assertEqual(code, EXIT_BELOW_THRESHOLD)

    def test_covered_new_code_passes(self):
        self._commit_lines(5)
        code = self._invoke(self._profile())
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

    def test_header_only_cannot_hide_executable_diff(self):
        self._commit_lines(5)
        self.assertEqual(self._invoke("mode: atomic\n"), EXIT_INCONCLUSIVE)

    def test_malformed_profile_cannot_hide_uncovered_records(self):
        self._commit_lines(5)
        self.assertEqual(self._invoke("mode: atomic\ngarbage\n"), EXIT_INCONCLUSIVE)

    def test_missing_one_changed_file_is_inconclusive_even_with_other_covered_code(self):
        self._commit_lines(5)
        write(os.path.join(self.dir, "missing.go"), "package a\nfunc Missing() { println(1) }\n")
        self.run("git", "add", "missing.go")
        self.run("git", "commit", "-qm", "add missing executable file")
        self.assertEqual(self._invoke(self._profile()),
                         EXIT_INCONCLUSIVE)

    def test_partial_file_profile_cannot_hide_a_changed_block(self):
        path = os.path.join(self.dir, "a.go")
        write(path, "package a\nfunc Old() { println(1) }\n")
        self.run("git", "add", "a.go")
        self.run("git", "commit", "-qm", "add existing block")
        self.base = self.run("git", "rev-parse", "HEAD").stdout.strip()
        write(path, "package a\nfunc Old() { println(1) }\nfunc New() { println(2) }\n")
        self.run("git", "add", "a.go")
        self.run("git", "commit", "-qm", "add changed block")

        partial = self._profile(include=lambda _, start, __: start < 3)
        self.assertEqual(self._invoke(partial), EXIT_INCONCLUSIVE)
        complete = self._profile()
        self.assertEqual(self._invoke(complete), EXIT_OK)

    def test_confirmed_declaration_only_diff_passes_without_blocks(self):
        write(os.path.join(self.dir, "a.go"), "package a\n// comment\ntype X struct{}\n")
        self.run("git", "add", "a.go")
        self.run("git", "commit", "-qm", "add declarations")
        self.assertEqual(self._invoke("mode: atomic\n"), EXIT_OK)

    def test_threshold_boundary_is_inclusive(self):
        write(os.path.join(self.dir, "a.go"),
              "package a\nfunc F() {\nprintln(0)\nprintln(1)\nprintln(2)\n"
              "if true { println(3) }\n}\n")
        self.run("git", "add", "a.go")
        self.run("git", "commit", "-qm", "add two executable blocks")
        partial = self._profile(include=lambda index, *_: index == 0)
        self.assertEqual(self._invoke(partial), EXIT_INCONCLUSIVE,
                         "another block on the same changed line cannot hide an omission")
        # Exactly 80% once both instrumented blocks are present.
        code = self._invoke(self._profile(hit=lambda index: index == 0), threshold=80.0)
        self.assertEqual(code, EXIT_OK, "exactly at the threshold must pass")


if __name__ == "__main__":
    unittest.main(verbosity=2)
