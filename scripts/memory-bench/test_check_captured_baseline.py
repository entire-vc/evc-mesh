#!/usr/bin/env python3
"""Self-check for the pre-commit baseline checker.

Stdlib `unittest`, no dependencies, same convention as the other bench tests:

    python scripts/memory-bench/test_check_captured_baseline.py

The property that matters: the file actually committed as
`baseline_retrieval.json` passes this checker. Everything else here exists to
prove the checker can still say no — a checker that only ever returns 0 is
indistinguishable from no checker at all, and that is precisely the failure
mode it was written to catch (`run_ci.py` already *warned* about the short
capture in evc-mesh#362; the warning changed nothing).
"""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from check_captured_baseline import (  # noqa: E402
    EXPECTED_SAMPLES,
    blockers,
    ineligible_categories,
)
from run_ci import load_baseline  # noqa: E402

HERE = Path(__file__).resolve().parent

# A sound capture: hybrid, 3 passes, every category on its full denominator, and
# no spread wide enough to blind a row.
SOUND = {
    "search_mode": "hybrid",
    "captured_at": "2026-07-26T15:15:02Z",
    "top_k": 10,
    "n_runs": 3,
    "scores": {
        "knowledge-update": 1.0,
        "multi-session": 1.0,
        "overall": 0.9027777777777777,
        "single-session-assistant": 1.0,
        "single-session-preference": 0.9166666666666666,
        "single-session-user": 0.5,
        "temporal-reasoning": 1.0,
    },
    "spread": {
        "knowledge-update": 0.0,
        "multi-session": 0.0,
        "overall": 0.04166666666666663,
        "single-session-assistant": 0.0,
        "single-session-preference": 0.25,
        "single-session-user": 0.0,
        "temporal-reasoning": 0.0,
    },
    "sample_sizes": {cat: [n, n] for cat, n in EXPECTED_SAMPLES.items()},
}


def check(payload: dict, tolerance: float = 0.25) -> list[str]:
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "b.json"
        path.write_text(json.dumps(payload))
        return blockers(load_baseline(path), tolerance)


class TestTheCommittedBaseline(unittest.TestCase):
    def test_the_baseline_in_the_tree_is_fit_to_commit(self):
        """The whole point. If this fails, the required gate has a bad floor."""
        baseline = load_baseline(HERE / "baseline_retrieval.json")
        self.assertEqual(blockers(baseline, 0.25), [])

    def test_the_BRANCH_baseline_in_the_tree_is_fit_to_commit(self):
        """The file behind the REQUIRED check — and it was not covered here.

        This class checked `baseline_retrieval.json` only, which is the PROD
        canary's floor. The required, merge-blocking arm reads
        `baseline_retrieval_branch.json`, so the checker that exists to keep a
        bad floor out of the tree was pointed at the advisory file and not at
        the blocking one. Same shape as the gate's own failure modes: a guard
        aimed one file to the left of the thing it is guarding.

        `single-session-preference` is accepted deliberately: it scores 0.250
        against a 0.250 tolerance, so its threshold is exactly 0 and the gate
        cannot rule on it. That is NOT a capture artefact — `main` scores the
        same 0.250 on those four questions — so no re-snap can clear it. The
        acknowledgement is named rather than blanket, so a SECOND category
        going ineligible still fails this test.
        """
        baseline = load_baseline(HERE / "baseline_retrieval_branch.json")
        self.assertEqual(
            blockers(baseline, 0.25, frozenset({"single-session-preference"})), []
        )

    def test_the_branch_acknowledgement_is_still_needed_and_not_wider(self):
        """Two directions at once: if the accepted category ever becomes
        rulable, this test says so (delete the acknowledgement); and the
        acknowledgement must not be silently covering a second category."""
        baseline = load_baseline(HERE / "baseline_retrieval_branch.json")
        ineligible = {c for c, _, _ in ineligible_categories(baseline, 0.25)}
        self.assertEqual(
            {"single-session-preference"}, ineligible,
            "the set of categories the branch gate cannot rule on has changed — "
            "update the acknowledgement in the test above and say why in the PR, "
            "rather than widening it",
        )

    def test_the_committed_baseline_covers_all_24_questions(self):
        baseline = load_baseline(HERE / "baseline_retrieval.json")
        self.assertEqual(baseline.sample_sizes["overall"], (24, 24))
        self.assertEqual(baseline.sample_sizes["temporal-reasoning"], (4, 4))

    def test_the_committed_baseline_is_repeated_and_hybrid(self):
        """A single pass records no spread, so every threshold silently falls
        back to --tolerance and the gate compares one sample against another."""
        baseline = load_baseline(HERE / "baseline_retrieval.json")
        self.assertEqual(baseline.search_mode, "hybrid")
        self.assertGreater(baseline.n_runs, 1)

    def test_the_denominators_match_the_dataset_on_disk(self):
        """EXPECTED_SAMPLES is stated, not read back out of the file it judges —
        so it is pinned against the real dataset instead."""
        questions = json.loads((HERE / "data" / "lme_s_24.json").read_text())
        counts: dict[str, int] = {}
        for q in questions:
            counts[q["question_type"]] = counts.get(q["question_type"], 0) + 1
        counts["overall"] = len(questions)
        self.assertEqual(counts, EXPECTED_SAMPLES)


