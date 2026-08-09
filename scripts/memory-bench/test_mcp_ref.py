"""#faf92388 — which mesh-mcp CLIENT the bench measured.

All three arms used to check `entire-vc/evc-mesh-mcp` out with no `ref:`, so every
arm always built the default branch and no client pull request was measurable by
this harness at all. The failure was silent in the worst direction: runs looked
complete, the gate ruled, and the thing it could not vary was a binary that
overrides the caller's recall parameters from a query profile.

These tests pin three separate things, because fixing only the first would leave
the hole reachable:
  1. the workflow structurally cannot check that repo out unpinned again,
  2. a run states which client it measured, in both artifacts,
  3. a run on a different client is INCONCLUSIVE, never REGRESSION, and cannot
     be written as a baseline.
"""

import json
import os
import re
import tempfile
import unittest
from pathlib import Path

import run_ci
from run_ci import (
    MCP_REF_UNSTATED_AS,
    Baseline,
    build_baseline_payload,
    capture_blockers,
    load_baseline,
    resolve_mcp_commit,
    resolve_mcp_ref,
    write_results_artifact,
)

WORKFLOW = (
    Path(__file__).resolve().parents[2] / ".github" / "workflows" / "memory-bench.yml"
)


class TestEveryClientCheckoutIsPinnable(unittest.TestCase):
    """Enumerate the checkouts by STRUCTURE, not by the count we happen to know.

    The original defect was three unpinned checkouts. A test asserting "three
    are pinned" passes the day someone adds a fourth — which is exactly how this
    class of hole comes back, and the reason the assertion below is over every
    block that names the repository rather than over a number.
    """

    def setUp(self):
        self.src = WORKFLOW.read_text()
        # A checkout step is a `uses: actions/checkout` whose `with:` block names
        # the client repo. Split on the step boundary (`      - name:`) so each
        # candidate is one whole step and a `ref:` belonging to a NEIGHBOUR step
        # cannot be miscredited to this one.
        steps = re.split(r"\n      - (?:name|uses):", self.src)
        self.client_checkouts = [
            s for s in steps
            if "actions/checkout" in s and "repository: entire-vc/evc-mesh-mcp" in s
        ]

    def test_the_detector_finds_the_checkouts_at_all(self):
        # Positive control. Without it every assertion below is vacuously true
        # the moment the regex stops matching — a detector narrowed to zero is
        # exactly as green as a passing one.
        self.assertGreaterEqual(
            len(self.client_checkouts), 3,
            "expected at least the branch / prod-canary / advisory client "
            "checkouts; the step-splitting regex has probably drifted",
        )

    def test_every_client_checkout_pins_a_ref(self):
        for step in self.client_checkouts:
            self.assertIn(
                "ref: ${{ inputs.mcp_ref }}", step,
                "a checkout of the client without `ref:` always builds the "
                "default branch, which makes client PRs unmeasurable and says "
                "nothing about it in the output (#faf92388). Step:\n" + step[:400],
            )

    def test_every_client_checkout_is_followed_by_the_resolve_step(self):
        """Pinning without recording is half the fix.

        A pinned checkout whose arm never exports MCP_REF produces a run that
        measured a branch client and reports the default one — a mis-statement,
        which is worse than the original silence.
        """
        # Count occurrences rather than parse ordering: one resolve step per
        # client checkout is the invariant, and both are per-job.
        self.assertGreaterEqual(
            self.src.count("Resolve which mesh-mcp this run measures"),
            len(self.client_checkouts),
            "every arm that checks the client out must also export MCP_REF",
        )

    def test_the_input_defaults_to_empty(self):
        """Empty must reproduce today's behaviour.

        `pull_request`, `merge_group`, `push` and `schedule` populate no inputs,
        so `${{ inputs.mcp_ref }}` is '' on all of them. actions/checkout treats
        a falsy ref as unset. A non-empty default would silently repoint every
        one of those triggers.
        """
        block = self.src.split("mcp_ref:")[1].split("repeat:")[0]
        self.assertIn("default: ''", block)

    def test_the_input_is_passed_through_env_not_interpolated_into_shell(self):
        for step in re.split(r"\n      - name:", self.src):
            if "Resolve which mesh-mcp" not in step:
                continue
            run_body = step.split("run: |")[1]
            self.assertNotIn(
                "${{ inputs.mcp_ref }}", run_body,
                "a dispatch input interpolated into a shell body is an "
                "injection sink; pass it via env and read $MCP_REF_INPUT",
            )
            self.assertIn("MCP_REF_INPUT", run_body)


