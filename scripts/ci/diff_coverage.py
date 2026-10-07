#!/usr/bin/env python3
"""Gate on the coverage of the lines a change actually touches.

The previous gate measured the aggregate coverage of the whole reverse-dependency
closure of the changed packages. That number is dominated by pre-existing debt in
packages the author never opened: a change to internal/repository/postgres drags in
handler, middleware, service and ws, whose combined coverage is ~28%. An 80%
threshold against that is unreachable by construction, so the gate could only ever
be passed by not touching the lower layers — which is not a coverage policy, it is
a freeze.

Measuring the touched lines instead asks the question the gate was written to ask:
is the code in THIS change tested? Pre-existing debt is then a separate problem
with a separate remedy, and it stops holding unrelated work hostage.

Exit codes:
    0  coverage meets the threshold, or there was nothing to measure
    1  coverage below the threshold  (a real, author-clearable failure)
    2  could not measure             (missing profile, unreadable diff)

2 is deliberately distinct from 1. "I could not tell" must never render as "you
made it worse" — an infrastructure outage that reads as a quality regression
teaches people to ignore the gate.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
import tempfile
from collections import defaultdict

EXIT_OK = 0
EXIT_BELOW_THRESHOLD = 1
EXIT_INCONCLUSIVE = 2

# github.com/org/repo/internal/foo/bar.go:12.34,15.2 3 1
PROFILE_LINE = re.compile(
    r"^(?P<file>.+\.go):(?P<start>\d+)\.(?P<start_col>\d+),"
    r"(?P<end>\d+)\.(?P<end_col>\d+)\s+(?P<stmts>\d+)\s+(?P<count>\d+)$"
)
# @@ -12,3 +14,7 @@
HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(?P<start>\d+)(?:,(?P<len>\d+))? @@")


def changed_line_ranges(base: str, repo_root: str) -> dict[str, set[int]]:
    """Return {repo-relative .go path: set of added/modified line numbers}.

    Uses -U0 so the hunks carry no context lines: context is unchanged code, and
    counting it would quietly re-import the pre-existing-debt problem this script
    exists to remove.

    Test files are excluded. A test is the instrument, not the subject; requiring
    tests to be covered by tests measures nothing and pushes authors toward
    writing tests for their tests to clear a gate.
    """
    try:
        out = subprocess.run(
            ["git", "diff", "-U0", "--diff-filter=d", base, "HEAD", "--", "*.go"],
            cwd=repo_root,
            capture_output=True,
            text=True,
            check=True,
        ).stdout
    except (subprocess.CalledProcessError, FileNotFoundError) as exc:
        raise RuntimeError(f"could not read the diff against {base}: {exc}") from exc

    changed: dict[str, set[int]] = defaultdict(set)
    current: str | None = None
    for line in out.splitlines():
        if line.startswith("+++ "):
            path = line[4:].strip()
            if path == "/dev/null":
                current = None
            else:
                # strip the b/ prefix git puts on the post-image path
                current = path[2:] if path.startswith("b/") else path
                if current.endswith("_test.go"):
                    current = None
            continue
        if current is None:
            continue
        m = HUNK.match(line)
        if m:
            start = int(m.group("start"))
            length = int(m.group("len") or 1)
            # length 0 means the hunk only deletes; there is no new line to cover.
            for n in range(start, start + length):
                changed[current].add(n)
    return changed


def parse_profile(path: str, module: str) -> dict[str, list[tuple[int, int, int, int, int, int]]]:
    """Return file records with start/end line and column, statement and hit counts.

    Coverage profiles name files by import path; the diff names them relative to the
    repo root. Stripping the module prefix is what lets the two be intersected at
    all, so a wrong module name shows up as "nothing measured" rather than an error
    — hence the explicit no-overlap diagnosis in main().
    """
    blocks: dict[str, list[tuple[int, int, int, int]]] = defaultdict(list)
    prefix = module.rstrip("/") + "/"
    with open(path, encoding="utf-8") as fh:
        header = fh.readline().strip()
        if header not in {"mode: atomic", "mode: count", "mode: set"}:
            raise ValueError(f"{path}: missing or invalid coverage mode")
        for number, raw in enumerate(fh, start=2):
            raw = raw.strip()
            if not raw:
                continue
            m = PROFILE_LINE.match(raw)
            if not m:
                raise ValueError(f"{path}:{number}: invalid coverage record")
            start = (int(m.group("start")), int(m.group("start_col")))
            end = (int(m.group("end")), int(m.group("end_col")))
            if start[0] < 1 or start[1] < 1 or end < start:
                raise ValueError(f"{path}:{number}: invalid coverage range")
            f = m.group("file")
            rel = f[len(prefix):] if f.startswith(prefix) else f
            if rel.endswith("_test.go"):
                continue
            blocks[rel].append((*start, *end, int(m.group("stmts")), int(m.group("count"))))
    return blocks


def instrumented_blocks(path: str, repo_root: str) -> list[tuple[int, int, int, int, int]]:
    """Get the executable blocks Go expects, including blocks absent from a profile."""
    with tempfile.TemporaryDirectory() as directory:
        output = os.path.join(directory, "instrumented.go")
        try:
            subprocess.run(
                ["go", "tool", "cover", "-mode=atomic", "-o", output, path],
                cwd=repo_root, capture_output=True, text=True, check=True,
            )
            with open(output, encoding="utf-8") as fh:
                instrumented = fh.read()
        except (OSError, subprocess.CalledProcessError) as exc:
            raise ValueError(f"cannot inspect Go coverage blocks in {path}: {exc}") from exc
    positions = re.search(r"Pos:\s*\[3\s*\*\s*(\d+)\]uint32\s*\{(.*?)\n\s*\}",
                          instrumented, re.DOTALL)
    statements = re.search(r"NumStmt:\s*\[(\d+)\]uint16\s*\{(.*?)\n\s*\}",
                           instrumented, re.DOTALL)
    if not positions or not statements:
        raise ValueError(f"cannot read Go coverage instrumentation for {path}")
    pos = re.findall(r"^\s*(\d+),\s*(\d+),\s*(0x[0-9a-fA-F]+|\d+),",
                     positions.group(2), re.MULTILINE)
    nums = re.findall(r"^\s*(\d+)\s*,", statements.group(2), re.MULTILINE)
    if not (len(pos) == len(nums) == int(positions.group(1)) == int(statements.group(1))):
        raise ValueError(f"incomplete Go coverage instrumentation for {path}")
    result = []
    for (start, end, packed), num in zip(pos, nums):
        # GoCover.Pos packs end column in the high half and start column low.
        columns = int(packed, 0)
        result.append((int(start), columns & 0xffff, int(end), columns >> 16, int(num)))
    return result


def measure(changed: dict[str, set[int]],
            blocks: dict[str, list[tuple[int, int, int, int, int, int]]]
            ) -> tuple[int, int, list[str]]:
    """Intersect changed lines with coverage blocks.

    A block counts when any changed line falls inside it. Attribution is
    block-level because a Go profile records statement counts per block and not
    per line — there is no finer signal available, and inventing one would make
    the number look more precise than it is.
    """
    total = covered = 0
    uncovered: list[str] = []
    for path, lines in sorted(changed.items()):
        for start, _, end, _, stmts, count in blocks.get(path, []):
            if any(start <= n <= end for n in lines):
                total += stmts
                if count > 0:
                    covered += stmts
                else:
                    uncovered.append(f"{path}:{start}-{end} ({stmts} stmt)")
    return covered, total, uncovered


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--profile", required=True, help="merged Go coverage profile")
    ap.add_argument("--base", required=True, help="base git ref to diff against")
    ap.add_argument("--threshold", type=float, default=80.0)
    ap.add_argument("--repo-root", default=".")
    ap.add_argument("--module", default="")
    ap.add_argument("--exclude-file", action="append", default=[],
                    help="post-image source of a confirmed main package")
    args = ap.parse_args()

    module = args.module
    if not module:
        gomod = os.path.join(args.repo_root, "go.mod")
        try:
            with open(gomod, encoding="utf-8") as fh:
                for line in fh:
                    if line.startswith("module "):
                        module = line.split(None, 1)[1].strip()
                        break
        except OSError as exc:
            print(f"::error::cannot read {gomod}: {exc}")
            return EXIT_INCONCLUSIVE
    if not module:
        print("::error::could not determine the Go module path")
        return EXIT_INCONCLUSIVE

    if not os.path.exists(args.profile):
        print(f"::error::coverage profile not found: {args.profile}")
        return EXIT_INCONCLUSIVE

    try:
        changed = changed_line_ranges(args.base, args.repo_root)
    except RuntimeError as exc:
        print(f"::error::{exc}")
        return EXIT_INCONCLUSIVE

    if not changed:
        print("No non-test Go lines changed — nothing to gate.")
        return EXIT_OK

    changed = {path: lines for path, lines in changed.items() if path not in args.exclude_file}
    try:
        blocks = parse_profile(args.profile, module)
        missing = []
        for path, lines in changed.items():
            measured = {block[:5] for block in blocks.get(path, [])}
            for block in instrumented_blocks(path, args.repo_root):
                start, _, end, _, stmts = block
                if stmts > 0 and any(start <= line <= end for line in lines) and block not in measured:
                    missing.append(f"{path}:{start}-{end}")
        if missing:
            raise ValueError(f"coverage profile is missing changed executable blocks: {missing}")
    except (OSError, ValueError) as exc:
        print(f"::error::cannot measure diff coverage: {exc}")
        return EXIT_INCONCLUSIVE
    covered, total, uncovered = measure(changed, blocks)

    if total == 0:
        # Missing executable files were rejected above. The remaining changes
        # touch only comments, imports, declarations or unchanged block gaps.
        print("Changed Go lines contain no measurable statements — nothing to gate.")
        return EXIT_OK

    pct = 100.0 * covered / total
    print(f"Diff coverage: {covered}/{total} statements = {pct:.1f}% "
          f"(threshold {args.threshold:.0f}%)")
    if uncovered:
        print("Uncovered changed blocks:")
        for u in uncovered[:40]:
            print(f"  {u}")
        if len(uncovered) > 40:
            print(f"  … and {len(uncovered) - 40} more")

    if pct + 1e-9 < args.threshold:
        print(f"::error::Diff coverage {pct:.1f}% < {args.threshold:.0f}% threshold — "
              "add tests for the lines this change introduces")
        return EXIT_BELOW_THRESHOLD
    print(f"Diff coverage {pct:.1f}% >= {args.threshold:.0f}% ✓")
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
