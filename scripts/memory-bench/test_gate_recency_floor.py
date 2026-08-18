#!/usr/bin/env python3
"""Self-check for the recency control's floor: "cannot measure" != "measured broken".

    python scripts/memory-bench/test_gate_recency_floor.py   # or: python -m unittest

The positive control asserts that decay DEMOTES gold, so the only movement it can
observe runs downward. When gold already sits in the last position with decay off,
`rank_on > rank_off` is unsatisfiable — the pass condition is unreachable, not merely
unmet, and the arm can only ever go red.

That red used to say "the bench is still blind to memory age: either the backdate did
not reach the rows the recall ranked, or the decay parameter did not reach the server."
Neither is true at the floor, and both send the reader into plumbing that works. Live
on run 32084076208 all four (age_mode x decay) measurements reported gold_rank=5 of
rows=5, while `--selftest` simultaneously certified gold as the strict content winner
([3,2,2,1,0] stem overlap) — the fixture was never the problem.

Both directions are pinned here, and the second is the one that keeps this honest:

  1. gold last with decay off  ⇒ INCONCLUSIVE, control-unarmed, never REGRESSION.
  2. every other shape is UNCHANGED. A genuine no-movement result with headroom
     still reds, a sign error still reds, a valid demotion still passes, and the
     negative control is not touched at all. A guard that quietly turned the whole
     arm inconclusive would look identical to this one on case 1 alone.
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import recency_control  # noqa: E402
from run_ci import EXIT_INCONCLUSIVE, EXIT_OK, EXIT_REGRESSION  # noqa: E402


class _Client:
    """Stands in for MeshMemoryClient: `run` only reads the ranked-list length."""

    def __init__(self, rows: int):
        self.ranked_records = [{"i": i} for i in range(rows)]
        self.rows_returned = rows
        self.search_mode = "hybrid"


def _run(expect: str, *, rank_on, rank_off, rows: int = 5):
    """Drive run() with the two arms stubbed to the ranks under test."""
    calls = iter(((rank_on, _Client(rows)), (rank_off, _Client(rows))))
    with mock.patch.object(recency_control, "_measure", lambda *a, **k: next(calls)):
        return recency_control.run(expect, recency_control.DEFAULT_TOP_K)


class RecencyFloor(unittest.TestCase):
    # ── 1. the floor itself ───────────────────────────────────────────────────
    def test_gold_last_with_decay_off_is_unarmed_not_regression(self):
        """The observed shape: rank 5 of 5 in both arms."""
        self.assertEqual(_run("visible", rank_on=5, rank_off=5, rows=5), EXIT_INCONCLUSIVE)

    def test_floor_holds_for_any_corpus_size(self):
        """The guard is 'last', not the literal number 5."""
        self.assertEqual(_run("visible", rank_on=3, rank_off=3, rows=3), EXIT_INCONCLUSIVE)

    # ── 2. the negative controls — everything else must be untouched ──────────
    def test_no_movement_with_headroom_still_reds(self):
        """THE control on this change. Gold at rank 2 of 5 has somewhere to fall;
        if it does not, the bench really is blind and that must still fail."""
        self.assertEqual(_run("visible", rank_on=2, rank_off=2, rows=5), EXIT_REGRESSION)

    def test_valid_demotion_still_passes(self):
        """The measurement the guard must never swallow (run 31274262413: 2 -> 5)."""
        self.assertEqual(_run("visible", rank_on=5, rank_off=2, rows=5), EXIT_OK)

    def test_sign_error_still_reds(self):
        """Decay promoting the oldest row is a sign error, not a floor."""
        self.assertEqual(_run("visible", rank_on=1, rank_off=4, rows=5), EXIT_REGRESSION)

    def test_negative_control_arm_is_not_gated_by_the_floor(self):
        """--expect blind REQUIRES agreement; agreement at the floor is a real pass.
        Applying the guard here would turn the negative control inconclusive for
        exactly the outcome it exists to observe."""
        self.assertEqual(_run("blind", rank_on=5, rank_off=5, rows=5), EXIT_OK)

    def test_negative_control_still_reds_when_the_toggle_moves_an_unaged_corpus(self):
        self.assertEqual(_run("blind", rank_on=3, rank_off=5, rows=5), EXIT_REGRESSION)

    # ── 3. the pre-existing unarmed case must keep its own reason ─────────────
    def test_gold_absent_is_still_unarmed(self):
        self.assertEqual(_run("visible", rank_on=None, rank_off=None, rows=5), EXIT_INCONCLUSIVE)

    # ── 4. gold present with decay off, absent with it ────────────────────────
    def test_gold_absent_only_with_decay_on_is_unarmed_not_a_crash(self):
        """Regression: this reached `rank_on < rank_off` with rank_on=None and
        raised TypeError, so the required job went red with a traceback and no
        verdict — reached by the strongest demotion the control can observe."""
        self.assertEqual(_run("visible", rank_on=None, rank_off=2, rows=5), EXIT_INCONCLUSIVE)

    def test_gold_absent_with_decay_on_does_not_crash_at_the_floor_either(self):
        """Floor guard is evaluated on rank_off and must not be confused by it."""
        self.assertEqual(_run("visible", rank_on=None, rank_off=5, rows=5), EXIT_INCONCLUSIVE)


if __name__ == "__main__":
    unittest.main(verbosity=2)
