#!/usr/bin/env python3
"""Merge only complete, valid atomic profiles from this gate invocation."""
from pathlib import Path
import sys

from diff_coverage import parse_profile


def merge(output: str, profiles: list[str]) -> None:
    if not profiles:
        raise ValueError("no expected coverage profiles")
    records = ["mode: atomic\n"]
    for path in profiles:
        parse_profile(path, "")  # validates every record, including the header
        lines = Path(path).read_text(encoding="utf-8").splitlines()
        if lines[0] != "mode: atomic":
            raise ValueError(f"{path}: expected race-compatible atomic coverage")
        records.extend(line + "\n" for line in lines[1:] if line.strip())
    Path(output).write_text("".join(records), encoding="utf-8")


if __name__ == "__main__":
    try:
        merge(sys.argv[1], sys.argv[2:])
    except (OSError, ValueError) as exc:
        sys.exit(f"ERROR: cannot merge coverage profiles: {exc}")
