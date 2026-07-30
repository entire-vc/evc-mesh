#!/usr/bin/env python3
"""The measurement window must be able to hold the dataset.

`rows_returned` is capped by the page the server may return. When that cap is
below a question's haystack, the question is unreachable BY CONSTRUCTION: the
shortfall appears in the artifact as though retrieval had missed rows, and no
amount of filter or ranking work can recover them. Four of the 24 questions
(52/52/53/54 sessions) sat under exactly that cap at `RECALL_CANDIDATE_LIMIT=50`.

This is the check that was missing, not a defect that was found: the lesson from
#1e4bd289 is to test an acceptance criterion for ATTAINABILITY against the
harness parameters BEFORE spending a run on it. A gate whose target is
arithmetically impossible reports the impossibility as a failure of the thing
under test.

What is pinned is the EFFECTIVE window, not the raw constant, because those are
different numbers whenever graph boost is on: the graph arm reserves
`graphBoostReserve(limit)` = limit/4 of the page for neighbours, so base
retrieval sees limit*3/4.

    effective window = RECALL_CANDIDATE_LIMIT - reserve   (reserve = limit//4
                                                           when the workflow
                                                           enables graph boost)

and that must clear `max(haystack)` with headroom.

HONEST LIMITATION, stated because a silent version of it is what this file is
about: `graphBoostReserve` lives in **evc-mesh-mcp**. `RESERVE_DIVISOR` below is a
MIRROR of a constant no test in this repo can read. If someone retunes it there
to limit/2, this check keeps passing while the real window drops to 40 — under
max(haystack) — and the gate's score falls for a purely structural reason with
nothing local to explain it. A mirror is the most this repo can check; it is not
a guarantee, and the divisor is named and commented so the coupling is at least
visible to whoever greps for it.

Every test here carries a positive control, because each one's failure mode is to
pass having measured nothing: a dataset loader that finds no questions makes the
window pin vacuous, and a regex that matches nothing would silently downgrade the
reserve to zero and so overstate the window.
"""

from __future__ import annotations

import json
import re
import sys
import unittest
from pathlib import Path

BENCH_DIR = Path(__file__).resolve().parent
DATASET = BENCH_DIR / "data" / "lme_s_24.json"
WORKFLOW = BENCH_DIR.parent.parent / ".github" / "workflows" / "memory-bench.yml"

sys.path.insert(0, str(BENCH_DIR))
from mesh_client_stdio import RECALL_CANDIDATE_LIMIT  # noqa: E402

# Slack above the longest haystack. Not decoration: the point of the pin is that
# a question landing ON the limit is served a full page with zero spare rows,
# which is indistinguishable in the artifact from one that was truncated. Enough
# room that a somewhat longer question added later still measures, and fails this
# check well before it silently caps a run.
REQUIRED_HEADROOM = 4

# MIRROR of `graphBoostReserve` in evc-mesh-mcp (internal/mcp/tools.go):
#     reserve := limit / 4
# See the module docstring: this repo cannot read that constant, so this is a copy
# that can drift. Kept as a named constant rather than inlined so the coupling is
# greppable from both ends.
RESERVE_DIVISOR = 4

# Any assignment of the var in the workflow, at any indent, quoted or not.
SETS_GRAPH_VAR = re.compile(r"^\s*RECALL_GRAPH_ENABLED\s*:", re.M)


def haystack_sizes() -> list[int]:
    entries = json.loads(DATASET.read_text(encoding="utf-8"))
    return [len(e.get("haystack_sessions") or []) for e in entries]


def graph_boost_enabled() -> bool:
    """Whether the bench workflow turns graph boost on for its recalls."""
    return bool(SETS_GRAPH_VAR.search(WORKFLOW.read_text(encoding="utf-8")))


def effective_window() -> int:
    """Rows of BASE retrieval one recall can return — the real ceiling."""
    if graph_boost_enabled():
        return RECALL_CANDIDATE_LIMIT - RECALL_CANDIDATE_LIMIT // RESERVE_DIVISOR
    return RECALL_CANDIDATE_LIMIT


