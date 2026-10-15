#!/usr/bin/env python3
"""Fail if the `recall-gate-branch` job-creation globs drift from MEMORY_PATHS.

    python3 scripts/ci/check_memory_gate_scope_sync.py [--selftest]

`.gitlab-ci.yml` carries the memory-gate scope twice, in two syntaxes:

  * `variables: MEMORY_PATHS` - newline-separated path PREFIXES, read at run time
    by `scripts/memory-bench/scope_relevant.sh` (the guard inside the job);
  * `.memory-gate-scope` - `rules:changes:paths` GLOBS, read by GitLab to decide
    whether the job is created at all.

GitLab cannot derive one from the other, so this check pins them together: every
prefix must appear in the globs (a directory prefix `dir/` as `dir/**/*`), and
the globs between the BEGIN/END markers must hold nothing else. A prefix missing
from the globs would let a memory change skip the required gate; an extra glob
is harmless but is a sign one list was edited by hand. Stdlib only, no yaml
dependency, because the job that runs it has none.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

CI_FILE = Path(__file__).resolve().parents[2] / ".gitlab-ci.yml"
MARK_BEGIN = "# BEGIN derived-from-MEMORY_PATHS"
MARK_END = "# END derived-from-MEMORY_PATHS"


def memory_paths(text: str) -> list[str]:
    block = re.search(r"^  MEMORY_PATHS: \|\n((?:    .*\n)+)", text, re.M)
    if not block:
        raise SystemExit("MEMORY_PATHS block not found in .gitlab-ci.yml")
    return [ln.strip() for ln in block.group(1).splitlines() if ln.strip()]


def scope_globs(text: str) -> list[str]:
    start = text.find(MARK_BEGIN)
    end = text.find(MARK_END)
    if start < 0 or end < start:
        raise SystemExit(f"{MARK_BEGIN} / {MARK_END} markers not found in .gitlab-ci.yml")
    out = []
    for ln in text[start + len(MARK_BEGIN):end].splitlines():
        m = re.match(r'^\s*-\s+"?([^"\s]+)"?\s*$', ln)
        if m:
            out.append(m.group(1))
    return out


def expected_globs(paths: list[str]) -> list[str]:
    return [p + "**/*" if p.endswith("/") else p for p in paths]


def check(text: str) -> list[str]:
    want = expected_globs(memory_paths(text))
    have = scope_globs(text)
    problems = []
    for g in want:
        if g not in have:
            problems.append(f"MEMORY_PATHS entry has no matching glob in .memory-gate-scope: {g}")
    for g in have:
        if g not in want:
            problems.append(f".memory-gate-scope glob has no MEMORY_PATHS entry: {g}")
    if not want:
        problems.append("MEMORY_PATHS is empty")
    return problems


def selftest() -> None:
    good = (
        '  MEMORY_PATHS: |\n    a/b.go\n    dir/\n\n'
        f'          {MARK_BEGIN}\n          - "a/b.go"\n          - "dir/**/*"\n          {MARK_END}\n'
    )
    assert check(good) == [], check(good)
    assert check(good.replace('          - "dir/**/*"\n', "")), "a missing glob must be reported"
    assert check(good.replace("    dir/\n", "    dir/\n    c.go\n")), "a new prefix without a glob must be reported"
    assert check(good.replace('- "a/b.go"', '- "a/b.go"\n          - "x/y.go"')), "an orphan glob must be reported"
    print("selftest ok")


def main() -> int:
    if "--selftest" in sys.argv[1:]:
        selftest()
        return 0
    problems = check(CI_FILE.read_text(encoding="utf-8"))
    for p in problems:
        print(f"memory-gate scope drift: {p}")
    if problems:
        print("Keep MEMORY_PATHS and the BEGIN/END block of .memory-gate-scope in .gitlab-ci.yml in step.")
        return 1
    print("memory-gate scope: MEMORY_PATHS and .memory-gate-scope agree")
    return 0


if __name__ == "__main__":
    sys.exit(main())
