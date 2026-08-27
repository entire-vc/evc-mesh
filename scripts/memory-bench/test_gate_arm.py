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
import subprocess
import tempfile
import textwrap
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

    def test_the_REAL_committed_prod_baseline_is_refused_in_the_branch_arm(self):
        """The fixtures above state `arm: prod`, so they exercise the cross-arm
        half of the guard. The file actually sitting in this repo states NO arm
        at all — so the real "someone copies baseline_retrieval.json into the
        branch path" accident travels the *unstated* half instead, and a pin
        built only from `ARM_PROD` fixtures would not cover the real file.

        Reads the committed file rather than a fixture: if a future capture
        starts labelling the prod baseline `arm: prod`, this keeps passing via
        the other half, and if the guard is ever narrowed to one half it fails
        here whichever half survives."""
        real = Path(run_ci.__file__).resolve().parent / "baseline_retrieval.json"
        self.BRANCH_RETRIEVAL_BASELINE_FILE.write_text(real.read_text())
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
    """The `CAPTURE: ${{ ... }}` expression from the step following `marker`.

    The expression lives in the step's `env:`, not on the `echo` line: a
    `${{ }}` interpolated straight into a `run:` body is the Actions
    script-injection shape (semgrep `run-shell-injection`, Mesh #5fe7fb4b), so
    every one of them in this workflow was moved into the environment. This
    reader follows it there.

    It deliberately does NOT fall back to the old `echo "capture=${{ ... }}"`
    form. A reader that accepts both would keep passing if someone reverted the
    fix, which is exactly the drift these tests exist to catch — an unfindable
    expression must raise, not silently match a different one.
    """
    idx = text.index(marker)
    frag = text[idx:]
    open_at = frag.index("CAPTURE: ${{") + len("CAPTURE: ${{")
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

    Only `&& || ! ( ) == !=`, string literals, `github.event_name`,
    `inputs.*` and the status function `cancelled()` appear here. GitHub's
    truthiness for an absent input is the empty string, which is falsy in
    Python too — so unset inputs need no special case.

    `cancelled()` is bound from the context and defaults to False, i.e. "this
    run was not cancelled". That is deliberately the *routing* reading: these
    tests ask which arms a dispatch starts, and cancellation is not an arm. An
    unbound name would be the safer default in general — and was: before this
    binding existed the merge of #3ce651a0's `!cancelled() &&` prefix made
    every routing test raise `NameError` rather than quietly report the arms as
    correct. Keep that property when extending this: an expression element this
    evaluator does not model must raise, never evaluate to something plausible.
    """
    py = expr.replace("!=", " __NE__ ")
    py = py.replace("&&", " and ").replace("||", " or ").replace("!", " not ")
    py = py.replace("__NE__", "!=")
    py = py.replace("github.event_name", "__event")
    py = re.sub(r"\binputs\.([A-Za-z_][A-Za-z0-9_]*)", r"__inputs.get('\1', '')", py)
    ns = {
        "__event": ctx["event"],
        "__inputs": ctx.get("inputs", {}),
        "cancelled": lambda: bool(ctx.get("cancelled", False)),
        "__builtins__": {},
    }
    return eval(py, ns)  # noqa: S307 — fixed grammar, no external input -- nosemgrep: python.lang.security.audit.eval-detected.eval-detected (ns has empty __builtins__, py is derived from fixed GitHub Actions expression grammar, not attacker input)


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


class TheRedProofIsWiredIn(unittest.TestCase):
    """`prove_gate_can_go_red.py` is not discovered by test_gate_blindness's
    self-check finder — it matches neither `test_*.py` nor `--selftest`, because
    it takes a baseline path rather than testing itself. So the invocation has
    no pin from that direction, and an undiscovered self-check that nothing
    requires is the exact shape that left test_gate_dense_arm.py unwired while
    the README credited it as a guard.

    Pinned here on the invocation and its arguments, not on the filename
    appearing somewhere in the job: naming the script in a comment, or running
    it against the PROD baseline, would both satisfy a substring check while
    proving nothing about the required arm.
    """

    @classmethod
    def setUpClass(cls):
        cls.text = WORKFLOW.read_text()

    def _required_job_block(self) -> str:
        blocks = re.split(r"\n  (?=\S+:\n)", self.text)
        return next(b for b in blocks
                    if re.search(r"^    name: Memory recall gate\s*$", b, re.M))

    def _step(self, name: str) -> str:
        block = self._required_job_block()
        step = re.search(
            rf"- name: {re.escape(name)}\n(.*?)(?=\n      - name:|\Z)",
            block, re.S)
        self.assertIsNotNone(step, f"step {name!r} is not in the required job")
        return step.group(1)

    @staticmethod
    def _condition(step_text: str) -> str | None:
        """Extract a step's `if:`, INCLUDING the folded (`>-`) spelling.

        The inline-only reader this replaces captured the literal `>-` and then
        asserted substrings against it, so every assertion about a folded
        condition passed or failed on the fold marker rather than on the
        condition. It did not fail safe: `assertIn('steps.mode…', '>-')` fails
        loudly, but a NEGATIVE assertion (`assertNotIn`) against `'>-'` would
        have passed for any condition whatsoever. Both spellings are live in
        this file, so reading only one is reading half the workflow.
        """
        m = re.search(r"^(\s*)if:[ \t]*(.*)$", step_text, re.M)
        if not m:
            return None
        indent, first = m.group(1), m.group(2).strip()
        if first not in (">-", ">", "|-", "|"):
            return first
        # Folded: take the more-indented lines that follow.
        rest, started = [], False
        for line in step_text[m.end():].splitlines():
            if not line.strip():
                if started:
                    break
                continue
            if len(line) - len(line.lstrip()) <= len(indent):
                break
            started = True
            rest.append(line.strip())
        return " ".join(rest)

    def test_the_required_job_proves_the_gate_can_go_red(self):
        block = self._required_job_block()
        # `(?:[^\n]*\\\n)*` walks any number of backslash continuations before
        # the final line. Without it the match stops at the trailing ` \` and
        # the argument assertions below inspect a line that has no arguments on
        # it yet — which is how the first draft of this test failed: it reported
        # the wired-in invocation as missing its own baseline path.
        call = re.search(
            r"python scripts/memory-bench/prove_gate_can_go_red\.py"
            r"(?:[^\n]*\\\n)*[^\n]*", block)
        self.assertIsNotNone(
            call,
            "the required job never runs prove_gate_can_go_red.py, so nothing "
            "checks that the gate is still CAPABLE of a REGRESSION verdict. A "
            "widened tolerance or a baseline captured from a degraded run both "
            "leave a permanently-green required check — the blind state of "
            "#f70347b5 without the no-baseline line that made it noticeable.",
        )
        invocation = call.group(0)
        self.assertIn(
            "baseline_retrieval_branch.json", invocation,
            "the red-proof must run against the BRANCH baseline — the file this "
            "required arm actually gates on.",
        )
        # Asserted POSITIONALLY — the token after the baseline path — because the
        # previous spelling `\bbranch\b\s*$` was anchored to end-of-string and
        # broke the moment the invocation grew a `| tee`: over-fit to the text
        # rather than to the property, which is what a pin must never be.
        #
        # (I first justified this by claiming an unanchored `\bbranch\b` would
        # false-match `baseline_retrieval_branch.json`. It does not: `_branch`
        # puts a word character before `b`, so there is no `\b` there. Recorded
        # because the wrong reason was checkable in one line and would otherwise
        # have been read back as the rationale for this shape.)
        cmd = invocation.replace("\\\n", " ").split("|")[0]      # drop `| tee …`
        argv = [t for t in cmd.split() if not t.startswith("2>&1")]
        self.assertIn("scripts/memory-bench/baseline_retrieval_branch.json", argv)
        i = argv.index("scripts/memory-bench/baseline_retrieval_branch.json")
        self.assertEqual(
            argv[i + 1:i + 2], ["branch"],
            "the red-proof must be told to expect arm 'branch' as the argument "
            "AFTER the baseline path; without the expected-arm argument it would "
            "accept a prod baseline, which the real gate refuses as arm-mismatch. "
            f"argv={argv!r}",
        )

    def test_the_red_proof_is_skipped_on_a_capture(self):
        """The bootstrap deadlock, pinned.

        On a capture the baseline file is about to be rewritten, and on the
        FIRST capture — the dispatch this card exists to make possible — it does
        not exist at all. prove_gate_can_go_red.py exits 2 on a missing file by
        design, so an ungated step fails the run before the re-snap can produce
        the very baseline it wanted to read. The gate would then be permanently
        un-bootstrappable: `no-baseline` for ever, which is this card verbatim.

        Asserted on the condition, not on the presence of any `if:` — a step
        gated on something unrelated would satisfy a truthiness check while
        leaving the deadlock in place.
        """
        cond = self._condition(self._step("The gate must still be able to go RED"))
        self.assertIsNotNone(
            cond,
            "the red-proof step has no `if:`, so it runs on captures too — "
            "including the bootstrap capture, where there is no baseline to "
            "read and the step's own exit 2 kills the run that would have "
            "created it.",
        )
        self.assertIn(
            "steps.mode.outputs.capture", cond,
            "the red-proof must be gated on the workflow's capture-vs-judge "
            f"source of truth; found {cond!r}",
        )
        self.assertIn(
            "!=", cond,
            "the gating must EXCLUDE captures (capture != 'true'); gated the "
            "other way the proof runs only on the runs that cannot satisfy it.",
        )

    def test_the_red_proof_does_not_block_a_pr_that_touches_no_memory_path(self):
        """#342, applied to the step this card added.

        The first draft of the red-proof sat before the scope gate and exited
        non-zero on rc=2, so a docs-only PR would have gone red because a
        baseline its author had never heard of was missing. This file states the
        consequence twice — `cf. #342`, and the gate step's own "a red nobody can
        clear gets bypassed" — and a bypassed required check is total blindness
        restored by consent, i.e. this card's own defect with extra steps.

        Coverage is not what pays for that: on push/schedule/dispatch the scope
        step sets relevant=true unconditionally, so main still evaluates the
        red-proof on every run.
        """
        cond = self._condition(self._step("The gate must still be able to go RED"))
        self.assertIn(
            "steps.scope.outputs.relevant", cond,
            "the red-proof must be gated on memory-path relevance, or it fails "
            f"the required check for PR authors who cannot clear it; found {cond!r}",
        )

    # The exit matrix, as behaviour. `None` for the event means "any".
    #   (rc, event) -> the exit status the STEP must produce
    RED_PROOF_EXIT_MATRIX = [
        # rc=2 is unclearable by anyone on the change, so it must not block a
        # pre-merge gate. Both pre-merge events are listed; see the docstring.
        (2, "pull_request", 0),
        (2, "merge_group", 0),
        # ...but on main and on the nightly it must still be loud: there is no
        # author to protect there, and a silent rc=2 is the blind gate itself.
        (2, "push", 2),
        (2, "schedule", 2),
        # rc=1 means the decision logic lost its red-ability, which only a
        # memory-path change can do. Always the author's to fix, always blocks.
        (1, "pull_request", 1),
        (1, "merge_group", 1),
        (1, "push", 1),
        # Healthy runs pass everywhere.
        (0, "pull_request", 0),
        (0, "merge_group", 0),
        (0, "push", 0),
    ]

    def test_rc1_blocks_but_rc2_does_not_fail_a_pre_merge_gate(self):
        """The routing, asserted as behaviour rather than as text.

        Both exit codes mean "the gate is not currently trustworthy", so it is
        tempting to treat them alike — but they differ in WHO CAN CLEAR THEM,
        which is the only thing that decides whether a required check may block:

          rc=1  an arm misbehaved => the decision logic lost its red-ability,
                which only a memory-path change can do => the author's to fix.
          rc=2  the committed baseline is missing / arm-mismatched / has no
                sample_sizes => nobody on the change can fix it.

        `merge_group` sits with `pull_request` on the rc=2 row, and the argument
        there is stronger rather than weaker: a queue entry has no author at all,
        and a required check that goes red on an unclearable infra fault under
        `enforce_admins: true` is not a blocked PR but a frozen repository. It is
        also behaviour-preserving — that change merges today, because today it
        merges from a PR. The blindness ALERT still fires on both events, so the
        episode is opened either way; it just does not wedge the queue.

        This used to be a regex over the step's literal text, which contradicted
        this docstring's own promise that "an equivalent rewrite stays green": it
        pinned one spelling, so ADDING an event to the tolerated set turned it red
        as loudly as collapsing the two codes would have. Now it extracts the exit
        logic and RUNS it, under the same shell GitHub uses, against the full
        matrix. Every collapse this was written to catch still turns it red — a
        bare `exit 0` fails the (1, push) row, a bare `exit $rc` fails the
        (2, pull_request) row — and a rewrite that preserves behaviour passes.
        """
        body = self._step("The gate must still be able to go RED")
        # Cut at the end of the job-summary block, NOT at the first `if [ "$rc"`.
        # Anchoring on the routing's own syntax is what made the first version of
        # this rewrite still over-pin: a `case`-outside-`if` spelling put an `if`
        # in the middle, extraction started there, and a behaviour-preserving
        # rewrite failed exactly as loudly as a collapse. The summary redirect is
        # the last thing before the routing and is not part of what is under
        # test, so it is the boundary that lets the routing be rewritten freely.
        marker = '>> "$GITHUB_STEP_SUMMARY"'
        self.assertIn(
            marker, body,
            f"the red-proof step no longer writes a job summary, so this test "
            f"cannot locate the exit routing that follows it:\n{body}",
        )
        routing = textwrap.dedent(body.rsplit(marker, 1)[-1])
        self.assertIn(
            "exit", routing,
            f"no exit routing found after the job summary:\n{body}",
        )

        for rc, event, expected in self.RED_PROOF_EXIT_MATRIX:
            script = routing.replace("${{ github.event_name }}", event)
            self.assertNotIn(
                "${{", script,
                f"the exit routing interpolates something other than "
                f"github.event_name; this test can only substitute that one:\n{routing}",
            )
            with self.subTest(rc=rc, event=event):
                proc = subprocess.run(
                    ["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c",
                     f"rc={rc}\n{script}"],
                    capture_output=True, text=True,
                )
                self.assertEqual(
                    expected, proc.returncode,
                    f"red-proof rc={rc} on {event} exited {proc.returncode}, "
                    f"expected {expected}. rc=2 must not block a pre-merge gate "
                    f"(nobody can clear it) and must stay loud on main; rc=1 must "
                    f"always block. Routing:\n{routing}",
                )

    def test_the_red_proof_reads_pipestatus_not_tees_exit_code(self):
        """Same defect as `6553dd6`, one step over.

        This step pipes through `tee`, and the workflow declares no `shell:` and
        no `defaults:`, so it runs under `bash -e` WITHOUT pipefail — `$?` is
        tee's, which is 0 whatever python did. Unguarded, an rc=1 (the gate can
        no longer go red) would report success.
        `test_every_teed_measurement_reads_pipestatus` in this file covers it as
        a file-wide invariant; named here as well so a regression points at this
        step rather than at "some step".
        """
        body = self._step("The gate must still be able to go RED")
        self.assertIn("| tee redproof.log", body)
        self.assertIn(
            "rc=${PIPESTATUS[0]}", body,
            "the red-proof takes tee's exit code, so every verdict it reaches "
            "is discarded and the step is green unconditionally.",
        )

    def test_the_block_finder_actually_found_the_required_job(self):
        """Positive control. Were the split to return a block that does not
        contain the self-check step, every assertion above would be searching
        the wrong text — and the `assertIsNotNone` would fail for a reason that
        has nothing to do with the wiring it claims to pin."""
        block = self._required_job_block()
        self.assertIn("Gate self-checks", block)
        self.assertIn("test_gate_blindness.py", block)
        self.assertNotIn("LongMemEval-S end-to-end", block)


# ---------------------------------------------------------------------------
# Every measurement piped through `tee` must read PIPESTATUS[0].
#
# This workflow has no `shell:` key and no `defaults:` block, so each `run:`
# gets GitHub's default `bash -e {0}` — `-e` but NOT `pipefail`. `cmd | tee f`
# therefore exits with tee's status, and a measurement that exited 1 or 2 reads
# as success.
#
# It is not a hypothetical omission and it was not global: 11 of the 13 tee'd
# steps in this file already wrap the call in `set +e` / `rc=${PIPESTATUS[0]}` /
# `set -e`, including the PROD arm's re-snap. The BRANCH arm's re-snap was the
# single exception — dropped when #394 copied the prod arm to create the branch
# arm — which meant a `capture-refused` (the guard that exists precisely so a
# degraded run cannot become a required check's floor) left the step green, the
# summary claiming "re-snapped", and an artifact holding only resnap.log.
#
# Pinned as an invariant over the whole file rather than as one assertion about
# line 482, so the next copied-and-adapted step inherits the check.
class EveryTeedMeasurementReadsPipestatus(unittest.TestCase):
    # `echo ... | tee` has no exit code worth protecting, and appending a
    # rendered block to the step summary is not a measurement.
    _NOT_MEASUREMENTS = ("GITHUB_STEP_SUMMARY", "echo ")

    def _teed_measurements(self):
        lines = WORKFLOW.read_text().splitlines()
        out = []
        for i, line in enumerate(lines):
            if not re.search(r"\|\s*tee\b", line):
                continue
            # The pipeline may be split over `\`-continuations, so the command
            # being piped can live on an earlier line. Walk back to the start of
            # the logical command before deciding whether this is a measurement
            # — `echo "..." \` + `| tee -a log` is one echo, not a measurement,
            # and judging it on the `| tee` line alone flags it every time.
            start = i
            while start > 0 and lines[start - 1].rstrip().endswith("\\"):
                start -= 1
            logical = " ".join(l.strip().rstrip("\\") for l in lines[start : i + 1])
            if any(tok in logical for tok in self._NOT_MEASUREMENTS):
                continue
            out.append((i + 1, logical.strip(), "\n".join(lines[i : i + 4])))
        return out

    def test_the_workflow_really_has_no_pipefail_default(self):
        """The premise. If a `defaults:`/`shell:` block ever adds pipefail this
        whole class becomes unnecessary — it should then fail loudly and be
        deleted deliberately, not keep passing for a reason that no longer
        holds."""
        text = WORKFLOW.read_text()
        self.assertNotIn("\ndefaults:", text)
        self.assertNotRegex(text, r"\n\s+shell:\s")

    def test_every_teed_measurement_reads_pipestatus(self):
        found = self._teed_measurements()
        self.assertGreaterEqual(len(found), 10, "tee-detection regex stopped matching")
        missing = [
            f"L{ln}: {src}" for ln, src, window in found if "PIPESTATUS" not in window
        ]
        self.assertEqual(
            [], missing,
            "these piped measurements would report tee's exit code, not their "
            "own:\n  " + "\n  ".join(missing),
        )

    def test_the_branch_resnap_specifically_guards_its_exit_code(self):
        """The instance this class was written for. Named separately so a
        regression points at the branch capture rather than at 'some step'."""
        text = WORKFLOW.read_text()
        step = text.split("- name: Re-snap the branch recall baseline", 1)
        self.assertEqual(len(step), 2, "the branch re-snap step was renamed")
        body = step[1].split("- name:", 1)[0]
        self.assertIn("--arm branch --update-baseline", body)
        self.assertIn("rc=${PIPESTATUS[0]}", body)
        self.assertIn("exit $rc", body)


if __name__ == "__main__":
    unittest.main(verbosity=2)
