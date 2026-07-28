#!/usr/bin/env python3
"""Does the recall gate, loaded with a REAL committed baseline, still reach REGRESSION?

The point of this task is the claim that the required check "cannot produce a
REGRESSION verdict at all" while its baseline file is missing. Committing the
file makes the check produce *a* verdict — but "it produced a verdict" and "it
can produce the RED verdict" are different claims, and only the second is the
one that was broken.

A gate run on the same branch the baseline was captured from passes tautologically:
identical code, identical data, scores equal to baseline. That PASS is evidence
about the plumbing and nothing else. So drive the pure decision functions
directly, against the file as committed:

  ARM A (identity)   scores == baseline            -> must be EXIT_OK
  ARM B (degraded)   one category dropped past its
                     effective tolerance           -> must be EXIT_REGRESSION,
                                                      naming that category
  ARM C (control)    same drop, but SMALLER than
                     the tolerance                 -> must stay EXIT_OK

ARM C is what makes ARM B mean something: without it, a "REGRESSION" could just
be a gate that reddens on any difference at all, which would be a different
defect wearing the same colour.

Usage:  prove_gate_can_go_red.py <baseline.json> <arm>
        arm is 'branch' or 'prod' — checked against the file's own recorded arm,
        because a baseline used in the wrong arm is refused upstream and this
        script must not quietly evidence a comparison the real gate would reject.

Exit 0 = all three arms behaved; 1 = an arm did not; 2 = could not run.
"""
from __future__ import annotations

import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

try:
    from run_ci import (  # noqa: E402
        EXIT_OK,
        EXIT_REGRESSION,
        classify,
        decide_verdict,
        effective_tolerance,
        load_baseline,
    )
except Exception as exc:  # pragma: no cover - import failure is a rig failure
    print(f"cannot import run_ci: {exc}")
    raise SystemExit(2)

DEFAULT_TOLERANCE = 0.25


def _sizes_from(baseline) -> dict[str, tuple[int, int]]:
    """Reuse the baseline's own recorded denominators.

    Not invented: `classify` refuses a category whose run-side sample differs
    from the baseline's (`_denominator_mismatch`), so a made-up denominator
    would make every arm below INELIGIBLE and all three would "pass" by never
    being ruled on at all.
    """
    if baseline.sample_sizes:
        return dict(baseline.sample_sizes)
    return {}


def _run(label: str, scores, baseline, sizes, expect_rc, expect_named=None) -> bool:
    verdicts = classify(scores, baseline, DEFAULT_TOLERANCE, sizes)
    verdict = decide_verdict(verdicts)
    ok = verdict.exit_code == expect_rc
    if expect_named is not None:
        ok = ok and expect_named in verdict.regressions
    ruled = [v.category for v in verdicts if v.eligible]
    print(f"  {label}")
    print(f"    exit_code   = {verdict.exit_code}  (expected {expect_rc})")
    print(f"    regressions = {verdict.regressions}")
    print(f"    unmeasured  = {verdict.unmeasured}")
    print(f"    spread_blind= {verdict.spread_blind}")
    print(f"    categories actually ruled on = {len(ruled)}  {ruled}")
    print(f"    -> {'PASS' if ok else 'FAIL'}")
    return ok


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    path = Path(sys.argv[1])
    want_arm = sys.argv[2]
    if not path.exists():
        print(f"baseline does not exist: {path}")
        return 2

    baseline = load_baseline(path)
    print(f"baseline file : {path}")
    print(f"  arm         = {baseline.arm!r} (expected {want_arm!r})")
    print(f"  search_mode = {baseline.search_mode!r}")
    print(f"  n_runs      = {baseline.n_runs}")
    print(f"  scores      = {baseline.scores}")
    print(f"  spread      = {baseline.spread}")
    print(f"  sample_sizes= {baseline.sample_sizes}")
    if baseline.arm != want_arm:
        print(f"REFUSED: file records arm {baseline.arm!r}, not {want_arm!r}")
        return 2

    sizes = _sizes_from(baseline)
    if not sizes:
        print("REFUSED: baseline records no sample_sizes; the denominator check "
              "cannot be honoured and every arm below would be vacuously green")
        return 2

    # Pick the category with the most headroom, so the injected drop is
    # unambiguous rather than riding on the tolerance boundary.
    ruleable = [
        c for c in baseline.scores
        if baseline.scores[c] - effective_tolerance(c, DEFAULT_TOLERANCE, baseline) > 0.0
    ]
    if not ruleable:
        print("REFUSED: no category has a threshold above zero — the gate is "
              "structurally unable to rule on this baseline, which is itself "
              "the finding, but it is not a REGRESSION proof")
        return 2
    target = max(ruleable, key=lambda c: baseline.scores[c])
    tol = effective_tolerance(target, DEFAULT_TOLERANCE, baseline)
    print(f"\ntarget category: {target}  baseline={baseline.scores[target]} "
          f"effective_tolerance={tol}")

    ok = True
    print("\nARM A — identity (scores == baseline): must be OK")
    ok &= _run("identity", dict(baseline.scores), baseline, sizes, EXIT_OK)

    print("\nARM B — one category dropped PAST its tolerance: must be REGRESSION")
    degraded = dict(baseline.scores)
    degraded[target] = max(0.0, baseline.scores[target] - tol - 0.01)
    ok &= _run(f"{target} {baseline.scores[target]} -> {degraded[target]:.4f}",
               degraded, baseline, sizes, EXIT_REGRESSION, expect_named=target)

    print("\nARM C — control: same category dropped LESS than its tolerance, "
          "must stay OK")
    inside = dict(baseline.scores)
    inside[target] = max(0.0, baseline.scores[target] - tol + 0.01)
    ok &= _run(f"{target} {baseline.scores[target]} -> {inside[target]:.4f}",
               inside, baseline, sizes, EXIT_OK)

    print("\n" + ("ALL THREE ARMS BEHAVED" if ok else "AN ARM DID NOT BEHAVE"))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