class TestRunStatesWhichClientItMeasured(unittest.TestCase):
    def setUp(self):
        self._saved = {k: os.environ.get(k) for k in ("MCP_REF", "MCP_COMMIT")}

    def tearDown(self):
        for k, v in self._saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v

    def test_unset_reads_as_the_default_branch(self):
        os.environ.pop("MCP_REF", None)
        self.assertEqual(MCP_REF_UNSTATED_AS, resolve_mcp_ref())

    def test_a_set_ref_is_reported_verbatim(self):
        os.environ["MCP_REF"] = "linus/some-branch"
        self.assertEqual("linus/some-branch", resolve_mcp_ref())

    def test_whitespace_only_is_not_a_ref(self):
        os.environ["MCP_REF"] = "   "
        self.assertEqual(MCP_REF_UNSTATED_AS, resolve_mcp_ref())

    def test_commit_is_reported_but_has_no_default(self):
        """`unreported` and `main` are different claims and must stay so.

        The commit deliberately gets no fallback: inventing one would assert a
        build that was never observed. The ref gets one because unstated there
        has a known correct reading.
        """
        os.environ.pop("MCP_COMMIT", None)
        self.assertEqual("", resolve_mcp_commit())
        os.environ["MCP_COMMIT"] = "deadbeefcafe"
        self.assertEqual("deadbeefcafe", resolve_mcp_commit())

    def test_results_artifact_carries_both_fields(self):
        os.environ["MCP_REF"] = "some-branch"
        os.environ["MCP_COMMIT"] = "abc123def456"
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "recall_gate.json"
            write_results_artifact(
                p, [], retrieval_only=True, run_mode="hybrid", top_k=10,
                repeat=1, scores={}, sizes={},
            )
            payload = json.loads(p.read_text())
        self.assertEqual("some-branch", payload["mcp_ref"])
        self.assertEqual("abc123def456", payload["mcp_commit"])

    def test_baseline_payload_carries_the_ref_but_not_the_commit(self):
        """The commit is observability; gating on it would gate on nothing else.

        `main` moves under every run, so a baseline pinning a client COMMIT
        would be incomparable to the very next run of identical code.
        """
        os.environ["MCP_REF"] = "some-branch"
        os.environ["MCP_COMMIT"] = "abc123def456"
        payload = build_baseline_payload([{"overall": 1.0}], "hybrid", 10)
        self.assertEqual("some-branch", payload["mcp_ref"])
        self.assertNotIn("mcp_commit", payload)

    def test_an_unstated_baseline_round_trips_as_empty_not_as_main(self):
        """The fallback lives at the comparison, not at the read.

        Baking `main` into the loader would make "stated main" and "said
        nothing" indistinguishable, and a later change to the reading could then
        never tell the two apart.
        """
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "b.json"
            p.write_text(json.dumps({"search_mode": "hybrid", "scores": {"a": 1.0}}))
            self.assertEqual("", load_baseline(p).mcp_ref)


class TestClientMismatchIsInconclusiveNeverRegression(unittest.TestCase):
    """Read off the source: the verdict path needs a dataset, a live Mesh and an
    embedder. What must be pinned exactly is that the branch returns the
    inconclusive exit with its own reason kind, and cannot return a regression.
    """

    def setUp(self):
        self.src = (Path(__file__).resolve().parent / "run_ci.py").read_text()
        self.block = self.src.split("# ── Client gate:")[1].split("# ── Mode gate:")[0]

    def test_the_block_exists_and_is_the_gate(self):
        # Positive control for the two assertions below, which are string
        # searches over a slice that would be empty if the marker moved.
        self.assertIn("run_mcp_ref", self.block)

    def test_mismatch_returns_inconclusive_with_its_own_reason(self):
        self.assertIn("REASON_MCP_REF_MISMATCH", self.block)
        self.assertIn("return EXIT_INCONCLUSIVE", self.block)

    def test_mismatch_can_never_be_a_regression(self):
        self.assertNotIn(
            "EXIT_REGRESSION", self.block,
            "measuring a different client is not evidence that this PR made "
            "memory worse; going red on it blocks PRs nobody can unblock",
        )

    def test_an_unstated_baseline_is_read_as_the_historical_client(self):
        self.assertIn(
            "baseline.mcp_ref or MCP_REF_UNSTATED_AS", self.block,
            "every baseline captured before this field existed WAS measured on "
            "the client's default branch, so unstated has a known correct "
            "reading; treating it as 'matches this run' would be the silent "
            "mis-comparison the axis exists to prevent",
        )

    def test_the_reason_kind_is_distinct_from_its_neighbours(self):
        """A shared kind would let one alert suppress the other.

        The workflow dedups out-of-band alerts on reason kind. `arm-mismatch`
        means "you are holding the wrong baseline file"; this means "you
        measured a different client on purpose". Different owner, different fix.
        """
        self.assertNotEqual(run_ci.REASON_MCP_REF_MISMATCH, run_ci.REASON_ARM_MISMATCH)
        self.assertNotEqual(
            run_ci.REASON_MCP_REF_MISMATCH, run_ci.REASON_AGE_MODE_MISMATCH
        )


class TestABranchClientCannotBecomeTheFloor(unittest.TestCase):
    def setUp(self):
        self._saved = os.environ.get("MCP_REF")

    def tearDown(self):
        if self._saved is None:
            os.environ.pop("MCP_REF", None)
        else:
            os.environ["MCP_REF"] = self._saved

    def _blockers(self, **kw):
        return capture_blockers({}, "hybrid", True, **kw)

    def test_capture_on_a_branch_client_is_refused(self):
        os.environ["MCP_REF"] = "some-branch"
        joined = " ".join(self._blockers())
        self.assertIn("some-branch", joined)

    def test_capture_on_the_default_client_is_allowed(self):
        """The discriminating control.

        Without it the refusal above passes on a `capture_blockers` that refuses
        everything — which would leave the required arm with no way to re-snap
        at all, a worse outcome than the bug.
        """
        os.environ["MCP_REF"] = MCP_REF_UNSTATED_AS
        self.assertEqual([], self._blockers())
        os.environ.pop("MCP_REF", None)
        self.assertEqual([], self._blockers())

    def test_the_advisory_arm_is_not_gated_on_it(self):
        """Same asymmetry the mode/sample blockers already have: the advisory
        baseline blocks no merge, and refusing there trades a labelled baseline
        for none at all."""
        os.environ["MCP_REF"] = "some-branch"
        self.assertEqual([], capture_blockers({}, "hybrid", False))


if __name__ == "__main__":
    unittest.main()
