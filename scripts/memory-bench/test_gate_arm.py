#!/usr/bin/env python3
"""Self-check for the ARM scoping of the recall gate (ADR-0003).

    python scripts/memory-bench/test_gate_arm.py

Background — the defect these pin
---------------------------------
The recall gate was required on every PR and took its target from
`secrets.MESH_API_URL`: the deployed production server. No job built `cmd/api`
from the branch. So the required check measured the binary that was ALREADY
RUNNING and reported a confident green about a program the PR did not contain.
PRs #388/#389/#391 (memory chunking) each passed that way, and the regression
they carried — new memories' vectors going to `memory_chunks` while nothing read
them back, so `memories.embedding` was NULL and those memories left the dense
arm — was invisible to it by construction.

The fix splits the gate into two arms with two targets. Which makes the *new*
hazard the one tested here: two baseline files that look interchangeable and are
not. `baseline_retrieval.json` (prod: prod's embedder, prod's accumulated
corpus) and `baseline_retrieval_branch.json` (branch: bge-small-en-v1.5, empty
DB) hold the same keys with the same value ranges. Comparing a branch run to the
prod baseline is arithmetically fine and factually meaningless — and it fails in
whichever direction the numbers happen to fall, so it can equally block a good
PR or pass a bad one.

The invariant: a baseline may only judge a run from its own arm, and every other
case is INCONCLUSIVE — never REGRESSION. Exit 1 blocks a merge, and no PR author
can fix which file CI picked up.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import run_ci  # noqa: E402
from run_ci import (  # noqa: E402
    ARM_BRANCH,
    ARM_PROD,
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    EXIT_REGRESSION,
    MODE_HYBRID,
    REASON_ARM_MISMATCH,
    build_baseline_payload,
    load_baseline,
)


class _Harness(unittest.TestCase):
    """Drive the real `main()` end-to-end, as test_gate_modes.py does.

    Same reasoning: the thing being pinned is not a pure helper, it is which
    FILE the caller resolved and whether the verdict path honoured its label. A
    unit test of the comparison helper stays green with the bug fully present.
    """

    CATEGORIES = ["knowledge-update", "multi-session"]
    QUESTIONS_PER_CATEGORY = 4

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        dataset = [
            {"question_id": f"q{i}", "question_type": cat}
            for cat in self.CATEGORIES
            for i in range(4)
        ]
        data_file = self.tmp / "data.json"
        data_file.write_text(json.dumps(dataset))

        # Every sink main() can write, redirected into tmp. Missing one here
        # means a stub baseline lands in the repo and becomes a real merge
        # threshold — see the same list in test_gate_modes.py.
        for p, attr in (
            (self.tmp / "baseline.json", "BASELINE_FILE"),
            (self.tmp / "baseline_retrieval.json", "RETRIEVAL_BASELINE_FILE"),
            (self.tmp / "baseline_retrieval_branch.json", "BRANCH_RETRIEVAL_BASELINE_FILE"),
            (self.tmp / "results", "RESULTS_DIR"),
            (self.tmp / "results" / "recall_gate.json", "RETRIEVAL_RESULTS_FILE"),
            (self.tmp / "results" / "longmemeval.json", "E2E_RESULTS_FILE"),
        ):
            patcher = mock.patch.object(run_ci, attr, p)
            patcher.start()
            self.addCleanup(patcher.stop)
            setattr(self, attr, p)

        for patcher in (
            mock.patch.object(run_ci, "DATA_FILE", data_file),
            mock.patch.dict(
                os.environ,
                {"MESH_API_URL": "http://mesh.test", "MESH_AGENT_KEY": "k"},
            ),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def _stub_answers(self, correct_by_category: dict[str, int]):
        seen: dict[str, int] = {}

        def fake_run_single(entry, **kwargs):
            cat = entry["question_type"]
            n = seen.get(cat, 0) % self.QUESTIONS_PER_CATEGORY
            seen[cat] = seen.get(cat, 0) + 1
            return {
                "question_id": entry["question_id"],
                "question_type": cat,
                "correct": n < correct_by_category.get(cat, 0),
                "search_mode": MODE_HYBRID,
                "gold_rank": 1,
                "n_returned": 10,
                "n_haystack": 40,
            }

        p = mock.patch.object(run_ci, "run_single", side_effect=fake_run_single)
        p.start()
        self.addCleanup(p.stop)

    def _run(self, *argv: str) -> tuple[int, str]:
        buf = io.StringIO()
        with mock.patch.object(sys, "argv", ["run_ci.py", *argv]), \
                contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
            rc = run_ci.main()
        return rc, buf.getvalue()

    def _baseline(self, scores: dict[str, float], arm: str | None) -> dict:
        payload = build_baseline_payload(
            [scores, scores, scores], MODE_HYBRID, 10,
            {c: (4, 4) for c in scores}, arm or ARM_PROD,
        )
        if arm is None:
            # A pre-ADR-0003 file: written before the field existed.
            payload.pop("arm")
        return payload


class TestTheArmSelectsTheBaselineFile(_Harness):
    def test_prod_arm_reads_the_prod_baseline(self):
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_PROD)
        self.assertEqual(rc, EXIT_OK, out)

    def test_branch_arm_reads_the_branch_baseline(self):
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_BRANCH)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_OK, out)

    def test_branch_arm_ignores_a_present_prod_baseline(self):
        """The failure this rules out: the branch arm silently falling back to
        the prod file because its own is absent. That is the ORIGINAL defect
        wearing new clothes — a green certifying a measurement of prod."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn("no-baseline", out)

    def test_default_arm_is_prod(self):
        """Every existing caller (the prod canary, a local run, the docs' own
        command) omits --arm. If the default moved, they would all read a file
        that does not exist and report no-baseline."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only")
        self.assertEqual(rc, EXIT_OK, out)


class TestACrossArmComparisonIsRefused(_Harness):
    def test_prod_baseline_in_the_branch_path_is_inconclusive_not_regression(self):
        """Someone copies baseline_retrieval.json over the branch one. The scores
        differ (different embedder), so an unguarded gate returns a verdict —
        and which verdict is pure luck. Must refuse instead."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)

    def test_it_refuses_even_when_the_scores_would_have_PASSED(self):
        """The direction that hides. A mislabelled baseline whose numbers happen
        to clear tolerance produces a GREEN required check over a comparison
        between two different systems — indistinguishable from a real pass, and
        the exact shape of #b052cdda. A guard that only fires on a red delta
        would be no guard at all."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 0.0, "multi-session": 0.0, "overall": 0.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})  # 1.000, way over
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertNotEqual(rc, EXIT_OK, "a cross-arm comparison must never be green")
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)

    def test_a_cross_arm_mismatch_never_blocks_a_merge(self):
        """Scores far below the mislabelled baseline: the tempting behaviour is
        EXIT_REGRESSION. But no PR author can fix which file CI resolved, and a
        red nobody can clear gets the check bypassed — which is how a safety net
        dies permanently rather than for one PR."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_PROD)))
        self._stub_answers({"knowledge-update": 0, "multi-session": 0})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertNotEqual(rc, EXIT_REGRESSION, "must not block on a file-choice fault")
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)

    def test_branch_baseline_in_the_prod_path_is_refused_too(self):
        """Symmetric. Tested separately because the guard has two halves and
        only one of them is exercised by the branch-side cases: an unstated arm
        is TOLERATED in prod (every pre-ADR-0003 file has none) and REFUSED in
        branch, so a one-sided test would pass with the prod half deleted."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, ARM_BRANCH)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_PROD)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)


class TestUnstatedArmBackCompat(_Harness):
    def test_a_pre_adr_baseline_still_judges_the_prod_arm(self):
        """`baseline_retrieval.json` on main has no `arm` field. If an unstated
        arm were refused, this change would wedge the prod canary on the commit
        that lands it — a self-inflicted total blindness."""
        payload = self._baseline(
            {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, None)
        self.assertNotIn("arm", payload)
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(payload))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_PROD)
        self.assertEqual(rc, EXIT_OK, out)

    def test_an_unstated_arm_is_refused_in_the_BRANCH_arm(self):
        """No legitimate unlabelled branch baseline can exist — the file is
        created by this change. So unstated there means "the prod file was
        copied in", which is exactly the case that must not produce a verdict."""
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(json.dumps(self._baseline(
            {"knowledge-update": 1.0, "multi-session": 1.0, "overall": 1.0}, None)))
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH)
        self.assertEqual(rc, EXIT_INCONCLUSIVE, out)
        self.assertIn(REASON_ARM_MISMATCH, out)


