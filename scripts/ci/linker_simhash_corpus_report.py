#!/usr/bin/env python3
"""Keep the corpus measurements from a successful verbose Go run in JUnit."""
import re
import sys
from html import escape
from pathlib import Path

TEST = "TestLinkerSimhashMaxBits_Corpus"
CASES = (
    TEST,
    TEST + "/few_unrelated_pairs_get_the_simhash_signal",
    TEST + "/every_1-2_word_edit_gets_the_simhash_signal",
)


def report(log):
    if re.search(r"--- (?:FAIL|SKIP):|^FAIL\b|^panic:", log, re.M):
        raise ValueError("corpus run failed or skipped")
    rows = ['<?xml version="1.0" encoding="utf-8"?>',
            '<testsuite name="linker-simhash-corpus" tests="3" failures="0" errors="0" skipped="0">']
    for case in CASES:
        if len(re.findall(r"^=== RUN   " + re.escape(case) + r"$", log, re.M)) != 1:
            raise ValueError("missing or duplicate RUN: " + case)
        passed = re.findall(r"^\s*--- PASS: " + re.escape(case) + r" \(([0-9]+(?:\.[0-9]+)?)s\)$", log, re.M)
        if len(passed) != 1:
            raise ValueError("missing or duplicate PASS: " + case)
        rows.append(f'<testcase classname="internal/reconciler" name="{escape(case, quote=True)}" time="{passed[0]}"/>')
    pairs = re.findall(r"unrelated pairs within (\d+) bits: (\d+) of (\d+) \(([0-9.]+)%\)", log)
    edits = re.findall(r"worst 1-2 word edit: (\d+) bits", log)
    if len(pairs) != 1 or len(edits) != 1:
        raise ValueError("missing or duplicate corpus measurement")
    bits, within, total = map(int, pairs[0][:3])
    worst = int(edits[0])
    if bits != 10 or total != 1225 or not 0 <= within <= total / 100 or not 0 <= worst <= bits:
        raise ValueError("corpus measurements outside tested bounds")
    if pairs[0][3] != f"{100 * within / total:.2f}":
        raise ValueError("corpus share disagrees with pair counts")
    rows.append("<properties>")
    for name, value in (("threshold_bits", bits), ("unrelated_within", within),
                        ("unrelated_total", total), ("edit_max_bits", worst)):
        rows.append(f'<property name="simhash.{escape(name, quote=True)}" value="{value}"/>')
    rows.extend(["</properties>", "</testsuite>"])
    return "\n".join(rows) + "\n"


if __name__ == "__main__":
    Path(sys.argv[2]).write_text(report(Path(sys.argv[1]).read_text()), encoding="utf-8")