class TestItCanStillSayNo(unittest.TestCase):
    def test_a_sound_capture_passes(self):
        self.assertEqual(check(SOUND), [])

    def test_a_baseline_with_no_denominator_is_refused(self):
        payload = {k: v for k, v in SOUND.items() if k != "sample_sizes"}
        reasons = check(payload)
        self.assertTrue(any("no `sample_sizes`" in r for r in reasons), reasons)

    def test_the_superseded_name_samples_reads_as_absent_and_is_refused(self):
        """#364's `samples` lost to #363's `sample_sizes`. A file written under
        the dead name does not error — `load_baseline` returns {} and the
        denominator guard is inert. That is the trap this case pins."""
        payload = {k: v for k, v in SOUND.items() if k != "sample_sizes"}
        payload["samples"] = {cat: [n, n] for cat, n in EXPECTED_SAMPLES.items()}
        reasons = check(payload)
        self.assertTrue(any("no `sample_sizes`" in r for r in reasons), reasons)

    def test_a_short_denominator_is_refused(self):
        """The evc-mesh#362 shape exactly: temporal-reasoning on 2 of 4."""
        payload = json.loads(json.dumps(SOUND))
        payload["sample_sizes"]["temporal-reasoning"] = [2, 2]
        reasons = check(payload)
        self.assertTrue(
            any("temporal-reasoning" in r and "holds 4" in r for r in reasons), reasons
        )

    def test_an_incomplete_run_of_a_full_category_is_refused(self):
        payload = json.loads(json.dumps(SOUND))
        payload["sample_sizes"]["multi-session"] = [3, 4]
        reasons = check(payload)
        self.assertTrue(
            any("only 3 of 4" in r for r in reasons), reasons
        )

    def test_a_degraded_mode_is_refused(self):
        payload = json.loads(json.dumps(SOUND))
        payload["search_mode"] = "vector-only"
        reasons = check(payload)
        self.assertTrue(any("search_mode" in r for r in reasons), reasons)

    def test_a_category_blinded_by_its_own_spread_is_refused(self):
        """A wide spread does not just widen the band — past `score`, it drives
        the threshold to ≤0 and `classify` rules the row ineligible, printing
        `ⓘ no verdict` for the life of the baseline. rc=0 at capture time."""
        payload = json.loads(json.dumps(SOUND))
        payload["spread"]["single-session-user"] = 0.5  # score is 0.5
        reasons = check(payload)
        self.assertTrue(
            any("single-session-user" in r and "no verdict" in r for r in reasons),
            reasons,
        )

    def test_the_committed_spread_is_not_close_to_blinding_its_row(self):
        """single-session-preference carries the widest real spread (0.250) and
        is the row that would blind first. Recorded so a future re-snap that
        drifts toward it fails here rather than in six weeks of silence."""
        baseline = load_baseline(HERE / "baseline_retrieval.json")
        score = baseline.scores["single-session-preference"]
        spread = baseline.spread["single-session-preference"]
        self.assertGreater(score - max(0.25, spread), 0.0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