class TestTheArmIsRecordedOnCapture(_Harness):
    def test_capture_writes_the_arm_into_the_file(self):
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        rc, out = self._run("--retrieval-only", "--arm", ARM_BRANCH,
                            "--update-baseline", "--repeat", "2")
        self.assertEqual(rc, EXIT_OK, out)
        written = json.loads(self.BRANCH_RETRIEVAL_BASELINE_FILE.read_text())
        self.assertEqual(written["arm"], ARM_BRANCH)
        self.assertEqual(load_baseline(self.BRANCH_RETRIEVAL_BASELINE_FILE).arm, ARM_BRANCH)

    def test_capture_in_one_arm_does_not_touch_the_other_arms_file(self):
        """A capture that wrote both would silently overwrite the prod baseline —
        the threshold behind the canary — with numbers from a different embedder."""
        self.RETRIEVAL_BASELINE_FILE.write_text(json.dumps(
            self._baseline({"knowledge-update": 0.5, "multi-session": 0.5, "overall": 0.5}, ARM_PROD)))
        before = self.RETRIEVAL_BASELINE_FILE.read_text()
        self._stub_answers({"knowledge-update": 4, "multi-session": 4})
        self._run("--retrieval-only", "--arm", ARM_BRANCH, "--update-baseline", "--repeat", "2")
        self.assertEqual(self.RETRIEVAL_BASELINE_FILE.read_text(), before)

    def test_the_committed_branch_baseline_states_arm_branch(self):
        """Guards the real file in the repo, not a fixture. A branch baseline
        committed without its label is accepted by nothing and INCONCLUSIVEs the
        required check on every PR."""
        real = Path(run_ci.__file__).resolve().parent / "baseline_retrieval_branch.json"
        if not real.exists():
            self.skipTest("branch baseline not captured yet")
        self.assertEqual(json.loads(real.read_text()).get("arm"), ARM_BRANCH)