class TestWindowHoldsTheDataset(unittest.TestCase):
    def test_the_effective_window_clears_the_longest_haystack(self):
        sizes = haystack_sizes()
        longest = max(sizes)
        window = effective_window()
        boost = graph_boost_enabled()
        how = (
            f"RECALL_CANDIDATE_LIMIT={RECALL_CANDIDATE_LIMIT} minus a "
            f"limit//{RESERVE_DIVISOR} graph-boost reserve = {window}"
            if boost
            else f"RECALL_CANDIDATE_LIMIT={RECALL_CANDIDATE_LIMIT} (graph boost off) = {window}"
        )
        unreachable = sorted({s for s in sizes if s > window})
        self.assertEqual(
            [], unreachable,
            f"the effective window ({how}) cannot hold haystacks of {unreachable}: "
            f"those questions can never reach rows_returned == haystack_size, and "
            f"the gap will read in the artifact as a retrieval miss. Raise "
            f"RECALL_CANDIDATE_LIMIT until the window clears {longest}.",
        )
        self.assertGreaterEqual(
            window, longest + REQUIRED_HEADROOM,
            f"the effective window ({how}) leaves under {REQUIRED_HEADROOM} rows of "
            f"headroom over the longest haystack ({longest}). A question served a "
            f"page with no spare rows cannot be told apart from one that was "
            f"truncated.",
        )

    def test_the_dataset_was_actually_read(self):
        """Positive control on the pin above.

        An empty or shape-changed dataset makes `unreachable` empty and `max()`
        raise or return 0 — the assertion would pass having examined nothing,
        which is the exact zero-denominator failure this file exists to catch one
        level up.
        """
        sizes = haystack_sizes()
        self.assertEqual(
            24, len(sizes),
            f"expected 24 questions in {DATASET.name}, found {len(sizes)} — the pin "
            f"above was checking a dataset that is not the one the gate runs.",
        )
        self.assertTrue(
            all(s > 0 for s in sizes),
            f"a question reports a zero-length haystack: {sizes}. `haystack_sessions` "
            f"is not being read, so every size compares below the limit for free.",
        )


class TestTheReserveIsNotSilentlyMissed(unittest.TestCase):
    """Positive controls on the reserve half of `effective_window()`.

    The dangerous direction here is NOT "graph boost is on". It is failing to
    NOTICE that it is on: `graph_boost_enabled()` returning False when the
    workflow does enable it makes `effective_window()` report the raw limit,
    overstating the window by 25% — and the pin above would then certify a
    dataset the gate cannot actually hold. That is the same shape as the defect
    this file guards, one level down.
    """

    def test_the_matcher_catches_the_line_as_the_workflow_writes_it(self):
        # Indented inside a step's `env:` block — how it actually appears.
        self.assertTrue(
            SETS_GRAPH_VAR.search("        env:\n          RECALL_GRAPH_ENABLED: 'true'\n")
        )
        self.assertTrue(SETS_GRAPH_VAR.search("RECALL_GRAPH_ENABLED: true\n"))
        # And does not fire on prose naming the var, or the harness forwarding it —
        # a matcher that fires on a comment would invent a reserve that isn't there.
        self.assertIsNone(SETS_GRAPH_VAR.search("# RECALL_GRAPH_ENABLED is off\n"))
        self.assertIsNone(
            SETS_GRAPH_VAR.search('for key in ("MESH_API_URL", "RECALL_GRAPH_ENABLED"):\n')
        )

    def test_the_reserve_is_actually_being_subtracted(self):
        """The workflow enables graph boost today, so the window MUST be reduced.

        If this fails because the workflow legitimately stopped enabling it, the
        assertion below is the place to update — deliberately, not by having the
        window quietly widen underneath the pin.
        """
        self.assertTrue(
            graph_boost_enabled(),
            "memory-bench.yml no longer sets RECALL_GRAPH_ENABLED. That is a real "
            "change in what the gate measures: the window widens from limit*3/4 to "
            "limit. Update this test on purpose if it was intended.",
        )
        self.assertLess(
            effective_window(), RECALL_CANDIDATE_LIMIT,
            "graph boost is enabled but effective_window() returned the raw limit — "
            "the reserve is not being subtracted, so the window is overstated by "
            f"limit//{RESERVE_DIVISOR} rows.",
        )

    def test_the_workflow_was_actually_found(self):
        """Guards against the path going stale: a missing file read as empty text
        would make `graph_boost_enabled()` return False for ever — silently
        removing the reserve from the calculation."""
        self.assertTrue(WORKFLOW.is_file(), f"{WORKFLOW} not found")
        self.assertIn(
            "MESH_MCP_BIN", WORKFLOW.read_text(encoding="utf-8"),
            "the workflow was read but does not look like the bench workflow",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
