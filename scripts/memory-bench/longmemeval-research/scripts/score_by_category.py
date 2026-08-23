#!/usr/bin/env python3
"""Aggregate a judged LongMemEval results file into a per-category score table.

Takes a `.eval-openai_gpt-4o` file (produced by `evaluate_results.py`, which
runs the paid LLM judge) and the local `data/question_categories.json`
lookup, and prints pass/total + percentage per `question_type`, plus the
overall total. Does not call any API and needs no network access — this is
pure aggregation over already-judged rows, so it's the reproducibility check
for a claimed score without re-running (and re-paying for) the benchmark.

Usage:
    python3 score_by_category.py results/full500-minefix-20260821.jsonl.eval-openai_gpt-4o
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
DATA_DIR = SCRIPT_DIR.parent / "data"
CATEGORIES_FILE = DATA_DIR / "question_categories.json"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", help="Judged .eval-openai_gpt-4o JSONL file")
    parser.add_argument(
        "--categories",
        default=str(CATEGORIES_FILE),
        help=f"question_id -> question_type lookup JSON (default: {CATEGORIES_FILE})",
    )
    args = parser.parse_args()

    categories = json.loads(Path(args.categories).read_text())

    totals: dict[str, list[int]] = defaultdict(lambda: [0, 0])
    overall = [0, 0]
    unknown = 0

    with open(args.results) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            qid = row["question_id"]
            qtype = categories.get(qid)
            if qtype is None:
                unknown += 1
                qtype = "UNKNOWN"

            label = row.get("autoeval_label")
            ok = bool(label["label"]) if isinstance(label, dict) else bool(label)

            totals[qtype][1] += 1
            overall[1] += 1
            if ok:
                totals[qtype][0] += 1
                overall[0] += 1

    for qtype in sorted(totals):
        passed, total = totals[qtype]
        print(f"{qtype}: {passed}/{total} = {100 * passed / total:.1f}%")
    print(f"OVERALL: {overall[0]}/{overall[1]} = {100 * overall[0] / overall[1]:.1f}%")

    if unknown:
        print(f"WARNING: {unknown} question_id(s) not found in categories lookup", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
