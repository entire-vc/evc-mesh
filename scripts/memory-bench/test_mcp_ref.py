"""#faf92388 — which mesh-mcp CLIENT the bench measured.

All three arms used to check `entire-vc/evc-mesh-mcp` out with no `ref:`, so every
arm always built the default branch and no client pull request was measurable by
this harness at all. The failure was silent in the worst direction: runs looked
complete, the gate ruled, and the thing it could not vary was a binary that
overrides the caller's recall parameters from a query profile.

These tests pin four separate things, because fixing only the first would leave
the hole reachable:
  1. the workflow structurally cannot check that repo out unpinned again,
  2. a run states which client it measured, in both artifacts,
  3. a run on a different client is INCONCLUSIVE, never REGRESSION, and cannot
     be written as a baseline,
  4. and that INCONCLUSIVE does not page an incident — the feature's intended
     use is not an outage.
"""

import json
import os
import re
import tempfile
import unittest
from pathlib import Path

import run_ci
import test_gate_blindness as blind
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


class TestTheBinaryAnswersForItself(unittest.TestCase):
    """AC4 — the run must show which client is in the BINARY, not which one was
    fetched.

    `MCP_REF`/`MCP_COMMIT` describe the checkout. Everything between that and the
    compiler is unproven by them: a reordered step, a stale `mesh-mcp-bin` in the
    workspace, a `cd` into the wrong tree — and the bench measures a client nobody
    selected while every log line still names the right ref. `go build` stamps the
    source commit into the artifact, so the artifact can be asked directly.
    """

    def setUp(self):
        self.src = WORKFLOW.read_text()
        self.builds = [
            s for s in re.split(r"\n      - name:", self.src)
            if "go build -o ../mesh-mcp-bin" in s
        ]

    def test_the_detector_finds_every_build(self):
        # Positive control — one build step per arm.
        self.assertEqual(3, len(self.builds))

    def test_every_build_reads_the_revision_out_of_the_binary(self):
        for step in self.builds:
            self.assertIn(
                "vcs.revision", step,
                "this build never asks the binary which source it came from; "
                "the checkout's own report is not evidence about the artifact.\n"
                + step[:300],
            )

    def test_every_build_compares_it_against_the_checkout(self):
        """Printing the revision is observability; comparing it is the control."""
        for step in self.builds:
            self.assertIn('"$built" != "$MCP_COMMIT"', step)
            self.assertIn("exit 1", step)

    def test_the_assertion_cannot_redden_an_ordinary_run(self):
        """Scoped to a dispatched ref, and that scoping is load-bearing.

        On pull_request / merge_group / push / schedule the default branch IS the
        right client, so a broken pin costs nothing there — while a hard failure
        would paint a REQUIRED check red for an author who cannot clear it (#342).
        Without the `-n "$MCP_REF_INPUT"` guard, an unstamped build would fail
        every PR in the repo.
        """
        for step in self.builds:
            self.assertIn('[ -n "$MCP_REF_INPUT" ]', step)
            self.assertIn("MCP_REF_INPUT: ${{ inputs.mcp_ref }}", step)

    def test_unstamped_is_a_mismatch_not_a_pass(self):
        """An unverifiable provenance claim must not read as a verified one.

        `built` empty (Go stopped stamping) is `!= $MCP_COMMIT`, so the dispatch
        path fails. Pinned because the tempting `[ -n "$built" ] &&` guard would
        turn the control off exactly when it stopped working.
        """
        for step in self.builds:
            self.assertNotIn('-n "$built"', step)
            self.assertNotIn("-z \"$built\"", step)