# ---------------------------------------------------------------------------
# Which JOBS a `baseline_arm` dispatch starts.
#
# The defect this pins, found 2026-07-28 while capturing the branch baseline for
# the first time: the paid end-to-end job excluded itself with
# `inputs.baseline_arm != 'retrieval'`. That was correct while the options were
# {end-to-end, retrieval, both}. #394 added `retrieval-branch`, and a negation
# cannot know about an option added after it — so re-snapping the REQUIRED
# arm's baseline silently also started a paid LLM-judge capture, which is the
# precise spend the comment above that guard says it exists to prevent.
#
# `grep -rn baseline_arm scripts/memory-bench/*.py` returned nothing before this
# class existed: the routing was the one part of the arm split that no
# self-check looked at, which is why a stale negation survived the split.
#
# These assert the INVARIANT (which arms start which job), not the current
# spelling — an equivalent rewrite of the expressions keeps them green, and any
# fifth arm added without revisiting the routing turns them red.
# ---------------------------------------------------------------------------

WORKFLOW = (
    Path(run_ci.__file__).resolve().parents[2]
    / ".github" / "workflows" / "memory-bench.yml"
)

_ARMS = ("end-to-end", "retrieval", "retrieval-branch", "both")


def _extract_if(text: str, job: str) -> str:
    """Pull a job's `if:` expression out of the workflow, block scalar or inline.

    Text-scraped on purpose: these self-checks are stdlib-only (no PyYAML on the
    runner), and the job runs them before it does anything else.
    """
    lines = text.splitlines()
    start = next(
        (i for i, ln in enumerate(lines) if ln == f"  {job}:"),
        None,
    )
    if start is None:
        raise AssertionError(f"job {job!r} not found in {WORKFLOW}")
    for i in range(start + 1, len(lines)):
        ln = lines[i]
        if ln and not ln.startswith("    "):
            break  # left the job block without finding an `if:`
        # JOB-level only: exactly four spaces. Steps carry their own `if:` at
        # deeper indent, and picking one of those up would silently pin the
        # wrong expression — the first draft of this helper did exactly that
        # and reported `recall-gate-branch` as conditional on a step output.
        if not re.match(r"^    if:", ln):
            continue
        rest = ln.strip()[len("if:"):].strip()
        if rest not in (">-", ">", "|-", "|"):
            return rest  # inline form
        body = []
        for cont in lines[i + 1:]:
            if cont.strip() and not cont.startswith("      "):
                break
            body.append(cont.strip())
        return " ".join(x for x in body if x)
    raise AssertionError(f"job {job!r} has no `if:` — it runs unconditionally")


def _extract_capture_expr(text: str, marker: str) -> str:
    """The `capture=${{ ... }}` expression from the step following `marker`."""
    idx = text.index(marker)
    frag = text[idx:]
    open_at = frag.index('echo "capture=${{') + len('echo "capture=${{')
    close_at = frag.index("}}", open_at)
    return frag[open_at:close_at].strip()


