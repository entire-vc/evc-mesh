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


# Why a category cannot produce a verdict. The distinction is the whole point:
# the two causes take DIFFERENT remedies, and until 2026-08-08 both were reported
# as one message advising a re-snap — which is right for the first and useless
# for the second.
#
#   SPREAD — `spread > tolerance`. The capture is too noisy to rule on. A quieter,
#            isolated re-snap genuinely fixes it. Hard blocker.
#   SCORE  — the measured score is at or below one tolerance-width. No capture can
#            fix that: it is what the system scores. Telling the operator to
#            re-snap sends them to do work that cannot help, and refusing outright
#            means NO baseline can be committed while any category sits that low —
#            i.e. the whole gate enforces nothing, which is strictly worse than
#            six of seven categories enforcing. Blocker unless named explicitly.
INELIGIBLE_SPREAD = "spread"
INELIGIBLE_SCORE = "score"


def ineligible_categories(
    baseline: Baseline, tolerance: float
) -> list[tuple[str, str, float]]:
    """(category, cause, threshold) for every category the gate cannot rule on.

    Threshold is imported from `run_ci.effective_tolerance`, not restated, so this
    cannot drift from what the gate actually computes.
    """
    out: list[tuple[str, str, float]] = []
    for cat in sorted(baseline.scores):
        threshold = baseline.scores[cat] - effective_tolerance(cat, tolerance, baseline)
        if threshold > 0.0:
            continue
        spread = baseline.spread.get(cat, 0.0)
        cause = INELIGIBLE_SPREAD if spread > tolerance else INELIGIBLE_SCORE
        out.append((cat, cause, threshold))
    return out


def blockers(
    baseline: Baseline,
    tolerance: float,
    accept_ineligible: frozenset[str] = frozenset(),
) -> list[str]:
    """Reasons this file must not be committed. Empty list = fit to commit.

    `accept_ineligible` names categories whose SCORE-bounded ineligibility is
    accepted deliberately. It never silences a spread-blinded one — that has a
    working remedy, and accepting noise instead of re-snapping is how a floor
    gets pinned to a coin flip.
    """
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

    for cat, kind, threshold in ineligible_categories(baseline, tolerance):
        score = baseline.scores[cat]
        spread = baseline.spread.get(cat, 0.0)
        if kind == INELIGIBLE_SPREAD:
            out.append(
                f"{cat}: threshold {threshold:+.3f} ≤ 0 because spread "
                f"{spread:.3f} exceeds tolerance {tolerance:.3f} — the gate would "
                "rule it ineligible and print `ⓘ no verdict` for the life of this "
                "baseline. The capture is too noisy to rule on: re-snap it (quiet "
                f"window, isolated fixtures); score {score:.3f}"
            )
        elif cat not in accept_ineligible:
            out.append(
                f"{cat}: threshold {threshold:+.3f} ≤ 0 because the SCORE "
                f"{score:.3f} is at or below tolerance {tolerance:.3f}, with "
                f"spread {spread:.3f} — the gate will print `ⓘ no verdict` for "
                "the life of this baseline. RE-SNAPPING CANNOT FIX THIS: the "
                "number is what the system scores, not an artefact of the "
                "capture. Either raise the score, widen the category, change the "
                "tolerance policy for small categories — or accept it "
                f"deliberately with --accept-ineligible {cat}"
            )

    for cat in sorted(accept_ineligible):
        if cat not in baseline.scores:
            out.append(
                f"--accept-ineligible names {cat!r}, which is not in this "
                "baseline — an acknowledgement for a category that does not "
                "exist hides nothing and will outlive whatever it was for"
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
    parser.add_argument(
        "--accept-ineligible",
        action="append",
        default=[],
        metavar="CATEGORY",
        help=(
            "Accept that this category cannot produce a verdict because its "
            "SCORE is at or below tolerance. Must name the category, so the "
            "acknowledgement is specific and a NEW one still refuses. Never "
            "silences a spread-blinded category — re-snap that instead."
        ),
    )
    args = parser.parse_args()
    accepted = frozenset(args.accept_ineligible)

    if not args.baseline.exists():
        print(f"✗ {args.baseline} does not exist")
        return 1

    baseline = load_baseline(args.baseline)
    print(render(baseline, args.tolerance))
    print(f"\nsearch_mode={baseline.search_mode}  n_runs={baseline.n_runs}")

    # Printed whether or not it blocks: an accepted ineligibility is still a
    # category the gate will never rule on, and that must be visible every time
    # someone runs this — not only on the run where it was first accepted.
    for cat, cause, threshold in ineligible_categories(baseline, args.tolerance):
        if cause == INELIGIBLE_SCORE and cat in accepted:
            print(
                f"\n⚠ ACCEPTED ineligible: {cat} (threshold {threshold:+.3f}). "
                "The gate will print `ⓘ no verdict` for this category for the "
                "life of this baseline. Six of seven enforcing beats none."
            )

    reasons = blockers(baseline, args.tolerance, accepted)
    if reasons:
        print("\n✗ NOT fit to commit:")
        for reason in reasons:
            print(f"  - {reason}")
        return 1

    print("\n✓ fit to commit")
    return 0


if __name__ == "__main__":
    sys.exit(main())