class TestADeliberateClientABDoesNotPageAnIncident(unittest.TestCase):
    """The first real use of `mcp_ref` opened two blindness episodes.

    Run 31321344020 dispatched the branch with `mcp_ref=garfield/…`, both arms
    correctly returned INCONCLUSIVE naming `mcp-ref-mismatch` — and both then
    raised an episode, #543 claiming the MERGE GATE was blind and #541 claiming
    nothing was watching prod. Neither was true: a dispatch gates no merge, and
    the PR / merge_group / push arms all build the default client regardless of
    what a dispatch chose. A feature whose intended use pages an incident trains
    people to close the page unread, which is how the real blindness — the kind
    these episodes exist for — stops being read too.

    The guard is therefore over the RESOLVED ref, not over `inputs.mcp_ref != ''`:
    `mcp_ref=main` is indistinguishable from empty and must keep both halves live.
    """

    # Arms whose verdict is a run_ci exit code — i.e. whose blindness is a
    # statement about a COMPARISON, which is what the client axis can invalidate.
    # The cancellation watchdog is deliberately not one of them: its subject is
    # "a prod arm was cancelled", which is true or false independently of which
    # client got built. Selected structurally so a fifth comparing arm is caught.
    @staticmethod
    def _comparison_arms():
        return {
            job_id: halves
            for job_id, halves in blind._blindness_calls().items()
            if re.search(r"steps\.\w+\.outputs\.rc == '2'", halves["alert"]["if"])
        }

    def test_the_selector_finds_the_three_known_arms(self):
        # Positive control: a selector narrowed to zero passes every assertion
        # below without checking anything.
        self.assertEqual(
            {"recall-gate-branch", "recall-gate", "memory-bench"},
            set(self._comparison_arms()),
        )

    def test_every_comparing_arm_guards_both_halves(self):
        for job_id, halves in sorted(self._comparison_arms().items()):
            for mode in ("alert", "resolve"):
                with self.subTest(job=job_id, mode=mode):
                    self.assertIn(
                        "env.MCP_REF", halves[mode]["if"],
                        f"{job_id!r}'s {mode} has no client-drill guard. On the "
                        f"alert that charges a false page for every deliberate "
                        f"client A/B; on the resolve it lets one close a real "
                        f"episode it was never allowed to open.\n"
                        f"  if: {halves[mode]['if']}",
                    )

    def test_the_guard_names_the_same_default_branch_the_gate_does(self):
        """Two copies of one branch name, in two languages, must not drift.

        `run_ci.MCP_REF_UNSTATED_AS` decides which runs are comparable; the YAML
        literal decides which are pageable. If evc-mesh-mcp's default branch is
        renamed and only one is updated, the two disagree silently — the gate
        calls every run a mismatch while the alert either pages on all of them
        or on none.
        """
        for job_id, halves in sorted(self._comparison_arms().items()):
            for mode in ("alert", "resolve"):
                literals = re.findall(
                    r"env\.MCP_REF == '([^']*)'", halves[mode]["if"]
                )
                with self.subTest(job=job_id, mode=mode):
                    self.assertEqual(
                        ["", MCP_REF_UNSTATED_AS], sorted(set(literals)),
                        f"{job_id!r}'s {mode} compares MCP_REF against "
                        f"{literals!r}; expected '' (fail-open when the client "
                        f"was never resolved) and {MCP_REF_UNSTATED_AS!r} "
                        f"(run_ci.MCP_REF_UNSTATED_AS).",
                    )

    def _ctx(self, mode, **over):
        base = dict(blind._BLIND if mode == "alert" else blind._MEASURING)
        base["github.event_name"] = "workflow_dispatch"
        base["inputs.expect_commit"] = ""
        base.update(over)
        return base

    def test_a_client_ab_dispatch_neither_pages_nor_clears(self):
        for job_id, halves in sorted(self._comparison_arms().items()):
            for mode in ("alert", "resolve"):
                with self.subTest(job=job_id, mode=mode):
                    self.assertFalse(
                        blind.evaluate_if(
                            halves[mode]["if"],
                            context=self._ctx(mode, **{"env.MCP_REF": "some/branch"}),
                            success=True, cancelled=False,
                        ),
                        f"{job_id!r}'s {mode} still fires on a dispatch that "
                        f"deliberately built another client — the shape that "
                        f"opened #541 and #543.",
                    )

    def test_the_same_dispatch_on_the_default_client_still_fires(self):
        """The discriminating control, in both directions.

        Without it the assertion above is satisfied by a guard that switches the
        arms off on every dispatch — which would lose real blindness found by an
        operator run, the one trigger a human reaches for when something looks
        wrong. `''` covers the arm whose resolve step was skipped.
        """
        for job_id, halves in sorted(self._comparison_arms().items()):
            for mode in ("alert", "resolve"):
                for ref in (MCP_REF_UNSTATED_AS, ""):
                    with self.subTest(job=job_id, mode=mode, ref=ref or "(unset)"):
                        self.assertTrue(
                            blind.evaluate_if(
                                halves[mode]["if"],
                                context=self._ctx(mode, **{"env.MCP_REF": ref}),
                                success=True, cancelled=False,
                            ),
                            f"{job_id!r}'s {mode} no longer fires on a dispatch "
                            f"measuring the default client (MCP_REF={ref!r}), so "
                            f"the guard silences real blindness too.",
                        )

    def test_a_non_dispatch_run_is_untouched_by_the_guard(self):
        """push / schedule resolve the client off the checkout, so they read
        `main` — but if the default branch is ever renamed they would read the
        new name, and suppressing THEM would be the silent direction. The event
        disjunct is what keeps those runs pageable; this pins it."""
        for job_id, halves in sorted(self._comparison_arms().items()):
            for mode in ("alert", "resolve"):
                with self.subTest(job=job_id, mode=mode):
                    self.assertTrue(
                        blind.evaluate_if(
                            halves[mode]["if"],
                            context=self._ctx(
                                mode,
                                **{
                                    "github.event_name": "push",
                                    "env.MCP_REF": "renamed-default",
                                },
                            ),
                            success=True, cancelled=False,
                        ),
                        f"{job_id!r}'s {mode} was silenced on a push by the "
                        f"client guard; only a dispatch chooses a client.",
                    )


if __name__ == "__main__":
    unittest.main()