def _eval_raw(expr: str, ctx: dict):
    """Evaluate the restricted GitHub-expression subset used in this workflow.

    Returns the RAW value, not a bool. The `capture=` steps are written as the
    GitHub idiom for a ternary — `<cond> && 'true' || 'false'` — which yields
    the *string* `'true'` or `'false'`. Coercing that with `bool()` makes both
    outcomes truthy, so a capture pin built on a bool-returning evaluator
    passes with the condition fully inverted. (Caught here by the first draft of
    this class, which did exactly that and reported the paid arm as capturing
    for every value of `baseline_arm`.)

    Only `&& || ! ( ) == !=`, string literals, `github.event_name` and
    `inputs.*` appear here. GitHub's truthiness for an absent input is the empty
    string, which is falsy in Python too — so unset inputs need no special case.
    """
    py = expr.replace("!=", " __NE__ ")
    py = py.replace("&&", " and ").replace("||", " or ").replace("!", " not ")
    py = py.replace("__NE__", "!=")
    py = py.replace("github.event_name", "__event")
    py = re.sub(r"\binputs\.([A-Za-z_][A-Za-z0-9_]*)", r"__inputs.get('\1', '')", py)
    ns = {
        "__event": ctx["event"],
        "__inputs": ctx.get("inputs", {}),
        "__builtins__": {},
    }
    return eval(py, ns)  # noqa: S307 — fixed grammar, no external input


def _evaluate(expr: str, ctx: dict) -> bool:
    """A job `if:` — GitHub coerces this one to a boolean itself."""
    return bool(_eval_raw(expr, ctx))


def _captures(expr: str, ctx: dict) -> bool:
    """A `capture=` step output. Compared against the literal string GitHub
    writes into `$GITHUB_OUTPUT`, so an inverted ternary (`&& 'false' ||
    'true'`) is a failure here rather than an invisible pass."""
    value = _eval_raw(expr, ctx)
    assert value in ("true", "false"), f"capture expression yielded {value!r}"
    return value == "true"


def _dispatch(arm: str, update_baseline: bool = True, full_eval: bool = False) -> dict:
    return {
        "event": "workflow_dispatch",
        "inputs": {
            "baseline_arm": arm,
            "update_baseline": update_baseline,
            "full_eval": full_eval,
        },
    }


