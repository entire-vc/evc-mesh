#!/usr/bin/env python3
"""Check a captured baseline before it is committed.

The capture guard in `run_ci.py` (`capture_blockers`) refuses to *write* a
baseline taken from an incomplete or degraded run. That covers the run. It does
not cover the file, and the file is what a required check reads for weeks.

Two properties survive a clean capture and are invisible at write time:

  1. **No denominator.** `sample_sizes` is what arms #361's baseline-denominator
     guard. A baseline missing it — or written under the superseded name
     `samples` — loads as `sample_sizes={}`, and the guard is then inert by
     design. That is exactly how `temporal-reasoning: 1.0` sat on 2 of 4
     questions for six days (evc-mesh#362) with nothing in the tree able to say
     so.

  2. **A category blinded by its own spread.** The gate compares against
     `baseline - max(tolerance, spread)`. Where that threshold falls to zero or
     below, `classify` rules the category ineligible and prints `ⓘ no verdict` —
     for the life of the baseline. A wide spread therefore does not merely widen
     the band, it can switch a category off, and the capture that does it exits
     0 and looks healthy.

Both are properties of the artifact, so they are checked on the artifact, right
before it is committed. Every rule here is imported from `run_ci` rather than
restated — a checker that keeps its own copy of the threshold formula will
eventually check a rule the gate no longer applies.

Usage:
    python3 check_captured_baseline.py baseline_retrieval.json [--tolerance 0.25]

Exit codes:
    0  fit to commit
    1  not fit to commit — reasons on stdout
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from run_ci import (
    MODE_HYBRID,
    Baseline,
    effective_tolerance,
    load_baseline,
)

# The required arm's dataset: 24 questions, 7 categories. A capture that scored
# a category on fewer questions than it contains is the defect this file exists
# to keep out of the tree, so the expected denominators are stated rather than
# read back out of the same file being judged.
EXPECTED_SAMPLES = {
    "knowledge-update": 4,
    "multi-session": 4,
    "single-session-assistant": 4,
    "single-session-preference": 4,
    "single-session-user": 4,
    "temporal-reasoning": 4,
    "overall": 24,
}


def blockers(baseline: Baseline, tolerance: float) -> list[str]:
    """Reasons this file must not be committed. Empty list = fit to commit."""
    out: list[str] = []

    if baseline.search_mode != MODE_HYBRID:
        out.append(
            f"search_mode is {baseline.search_mode!r}, not {MODE_HYBRID!r} — "
            "committing it pins a required check at a degraded quality level"
        )

    if not baseline.sample_sizes:
        out.append(
            "no `sample_sizes` — the baseline-denominator guard stays inert. "
            "(A capture written under the superseded name `samples` lands here: "
            "it reads as absent, not as an error.)"
        )
    else:
        for cat, want in sorted(EXPECTED_SAMPLES.items()):
            got = baseline.sample_sizes.get(cat)
            if got is None:
                out.append(f"{cat}: no denominator recorded")
            elif got[1] != want:
                out.append(
                    f"{cat}: measured on {got[0]}/{got[1]} questions, "
                    f"but the category holds {want}"
                )
            elif got[0] != got[1]:
                out.append(
                    f"{cat}: only {got[0]} of {got[1]} questions ran — an "
                    "incomplete denominator"
                )

    for cat in sorted(baseline.scores):
        threshold = baseline.scores[cat] - effective_tolerance(cat, tolerance, baseline)
        if threshold <= 0.0:
            out.append(
                f"{cat}: threshold {threshold:+.3f} ≤ 0 — the gate would rule it "
                "ineligible and print `ⓘ no verdict` for the life of this "
                f"baseline (score {baseline.scores[cat]:.3f}, "
                f"spread {baseline.spread.get(cat, 0.0):.3f})"
            )

    return out


def render(baseline: Baseline, tolerance: float) -> str:
    lines = [
        f"{'category':<30}{'score':>7}{'spread':>8}{'thresh':>8}{'sample':>9}"
        f"  verdict-eligible",
    ]
    for cat in sorted(baseline.scores):
        score = baseline.scores[cat]
        spread = baseline.spread.get(cat, 0.0)
        threshold = score - effective_tolerance(cat, tolerance, baseline)
        size = baseline.sample_sizes.get(cat)
        sample = f"{size[0]}/{size[1]}" if size else "—"
        lines.append(
            f"{cat:<30}{score:>7.3f}{spread:>8.3f}{threshold:>8.3f}{sample:>9}"
            f"  {'yes' if threshold > 0.0 else 'NO'}"
        )
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("--tolerance", type=float, default=0.25)
    args = parser.parse_args()

    if not args.baseline.exists():
        print(f"✗ {args.baseline} does not exist")
        return 1

    baseline = load_baseline(args.baseline)
    print(render(baseline, args.tolerance))
    print(f"\nsearch_mode={baseline.search_mode}  n_runs={baseline.n_runs}")

    reasons = blockers(baseline, args.tolerance)
    if reasons:
        print("\n✗ NOT fit to commit:")
        for reason in reasons:
            print(f"  - {reason}")
        return 1

    print("\n✓ fit to commit")
    return 0


if __name__ == "__main__":
    sys.exit(main())