class WorkflowRouting(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = WORKFLOW.read_text()

    def _runs(self, job: str, ctx: dict) -> bool:
        return _evaluate(_extract_if(self.text, job), ctx)

    # -- the evaluator must be able to say False ---------------------------
    def test_the_evaluator_is_not_stuck_true(self):
        """Negative control for this class's own instrument. Every assertion
        below is `assertFalse` on a real expression or `assertTrue` on one; an
        evaluator that returned a constant would satisfy half of them silently,
        and a routing pin that cannot go red is decoration."""
        self.assertTrue(_evaluate("github.event_name == 'push'", {"event": "push"}))
        self.assertFalse(_evaluate("github.event_name == 'push'", {"event": "schedule"}))
        self.assertFalse(_evaluate(
            "inputs.update_baseline && inputs.baseline_arm == 'both'",
            _dispatch("retrieval")))
        # `!=` must survive the `!` → `not` rewrite.
        self.assertTrue(_evaluate("inputs.baseline_arm != 'retrieval'", _dispatch("both")))
        self.assertFalse(_evaluate("inputs.baseline_arm != 'retrieval'", _dispatch("retrieval")))

    def test_the_capture_reader_survives_an_inverted_ternary(self):
        """The mutation that beat the first draft of this class. Both branches
        of `<cond> && 'true' || 'false'` are non-empty strings, so `bool()` says
        True either way — a capture pin built that way is green with the
        condition reversed. `_captures` compares the emitted string instead."""
        ok = "inputs.baseline_arm == 'end-to-end' && 'true' || 'false'"
        self.assertTrue(_captures(ok, _dispatch("end-to-end")))
        self.assertFalse(_captures(ok, _dispatch("retrieval-branch")))
        inverted = "inputs.baseline_arm == 'end-to-end' && 'false' || 'true'"
        self.assertFalse(_captures(inverted, _dispatch("end-to-end")))
        self.assertTrue(_captures(inverted, _dispatch("retrieval-branch")))
        # The bool() route cannot tell those two apart — that is the whole point.
        self.assertTrue(bool(_eval_raw(ok, _dispatch("retrieval-branch"))))

    def test_the_required_arm_runs_unconditionally(self):
        """`recall-gate-branch` carries the required context. A required check
        that skips on some event never reports there, and a never-reported
        required context blocks that PR for ever (evc-mesh#320)."""
        with self.assertRaises(AssertionError):
            _extract_if(self.text, "recall-gate-branch")

    # -- the paid job ------------------------------------------------------
    def test_only_the_paid_arms_start_the_paid_job(self):
        """THE regression. `retrieval-branch` must not spend an end-to-end
        capture; that is money, and nobody asked for it."""
        for arm in ("end-to-end", "both"):
            with self.subTest(arm=arm):
                self.assertTrue(self._runs("memory-bench", _dispatch(arm)))
        for arm in ("retrieval", "retrieval-branch"):
            with self.subTest(arm=arm):
                self.assertFalse(self._runs("memory-bench", _dispatch(arm)))

    def test_the_paid_job_does_not_capture_for_a_retrieval_arm(self):
        """Starting the job and capturing in it are two expressions. Pinning
        only the `if:` leaves the second free to drift into writing an
        end-to-end baseline from a retrieval dispatch."""
        expr = _extract_capture_expr(self.text, "  memory-bench:")
        for arm in ("end-to-end", "both"):
            self.assertTrue(_captures(expr, _dispatch(arm)), arm)
        for arm in ("retrieval", "retrieval-branch"):
            self.assertFalse(_captures(expr, _dispatch(arm)), arm)

    # -- the prod canary ---------------------------------------------------
    def test_a_branch_capture_does_not_start_the_prod_canary(self):
        """Not about money — about the shared `memory-bench-prod` group. The
        branch arm builds its own server, so the canary contributes nothing to
        a branch capture while still taking a slot from whoever is legitimately
        measuring prod. Both stowaway jobs also sat in that one group, which is
        two jobs of ONE run in one group — the shape that kills one at 0s."""
        self.assertFalse(self._runs("recall-gate", _dispatch("retrieval-branch")))
        for arm in ("retrieval", "both", "end-to-end"):
            with self.subTest(arm=arm):
                self.assertTrue(self._runs("recall-gate", _dispatch(arm)))

    def test_the_prod_canary_still_runs_on_the_unattended_triggers(self):
        """The exclusion is scoped to a dispatch. Narrowing it far enough to
        silence push/schedule would retire the canary by accident."""
        for event in ("push", "schedule"):
            with self.subTest(event=event):
                self.assertTrue(self._runs("recall-gate", {"event": event, "inputs": {}}))
        self.assertFalse(self._runs("recall-gate", {"event": "pull_request", "inputs": {}}))

    def test_a_plain_dispatch_still_reaches_both_prod_arms(self):
        """`update_baseline` unset — the ordinary "run the bench now" dispatch."""
        ctx = _dispatch("end-to-end", update_baseline=False, full_eval=True)
        self.assertTrue(self._runs("recall-gate", ctx))
        self.assertTrue(self._runs("memory-bench", ctx))

    # -- the branch arm's own capture flag ---------------------------------
    def test_only_a_branch_dispatch_captures_the_branch_baseline(self):
        expr = _extract_capture_expr(self.text, "  recall-gate-branch:")
        self.assertTrue(_captures(expr, _dispatch("retrieval-branch")))
        for arm in ("end-to-end", "retrieval", "both"):
            self.assertFalse(_captures(expr, _dispatch(arm)), arm)
        self.assertFalse(_captures(expr, {"event": "pull_request", "inputs": {}}))

    def test_every_declared_arm_is_routed_somewhere(self):
        """A fifth option added to the `choice` list without revisiting the
        routing lands here rather than in a surprise bill. Each arm must start
        the job that writes the file it names."""
        declared = re.search(
            r"baseline_arm:.*?options:\n((?:\s+- \S+\n)+)", self.text, re.S)
        self.assertIsNotNone(declared, "could not read the baseline_arm options")
        options = re.findall(r"- (\S+)", declared.group(1))
        self.assertEqual(sorted(options), sorted(_ARMS),
                         "a baseline_arm option was added or removed without "
                         "updating the routing pins in this file")
        for arm in options:
            ctx = _dispatch(arm)
            started = {
                "memory-bench": self._runs("memory-bench", ctx),
                "recall-gate": self._runs("recall-gate", ctx),
                "recall-gate-branch": True,  # unconditional, asserted above
            }
            self.assertTrue(any(started.values()), f"{arm} starts no job at all")


if __name__ == "__main__":
    unittest.main(verbosity=2)
