#!/usr/bin/env python3
"""Self-checks for the ways the recall gate could stop enforcing anything.

    python scripts/memory-bench/test_gate_blindness.py

The gate is a REQUIRED check, which makes silence its most dangerous output. A
regression here does not turn CI red — it turns CI green while measuring nothing,
and the "required" badge then certifies a run that never happened. Each test below
pins one way that has actually occurred:

  1. PATH COVERAGE — a memory file missing from MEMORY_PATHS means the gate
     no-ops to green on a PR that changed memory. (#347 rewrote the authz on
     memory DELETE and the gate never ran.)
  2. TRANSIENT RESTART — a push to main runs this bench *and* the backend deploy
     concurrently, so mesh-api restarts underneath the run. Un-retried, every
     question in that window errors and the gate reports INCONCLUSIVE: the safety
     net switches itself off exactly on the commits that changed memory.
  3. LEGIBLE CAUSE — an anyio TaskGroup reports failures as "unhandled errors in
     a TaskGroup (1 sub-exception)". If that is the string the gate prints when
     it goes blind, nobody can tell why it went blind.
  4. SILENT TOOL ERROR — a `call_tool` result with `isError=True` (or any non-JSON
     body a healthy mesh-mcp tool never produces) was parsed as `{"text": ...}`,
     a shape neither `_store` nor `_search` recognise as an error. A real recall
     failure then reads as "zero items, mode unknown" with no exception and no log
     line. (evc-mesh#352: one such call collapsed a 24-question run's mode to
     "unknown", turning a real -0.5 regression into mere INCONCLUSIVE gate
     blindness — worse than #1-3, because it hides a REGRESSION, not just an
     infra hiccup.)
  5. UNSTORABLE QUESTION — the memory `key` is built from the question id, and
     Mesh validates keys against `^[a-z0-9][a-z0-9-]*[a-z0-9]$`. The two
     `gpt4_*` ids in the dataset carry an `_`, so their first `remember` was
     rejected 400 and the question never ran. Both are `temporal-reasoning`, so
     that category was scored 2/4 in every run for 6 days and post-#361 the gate
     correctly refused to score it at all. Nothing was red; a seventh of the
     safety net simply did not exist. The guard is over the WHOLE dataset, so the
     next dataset refresh cannot quietly reintroduce it.
  6. MISREPORTED CAUSE — that 400 was raised inside anyio's task groups, and the
     transport teardown's own `BrokenResourceError` replaced it on the way out.
     Six days of logs named the plumbing; none named the key. A cause that is
     knowable and then discarded is how a one-line fix stays unfound.
  7. FORGIVEN FOR EVER — the retry budget a question spent is recorded here, so a
     failure that looks transient but exhausted every attempt can be told apart
     from a blip that recovered. `--max-error-rate` forgives 10% of a run on the
     premise that those questions get measured next run; a deterministic failure
     breaks that premise and is forgiven permanently. (The classification and the
     budget itself live in run_ci.py — see test_gate_modes.py.)
"""

from __future__ import annotations

import asyncio
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import tokenize
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mesh_client_stdio as mc  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github/workflows/memory-bench.yml"

# The blindness-alert shell used to be inline in the workflow, twice, and absent
# from the arm that needed it most. It is now one composite action with one
# caller per arm. Way 8's harness executes THIS file; way 9 asserts every arm
# routes through it.
BLINDNESS_ACTION_REL = ".github/actions/blindness-issue"
BLINDNESS_ACTION = REPO_ROOT / BLINDNESS_ACTION_REL / "action.yml"


def _memory_paths() -> list[str]:
    """The MEMORY_PATHS block from the workflow, as the gate's scope step reads it."""
    text = WORKFLOW.read_text(encoding="utf-8")
    block = re.search(r"^  MEMORY_PATHS: \|\n((?:    .*\n)+)", text, re.M)
    assert block, "MEMORY_PATHS block not found in memory-bench.yml"
    return [ln.strip() for ln in block.group(1).splitlines() if ln.strip()]


class TestMemoryPathCoverage(unittest.TestCase):
    """Every memory source file must be in the gate's scope.

    Self-maintaining on purpose: a NEW memory file added tomorrow fails this test
    until it is listed, rather than silently widening the blind spot. The gate's
    scope step prefix-matches, so a directory entry covers everything under it.

    KNOWN BOUNDARY, stated rather than papered over: this fences the code the
    bench can actually detect a regression in — the remember / recall / forget
    path. Other files consume MemoryService without matching the name heuristic
    (`cmd/api/main.go`, `internal/handler/canonical_updates_handler.go`,
    `internal/service/event_bus_service.go`, `internal/service/interfaces.go`).
    They are deliberately NOT gated: the bench never calls `ListMemories`, so
    gating them would spend 16 minutes of CI to measure nothing — and a gate that
    is slow and pointless is one people switch off, which costs more than it
    protects. If the bench ever exercises those paths, gate them then.
    """

    def test_every_memory_source_file_is_gated(self):
        paths = _memory_paths()
        uncovered = []
        for f in REPO_ROOT.rglob("*.go"):
            if any(part in {"vendor", "node_modules", ".git"} for part in f.parts):
                continue
            if f.name.endswith("_test.go") or "memor" not in f.name.lower():
                continue
            rel = f.relative_to(REPO_ROOT).as_posix()
            if not any(rel.startswith(p) for p in paths):
                uncovered.append(rel)
        self.assertEqual(
            [], sorted(uncovered),
            "These memory files are NOT in MEMORY_PATHS, so a PR touching only them "
            "would report the required recall gate as GREEN without running it. "
            "Add them to MEMORY_PATHS in .github/workflows/memory-bench.yml.",
        )

    def test_the_handler_that_slipped_through_is_gated(self):
        # Regression pin for #347 specifically: memory DELETE authorization.
        self.assertIn("internal/handler/memory_handler.go", _memory_paths())


REQUIRED_CONTEXT = "Memory recall gate"


def _job_blocks() -> dict[str, str]:
    """`{job_id: raw yaml of that job}` — enough to assert on without a yaml dep.

    Deliberately text-level: what GitHub matches is text, and a structural parse
    would happily normalise away the very thing under test (a job `name:` that
    differs from the required context by one character still parses fine).
    """
    text = WORKFLOW.read_text(encoding="utf-8")
    body = text.split("\njobs:\n", 1)[1]
    starts = [(m.start(), m.group(1)) for m in re.finditer(r"^  ([a-zA-Z0-9_-]+):$", body, re.M)]
    assert starts, "no jobs found in memory-bench.yml"
    out = {}
    for i, (pos, job_id) in enumerate(starts):
        end = starts[i + 1][0] if i + 1 < len(starts) else len(body)
        out[job_id] = body[pos:end]
    return out


# ---------------------------------------------------------------------------
# 9. AN ARM WITH NO ALERT — the way the REQUIRED check went blind for two days.
# ---------------------------------------------------------------------------
# #394 split the recall gate into a required branch arm and an advisory prod
# canary. The blindness alert stayed with the canary. It reads that job's own
# `steps.*.outputs.rc`, so it could not see the required arm's verdict even in
# principle — and the required arm's own step summary told readers that "the
# alert below is what stops it being silent", with nothing below it.
#
# The result was not a false green: a missing baseline is correctly forced from
# PASS to INCONCLUSIVE. It was that the merge-gating check could not produce a
# REGRESSION verdict AT ALL, and said so only in a step summary attached to a
# green check, which nothing polls. `no-baseline` was its steady state on every
# run from 2026-07-28 05:02Z until a baseline landed — including runs on main —
# and exactly one `no-baseline` issue exists in the tracker, from before the
# split.
#
# So the pin cannot be "the workflow contains an alert step". It has to be a
# REGISTRY check: enumerate the arms that can report an INCONCLUSIVE reason, and
# require each one to have both halves wired to the shared action. That is the
# assertion #394 needed; a per-step test would have passed on #394's diff,
# because the step it would have looked at was still there — in the other job.


def _steps(job_yaml: str) -> list[str]:
    """The `- name: ...` step blocks of one job's yaml, in order, verbatim."""
    marks = [m.start() for m in re.finditer(r"^      - name: ", job_yaml, re.M)]
    return [
        job_yaml[s:(marks[i + 1] if i + 1 < len(marks) else len(job_yaml))]
        for i, s in enumerate(marks)
    ]


def _scalar(block: str, key: str) -> str | None:
    """One `key: value` from a `with:`/`uses:` mapping, folded blocks included.

    Text-level for the same reason `_job_blocks` is, and because the self-check
    step that runs this suite has stdlib only — no yaml dependency (see the
    workflow's "Stdlib only: no secrets, no network, ~1s").
    """
    lines = block.splitlines()
    for i, ln in enumerate(lines):
        m = re.match(rf"^(\s*){re.escape(key)}:(?:[ \t]+(.*))?$", ln)
        if not m:
            continue
        indent, first = len(m.group(1)), (m.group(2) or "").strip()
        if first and first not in (">-", ">", "|-", "|"):
            return first.strip("'\"")
        parts = []
        for nxt in lines[i + 1:]:
            if not nxt.strip():
                break
            if len(nxt) - len(nxt.lstrip()) <= indent:
                break
            parts.append(nxt.strip())
        return " ".join(" ".join(parts).split())
    return None


def _blindness_calls() -> dict[str, dict[str, dict]]:
    """`{job_id: {'alert': {...}, 'resolve': {...}}}` for every arm.

    Each inner dict carries the step `name`, its `if:` expression, and the `with:`
    inputs as read from the shipped workflow. Extracted rather than restated: a
    restated registry keeps passing after the real wiring is deleted, which is
    exactly what happened to the promise in the required arm's step summary.
    """
    found: dict[str, dict[str, dict]] = {}
    for job_id, job_yaml in _job_blocks().items():
        for step in _steps(job_yaml):
            if _scalar(step, "uses") != f"./{BLINDNESS_ACTION_REL}":
                continue
            mode = _scalar(step, "mode")
            name = re.match(r"^      - name: (.+)$", step, re.M).group(1).strip()
            entry = {
                "name": name,
                "if": _step_if(name),
                "title": _scalar(step, "title"),
                "marker-slug": _scalar(step, "marker-slug"),
                "step": step,
            }
            found.setdefault(job_id, {})[mode] = entry
    return found


def _arms_that_can_go_inconclusive() -> set[str]:
    """Jobs with a step that publishes a `reason_kind` — i.e. that can report an
    INCONCLUSIVE cause at all.

    Derived from the workflow, not listed here, so that adding a FOURTH arm (which
    is how this defect was introduced: #394 added the third) puts it under this
    test automatically instead of inheriting silence by default. The same shape as
    `test_only_the_paid_arms_start_the_paid_job`: an allow-list cannot know about
    an option added after it was written.
    """
    return {
        job_id
        for job_id, job_yaml in _job_blocks().items()
        if 'echo "reason_kind=' in job_yaml
    }


class TestEveryInconclusiveArmCanPage(unittest.TestCase):
    """An arm that can go blind and cannot page is the defect this class pins."""

    def test_every_arm_that_can_go_inconclusive_has_both_halves(self):
        calls = _blindness_calls()
        for job_id in sorted(_arms_that_can_go_inconclusive()):
            with self.subTest(job=job_id):
                halves = calls.get(job_id, {})
                self.assertEqual(
                    {"alert", "resolve"}, set(halves),
                    f"job {job_id!r} publishes a `reason_kind`, so it can report "
                    f"INCONCLUSIVE — but its blindness channel is {sorted(halves) or 'absent'}. "
                    f"An arm with an alert and no resolve leaves the episode open, and "
                    f"the dedup key then MUTES its own re-alerts; an arm with neither "
                    f"records its blindness in a step summary nobody polls, which is how "
                    f"the required check enforced nothing for two days after #394.",
                )

    def test_the_required_arm_is_one_of_them(self):
        """Anchor: the discovery above must actually find the arm that bit.

        Without this, a change that stopped `recall-gate-branch` from publishing a
        reason_kind would silently remove it from the population and turn the test
        above green by shrinking what it examines.
        """
        self.assertIn(
            "recall-gate-branch", _arms_that_can_go_inconclusive(),
            "the REQUIRED arm no longer publishes a `reason_kind`, so the registry "
            "test above is no longer examining it. Either the arm stopped reporting "
            "its INCONCLUSIVE cause, or the discovery heuristic drifted.",
        )

    def test_no_arm_keeps_an_inline_copy_of_the_alert_shell(self):
        """AC1: a shared path, not a third copy.

        Every inline copy of this shell has already lost something the others kept
        (the dedup window, the push-scoping, the kind-scoping — see the action's
        header). A new inline `gh issue create` next to a marker is a new place for
        all three to come back, so it is a red here rather than a review comment.
        """
        for job_id, job_yaml in _job_blocks().items():
            with self.subTest(job=job_id):
                if job_id == "prod-arm-cancelled":
                    continue  # not a blindness alert: different question, different marker
                self.assertNotIn(
                    "gh issue create", job_yaml,
                    f"job {job_id!r} opens a tracking issue inline instead of via "
                    f"{BLINDNESS_ACTION_REL}. Route it through the action.",
                )

    def test_the_shared_action_exists_and_is_referenced_by_path(self):
        self.assertTrue(
            BLINDNESS_ACTION.is_file(),
            f"{BLINDNESS_ACTION} is missing, so every `uses: ./{BLINDNESS_ACTION_REL}` "
            f"in the workflow fails at load time — every arm's alert AND resolve at "
            f"once.",
        )
        self.assertGreaterEqual(
            len(_blindness_calls()), 2,
            "fewer than two arms route through the shared action; the whole point of "
            "factoring it was that it has more than one caller.",
        )


class TestBlindnessEpisodesAreNamespacedPerArm(unittest.TestCase):
    """AC2/AC3: two arms must not silence or close each other.

    The episode identity is the issue TITLE — `resolve` finds what `alert` opened
    by exact title, and the storm guard only suppresses within one open issue. So
    two arms sharing a title is not cosmetic: a prod `no-baseline` and a branch
    `no-baseline` would be the same episode, the first to fire would suppress the
    second, and the first to RECOVER would close the other's still-live episode.
    That is a new way to go silently blind, introduced by the fix for the old one.
    """

    def test_each_arm_alerts_and_resolves_on_the_same_title(self):
        for job_id, halves in sorted(_blindness_calls().items()):
            with self.subTest(job=job_id):
                self.assertEqual(
                    halves["alert"]["title"], halves["resolve"]["title"],
                    f"job {job_id!r} raises its episode under one title and clears it "
                    f"under another, so every episode it opens stays open for ever — "
                    f"and a permanently open episode mutes its own re-alerts via the "
                    f"dedup key.",
                )

    def test_no_two_arms_share_a_title(self):
        seen: dict[str, str] = {}
        for job_id, halves in sorted(_blindness_calls().items()):
            title = halves["alert"]["title"]
            self.assertNotIn(
                title, seen,
                f"jobs {seen.get(title)!r} and {job_id!r} both track blindness under "
                f"{title!r}. They would share one episode: whichever fires first "
                f"suppresses the other, and whichever recovers first closes the "
                f"other's open episode.",
            )
            seen[title] = job_id

    def test_no_two_arms_share_a_marker_slug(self):
        seen: dict[str, str] = {}
        for job_id, halves in sorted(_blindness_calls().items()):
            slug = halves["alert"]["marker-slug"]
            self.assertTrue(
                slug, f"job {job_id!r} alerts with an empty marker-slug, so its storm "
                      f"guard keys on `<!--  kind=... -->` and collides with any other "
                      f"arm that also forgot one.",
            )
            self.assertNotIn(
                slug, seen,
                f"jobs {seen.get(slug)!r} and {job_id!r} share marker-slug {slug!r}. "
                f"Titles alone are not enough if a future change ever points two arms "
                f"at one issue — keep both namespaces distinct.",
            )
            seen[slug] = job_id

    def test_the_required_arm_names_the_stake_that_is_specific_to_it(self):
        """The consequence differs per arm and conflating them is what teaches a
        reader to discount both: the branch arm going blind means regressions can
        MERGE, the prod canary going blind means a DEPLOYED regression goes
        unnoticed. The prod alert used to claim the former, which stopped being
        true when #394 made it advisory."""
        calls = _blindness_calls()
        for job in ("recall-gate-branch", "recall-gate"):
            # A KeyError here would be an ERROR, not a FAILURE, and an errored test
            # reads as "the suite is broken" rather than "the workflow is wrong" —
            # the wrong signal for a missing alert, which is the defect itself.
            self.assertIn(
                job, calls,
                f"job {job!r} has no alert wired to {BLINDNESS_ACTION_REL}, so it has "
                f"no stakes to state — see "
                f"TestEveryInconclusiveArmCanPage for what that costs.",
            )
        branch = _scalar(calls["recall-gate-branch"]["alert"]["step"], "stakes") or ""
        prod = _scalar(calls["recall-gate"]["alert"]["step"], "stakes") or ""
        self.assertIn("merge", branch.lower(), f"branch-arm stakes: {branch!r}")
        self.assertNotIn(
            "can merge", prod.lower(),
            f"the prod canary's alert claims a regression could MERGE. It is advisory "
            f"and gates nothing — since #394 the merge gate is `recall-gate-branch`. "
            f"stakes: {prod!r}",
        )


class TestTheRequiredContextIsStillProduced(unittest.TestCase):
    """Way 7's sibling: the gate can also go blind by never reporting at all.

    `main`'s branch protection requires the literal context string
    "Memory recall gate" with `enforce_admins: true`, and GitHub matches a
    required context against the check-run NAME, not the job id. So the name is a
    public interface with a hard failure mode on both sides:

      * rename the job and leave protection alone -> the required context is
        produced by nobody, every PR carrying the change is permanently BLOCKED,
        and no override exists (observed live on #394 at 14 green checks);
      * move protection to the new name first -> every OTHER open PR is instantly
        BLOCKED, because they still produce the old one.

    Neither is recoverable from inside a PR, which is why this is pinned here
    rather than left to review. Changing the name is a legitimate decision — it
    just has to be a deliberate one, taken together with a protection edit, and
    this test is what makes it deliberate.
    """

    def test_some_job_produces_the_required_context_verbatim(self):
        names = re.findall(r"^    name: (.+)$", "".join(_job_blocks().values()), re.M)
        self.assertIn(
            REQUIRED_CONTEXT, [n.strip() for n in names],
            f"No job is named exactly {REQUIRED_CONTEXT!r}, so the required status "
            f"check on main will never report and every PR touching this file is "
            f"BLOCKED with no override (enforce_admins is true). Job names found: "
            f"{names}. If the rename is intended, patch branch protection in the "
            f"same rollout — see README 'Making the recall gate a required check'.",
        )

    def test_the_job_producing_it_runs_on_pull_request(self):
        """A required context that is skipped on PRs is the same wedge, arrived at
        by `if:` instead of by `name:`. The prod arm carries
        `if: github.event_name != 'pull_request'` precisely because it is NOT the
        required one; that guard must never end up on the job that is."""
        owner = [
            (job_id, block) for job_id, block in _job_blocks().items()
            if re.search(rf"^    name: {re.escape(REQUIRED_CONTEXT)}\s*$", block, re.M)
        ]
        self.assertEqual(1, len(owner), f"expected exactly one job named {REQUIRED_CONTEXT!r}")
        job_id, block = owner[0]
        header = block.split("    steps:", 1)[0]
        self.assertNotIn(
            "github.event_name != 'pull_request'", header,
            f"job {job_id!r} produces the required context but excludes itself from "
            f"pull_request runs — it would never report, which blocks every PR.",
        )

    def test_the_prod_arms_are_serialised_against_each_other(self):
        """Way 7: one live MESH_API_URL, no `concurrency:` anywhere, so runs
        contended inside the server until one hit its timeout and was reported
        `cancelled` — a required check with no verdict that reads like a flake.
        `cancel-in-progress: false` is the load-bearing half: the default `true`
        pre-empts a peer mid-run, losing the same verdict with less evidence."""
        for job_id, block in _job_blocks().items():
            header = block.split("    steps:", 1)[0]
            if "secrets.MESH_API_URL" not in block:
                continue
            self.assertRegex(
                header, r"concurrency:\s*\n\s*group: memory-bench-prod",
                f"job {job_id!r} targets the shared production API but is not in the "
                f"memory-bench-prod concurrency group — concurrent runs will slow "
                f"each other down inside the server until one times out.",
            )
            self.assertIn(
                "cancel-in-progress: false", header,
                f"job {job_id!r} must serialise, not pre-empt: cancel-in-progress "
                f"defaults to true, which aborts a peer mid-run.",
            )

    def test_the_required_arm_is_not_queued_behind_the_canary(self):
        """It builds its own postgres + embedder, so it contends for nothing. A
        required check waiting on an advisory one hands its latency to a job
        nobody is blocked on."""
        block = next(
            b for b in _job_blocks().values()
            if re.search(rf"^    name: {re.escape(REQUIRED_CONTEXT)}\s*$", b, re.M)
        )
        self.assertNotIn("memory-bench-prod", block.split("    steps:", 1)[0])


class TestEverySelfCheckIsActuallyInvoked(unittest.TestCase):
    """A self-check that CI never runs is a guard in name only.

    Found on #2a079432 by counting invocations rather than reading the tree:
    `test_gate_dense_arm.py` and `test_check_captured_baseline.py` were both
    present, both passing, and both credited by the README as the pin for their
    failure mode — and **neither appeared in any workflow step**. They arrived
    with the PRs that wrote them and nobody wired them, so "pinned by X" was true
    about the file and false about CI.

    Same shape as everything else in this file — a confident green about
    something never evaluated — one level up, in the harness guarding the
    harness.

    Two construction notes, both of which this test got wrong on the first
    attempt and which are the reason it is worth reading:

    * **Discovery is derived from the directory, never from a list kept here.**
      A hand-maintained list needs the same edit that was missed, so pinning
      against a copy of it would reproduce the bug inside the test meant to
      catch it.
    * **An invocation is matched, not a mention.** The first version asked
      `name in job_block`, and the step carries a COMMENT naming the two files
      above. Deleting the actual `python …` line then left the name behind in
      the prose and the test stayed green — a source-grep surviving an orphaned
      call. Only lines that would really execute count.
    """

    BENCH_DIR = REPO_ROOT / "scripts/memory-bench"
    # An invocation, i.e. a line CI would run. A comment cannot match: `#` is not
    # `python`, and the path has to be the command's argument.
    INVOCATION = re.compile(r"^\s*python[0-9.]*\s+scripts/memory-bench/(\S+\.py)", re.M)

    def _self_check_scripts(self) -> set[str]:
        """Files that ARE a self-check: a `test_*.py`, or a script offering
        `--selftest`. Both forms are in use (`dense_arm_control.py --selftest`,
        `prod_window.py --selftest`); counting only `test_*.py` would miss them."""
        found = set()
        for f in sorted(self.BENCH_DIR.glob("*.py")):
            if f.name.startswith("test_"):
                found.add(f.name)
                continue
            if '"--selftest"' in f.read_text(encoding="utf-8"):
                found.add(f.name)
        return found

    def _required_job_block(self) -> str:
        return next(
            b for b in _job_blocks().values()
            if re.search(rf"^    name: {re.escape(REQUIRED_CONTEXT)}\s*$", b, re.M)
        )

    def test_the_required_job_invokes_every_self_check(self):
        invoked = set(self.INVOCATION.findall(self._required_job_block()))
        missing = sorted(self._self_check_scripts() - invoked)
        self.assertEqual(
            [], missing,
            f"these self-checks exist but the REQUIRED job never runs them, so they "
            f"guard nothing on a PR: {missing}. Add them to the 'Gate self-checks' "
            f"step in the job named {REQUIRED_CONTEXT!r}.",
        )

    def test_the_discovery_actually_finds_the_known_checks(self):
        """Positive control on the finder. Were the glob to match nothing,
        `missing` above would be empty and the test would pass having checked
        nothing — the very failure it exists to prevent, one level down."""
        found = self._self_check_scripts()
        self.assertGreaterEqual(len(found), 7, f"discovery returned too little: {found}")
        for expected in ("test_gate_dense_arm.py", "dense_arm_control.py", "prod_window.py"):
            self.assertIn(expected, found, f"{expected} was not discovered as a self-check")

    def test_the_required_arm_can_capture_its_own_baseline(self):
        """Without a capture path the required check is `no-baseline` for ever —
        required, and blocking nothing but the dense-arm control.

        The README used to say "dispatch the workflow on main; the branch job
        writes it with --arm branch". The job does run on dispatch (it carries no
        `if:`), but nothing in it ever passed `--update-baseline`, so the
        documented procedure was inert — discoverable only by someone following
        it after the merge and finding no artifact.

        Asserted on the invocation, not on the word: `--arm branch` appearing in
        the judging call would satisfy a substring check while capturing nothing.
        """
        block = self._required_job_block()
        capture = re.search(
            r"^\s*python run_ci\.py[^\n]*--update-baseline[^\n]*$", block, re.M
        ) or re.search(
            r"^\s*python run_ci\.py .*\\\n\s*.*--update-baseline", block, re.M
        )
        self.assertIsNotNone(
            capture,
            "the required arm has no `--update-baseline` invocation, so "
            "baseline_retrieval_branch.json cannot be produced by CI and the gate "
            "stays permanently INCONCLUSIVE on no-baseline.",
        )
        self.assertIn(
            "--arm branch", capture.group(0),
            "the capture must name `--arm branch`, or it writes the PROD baseline "
            "file from the branch arm's numbers — an arm-mismatch baked into the "
            "floor rather than caught as one.",
        )
        self.assertIn(
            "baseline_retrieval_branch.json", block,
            "the captured baseline is never uploaded, so the run produces a file "
            "that dies with the runner.",
        )

    def test_a_mention_in_prose_does_not_count_as_an_invocation(self):
        """Positive control on the matcher, in the direction that actually broke.
        The step's own comment names two of these scripts; if the pattern counted
        that, removing the real line would go unnoticed."""
        commented_out = "      # python scripts/memory-bench/test_gate_dense_arm.py\n"
        prose = "      # see test_gate_dense_arm.py for the dense-arm pin\n"
        self.assertEqual([], self.INVOCATION.findall(prose))
        self.assertEqual([], self.INVOCATION.findall(commented_out))
        self.assertEqual(
            ["test_gate_dense_arm.py"],
            self.INVOCATION.findall("          python scripts/memory-bench/test_gate_dense_arm.py\n"),
        )


class TestNoDeadCodeqlSuppression(unittest.TestCase):
    """`# codeql[rule-id]` is a CodeQL-CLI feature the Actions integration
    ignores, so writing one silences nothing while reading exactly like a
    handled alert. Measured, not assumed: alert #18 was raised at
    cbf93f7b:187 — the very line carrying the comment — by the analysis of the
    commit that added it. The supported mechanism is an API/UI dismissal.

    This pins the honest state. Without it the next session reaches for the
    comment again (this one did, on top of two prior sink-relocations), and a
    suppression that does not suppress is worse than no suppression at all."""

    def test_no_source_file_relies_on_an_inline_suppression(self):
        # BOTH placements are flagged. The two recorded attempts differ only in
        # where the comment sat — 371dfe7 put it on the preceding line, cbf93f7b
        # moved it to trailing — and neither suppressed anything, so pinning only
        # the trailing form would let the first one back in unnoticed.
        #
        # tokenize + an ANCHORED match, not a line regex: prose ABOUT the syntax
        # (this docstring, and the block in ci_bootstrap.py explaining why there
        # is no suppression) has to survive. A real directive begins the comment;
        # prose reaches the token mid-sentence, so `re.match` separates them and
        # STRING tokens never reach this loop at all.
        offenders = []
        for f in sorted((REPO_ROOT / "scripts/memory-bench").rglob("*.py")):
            with io.StringIO(f.read_text(encoding="utf-8")) as buf:
                for tok in tokenize.generate_tokens(buf.readline):
                    if tok.type is tokenize.COMMENT and re.match(
                        r"#\s*(codeql|lgtm)\[", tok.string
                    ):
                        offenders.append(f"{f.relative_to(REPO_ROOT)}:{tok.start[0]}")
        self.assertEqual(
            [], offenders,
            "inline CodeQL/LGTM suppression comments are ignored by GitHub code "
            "scanning — the alert stays open while the comment claims otherwise. "
            "Dismiss via the code-scanning API with a justification instead: "
            f"{offenders}",
        )


class TestFlattenExc(unittest.TestCase):
    def test_taskgroup_wrapper_is_unwrapped_to_the_real_cause(self):
        inner = RuntimeError("Connection closed")
        group = ExceptionGroup("unhandled errors in a TaskGroup", [inner])
        out = mc.flatten_exc(group)
        self.assertIn("Connection closed", out)
        self.assertNotIn("sub-exception", out)
        self.assertNotIn("TaskGroup", out)

    def test_nested_groups_are_flattened(self):
        deep = ExceptionGroup("outer", [ExceptionGroup("inner", [ValueError("boom")])])
        self.assertIn("boom", mc.flatten_exc(deep))

    def test_plain_exception_is_named(self):
        self.assertEqual("ValueError: nope", mc.flatten_exc(ValueError("nope")))


class TestTransientDetection(unittest.TestCase):
    def test_restart_symptoms_are_transient(self):
        # The literal strings mesh-mcp / the MCP client actually emit on a restart.
        for msg in ("Connection closed", "Bad Gateway", "API error 502"):
            self.assertTrue(mc._is_transient(RuntimeError(msg)), msg)

    def test_transient_leaf_inside_a_group_is_seen(self):
        group = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        self.assertTrue(mc._is_transient(group))

    def test_a_harness_bug_is_not_transient(self):
        # Must NOT be retried: retrying a real bug just burns the job's clock.
        self.assertFalse(mc._is_transient(KeyError("haystack_sessions")))

    def test_incidental_digits_are_not_mistaken_for_a_gateway_error(self):
        # The fixtures carry "502"/"503"/"504" as ordinary digits >130 times, and
        # ids are hex. A loose substring match would read a REAL bug as a restart:
        # retry it, delay the run, then report it under a cause that never was.
        for msg in (
            "ValueError: expected 502 tokens, got 41",
            "KeyError: 'bench-9f504a2c-s3'",
            "RuntimeError: remember failed: quota 503 of 1000 rows",
        ):
            self.assertFalse(mc._is_transient(RuntimeError(msg)), msg)

    def test_real_gateway_errors_are_still_caught(self):
        for msg in (
            "Agent authentication failed: Bad Gateway: API error 502",
            "http 503 service unavailable",
            "status: 504 gateway timeout",
        ):
            self.assertTrue(mc._is_transient(RuntimeError(msg)), msg)


class _Harness(unittest.TestCase):
    def setUp(self):
        mc.MeshMemoryClient._exhausted_questions = 0
        # Keep the tests fast: the backoff itself is not what's under test.
        patcher = mock.patch.object(mc.time, "sleep")
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)
        # `_run` is a coroutine function; stub it so the tests never build a
        # coroutine object that the mocked asyncio.run would leave un-awaited.
        # MagicMock, not the default AsyncMock: patching a coroutine function
        # would hand asyncio.run a coroutine nobody awaits.
        run_patcher = mock.patch.object(
            mc.MeshMemoryClient, "_run", new=mock.MagicMock(return_value=None)
        )
        run_patcher.start()
        self.addCleanup(run_patcher.stop)
        self.addCleanup(setattr, mc.MeshMemoryClient, "_exhausted_questions", 0)

    def _call(self, client):
        return client.ingest_and_search(
            sessions=[], dates=[], format_session_text=lambda t, date: "", query="q", top_k=10
        )


class TestRetryRidesOutARestart(_Harness):
    def test_question_recovers_when_the_api_comes_back(self):
        client = mc.MeshMemoryClient(question_id="q1")
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(
            mc.asyncio, "run", side_effect=[closed, closed, ["hit"]]
        ) as run:
            self.assertEqual(["hit"], self._call(client))
        self.assertEqual(3, run.call_count)

    def test_a_nontransient_failure_is_not_retried(self):
        client = mc.MeshMemoryClient(question_id="q1")
        with mock.patch.object(mc.asyncio, "run", side_effect=KeyError("bug")) as run:
            with self.assertRaises(KeyError):
                self._call(client)
        self.assertEqual(1, run.call_count)

    def test_breaker_stops_paying_backoff_once_the_api_is_really_down(self):
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=closed) as run:
            for _ in range(mc.BREAKER_TRIP_AFTER):
                with self.assertRaises(BaseException):
                    self._call(mc.MeshMemoryClient(question_id="down"))
            tripped = run.call_count
            # Breaker is open now: further questions fail fast, one attempt each.
            with self.assertRaises(BaseException):
                self._call(mc.MeshMemoryClient(question_id="after"))
            self.assertEqual(tripped + 1, run.call_count)

    def test_a_recovery_rearms_the_breaker(self):
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=[closed, ["hit"]]):
            self._call(mc.MeshMemoryClient(question_id="blip"))
        self.assertEqual(0, mc.MeshMemoryClient._exhausted_questions)


class TestTheRetryBudgetSpentIsRecorded(_Harness):
    """The gate classifies a transient-LOOKING failure by whether retrying ever
    helped, so the client has to say how much of its allowance it actually spent.
    Without this, four consecutive "Connection closed" deaths are indistinguishable
    from one blip that recovered — and the permanent case is the one that hides.
    """

    def test_a_question_that_burned_every_attempt_says_so(self):
        client = mc.MeshMemoryClient(question_id="down")
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=closed):
            with self.assertRaises(BaseException):
                self._call(client)
        self.assertEqual(mc.CONNECT_RETRIES, client.attempts_allowed)
        self.assertEqual(client.attempts_allowed, client.attempts_made)

    def test_a_question_that_recovered_did_not_burn_them_all(self):
        client = mc.MeshMemoryClient(question_id="blip")
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=[closed, ["hit"]]):
            self._call(client)
        self.assertLess(client.attempts_made, client.attempts_allowed)

    def test_an_open_breaker_leaves_an_allowance_of_one(self):
        """`attempts_made == attempts_allowed` must not read as "retrying did not
        help" when the breaker had already withdrawn the retries. One attempt out
        of one proves nothing, and the gate's classifier keys on exactly this."""
        closed = ExceptionGroup("tg", [RuntimeError("Connection closed")])
        with mock.patch.object(mc.asyncio, "run", side_effect=closed):
            for _ in range(mc.BREAKER_TRIP_AFTER):
                with self.assertRaises(BaseException):
                    self._call(mc.MeshMemoryClient(question_id="down"))
            after = mc.MeshMemoryClient(question_id="after")
            with self.assertRaises(BaseException):
                self._call(after)
        self.assertEqual(1, after.attempts_allowed)
        self.assertEqual(1, after.attempts_made)


class _FakeSession:
    """A scripted stand-in for an MCP ClientSession.

    `die_after_stores`: the connection drops once this many `remember` calls have
    landed — i.e. the rows EXIST in the store, but every later call on this
    session (including the `forget`s that would clean them up) raises. That is
    the mesh-api-restarts-mid-ingest case, and the whole reason cleanup cannot
    live on the connection that failed.
    """

    seq = 0

    def __init__(
        self,
        log: list[tuple[str, str]],
        die_after_stores: int | None,
        store: set[str] | None = None,
    ):
        self.log = log
        self.die_after_stores = die_after_stores
        # The server-side rows, shared across sessions — a restart does not empty
        # the database, which is the entire point.
        self.store = store if store is not None else set()
        self.stores = 0
        self.dead = False

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_exc):
        return False

    async def initialize(self):
        if self.dead:
            raise RuntimeError("Connection closed")

    async def call_tool(self, name, args):
        if self.dead:
            raise RuntimeError("Connection closed")
        if name == "remember":
            self.stores += 1
            # A FRESH id per store, as Mesh does. Deriving the id from the key
            # would let a re-store of the same session collide with — and so
            # silently reclaim — the row a previous attempt orphaned, hiding the
            # very leak these tests exist to catch.
            _FakeSession.seq += 1
            mid = f"mem-{_FakeSession.seq}"
            self.store.add(mid)          # the row is COMMITTED server-side
            self.log.append(("remember", mid))
            if self.die_after_stores is not None and self.stores >= self.die_after_stores:
                self.dead = True
                # Committed, THEN the pipe dropped. The row is live and its id
                # never reaches the client — unreachable by id, findable by tag.
                raise RuntimeError("Connection closed")
            return {"memory": {"id": mid, "key": args["key"]}}
        if name == "forget":
            self.store.discard(args["memory_id"])
            self.log.append(("forget", args["memory_id"]))
            return {}
        if name == "recall":
            self.log.append(("recall", ""))
            # The tag sweep: return whatever this question actually left behind.
            return {
                "items": [{"id": mid, "tags": []} for mid in sorted(self.store)],
                "search_mode": "bm25-only",
            }
        raise AssertionError(f"unexpected tool {name}")


class TestCleanupSurvivesTheConnectionDying(unittest.TestCase):
    """The retry must not leak the fixtures of the attempt it is retrying.

    Cleanup used to run its deletes down the same connection whose death caused
    the failure, swallowing the errors — so an attempt that died mid-store left
    its haystack behind. `_pending` lives on the CLIENT, not the connection, so
    the next attempt's fresh session finishes the job.

    Drives the real `_run`/`_sweep` through a fake MCP transport. Mocking
    `asyncio.run` instead would make this test pass with the fix reverted.
    """

    def setUp(self):
        mc.MeshMemoryClient._exhausted_questions = 0
        self.addCleanup(setattr, mc.MeshMemoryClient, "_exhausted_questions", 0)
        patcher = mock.patch.object(mc.time, "sleep")
        patcher.start()
        self.addCleanup(patcher.stop)

    def _install_transport(self, sessions: list[_FakeSession]):
        """Inject fake `mcp` modules; `_run` imports them at call time."""
        import contextlib
        import types

        @contextlib.asynccontextmanager
        async def _stdio_client(_params):
            yield (None, None)

        it = iter(sessions)
        mcp_mod = types.ModuleType("mcp")
        mcp_mod.ClientSession = lambda _r, _w: next(it)
        mcp_mod.StdioServerParameters = lambda **kw: kw
        stdio_mod = types.ModuleType("mcp.client.stdio")
        stdio_mod.stdio_client = _stdio_client
        client_mod = types.ModuleType("mcp.client")

        for name, mod in [
            ("mcp", mcp_mod), ("mcp.client", client_mod), ("mcp.client.stdio", stdio_mod),
        ]:
            p = mock.patch.dict(sys.modules, {name: mod})
            p.start()
            self.addCleanup(p.stop)

    def _ingest(self, client, n_sessions=3):
        return client.ingest_and_search(
            sessions=[[{"role": "user", "content": "x"}]] * n_sessions,
            dates=["2026-01-01"] * n_sessions,
            format_session_text=lambda turns, date: "text",
            query="q",
            top_k=10,
        )

    def test_rows_orphaned_by_a_dead_connection_are_deleted_by_the_retry(self):
        log: list[tuple[str, str]] = []
        db: set[str] = set()   # survives the "restart", as a real database does
        dying = _FakeSession(log, die_after_stores=2, store=db)
        healthy = _FakeSession(log, die_after_stores=None, store=db)
        self._install_transport([dying, healthy])

        client = mc.MeshMemoryClient(question_id="q1")
        self._ingest(client)  # attempt 1 dies mid-ingest; attempt 2 must clean up after it

        self.assertTrue(
            any(op == "remember" for op, _ in log), "the fake must commit rows to orphan"
        )
        self.assertEqual(
            set(), db,
            "fixtures committed by the attempt that died are still in the store — "
            "the bench leaked its haystack, which is what poisons real agents' recall()",
        )
        self.assertEqual([], client._pending)

    def test_the_row_whose_id_never_came_back_is_still_reachable(self):
        # The killer case: `remember` COMMITS, then the pipe drops before the id
        # reaches us. Nothing in `_pending` refers to that row. Only the tag does.
        log: list[tuple[str, str]] = []
        db: set[str] = set()
        self._install_transport([
            _FakeSession(log, die_after_stores=1, store=db),   # dies on its 1st store
            _FakeSession(log, die_after_stores=None, store=db),
        ])
        client = mc.MeshMemoryClient(question_id="q1")
        self._ingest(client)
        self.assertEqual(
            set(), db, "a row whose id we never received was left behind forever"
        )

    def test_a_leak_that_survives_every_retry_is_not_silent(self):
        log: list[tuple[str, str]] = []
        db: set[str] = set()
        # Every session dies immediately: cleanup never gets a live connection.
        self._install_transport(
            [_FakeSession(log, die_after_stores=1, store=db) for _ in range(4)]
        )
        client = mc.MeshMemoryClient(question_id="q1")
        with self.assertLogs(mc.logger, level="ERROR") as logs:
            with self.assertRaises(BaseException):
                self._ingest(client)
        self.assertTrue(
            any("ORPHANED FIXTURES" in m for m in logs.output),
            "fixtures left in a shared store must never be abandoned quietly",
        )


class _ToolBlock:
    def __init__(self, text):
        self.text = text


class _ToolResult:
    """Minimal stand-in for `mcp.types.CallToolResult` (content + isError)."""

    def __init__(self, *, text=None, is_error=False):
        self.content = [_ToolBlock(text)] if text is not None else []
        self.isError = is_error


class TestSilentToolErrorIsSurfaced(unittest.TestCase):
    """evc-mesh#352: an `isError` (or non-JSON) tool result must become an
    `{"error": ...}` payload — the only shape `_store`/`_search` treat as a
    failure — never the old `{"text": ...}` that both silently ignore."""

    def test_isError_with_text_becomes_an_error_payload(self):
        result = _ToolResult(text="recall failed: workspace not found", is_error=True)
        payload = mc._parse_tool_payload(result)
        self.assertEqual(
            {"error": "recall failed: workspace not found"}, payload
        )

    def test_isError_with_no_text_still_reports_an_error(self):
        result = _ToolResult(is_error=True)
        payload = mc._parse_tool_payload(result)
        self.assertIn("error", payload)

    def test_non_json_body_without_isError_is_still_an_error(self):
        # No healthy mesh-mcp tool ever answers success with a non-JSON body —
        # a body that fails to parse is corruption/truncation, not "empty".
        result = _ToolResult(text="<html>502 Bad Gateway</html>", is_error=False)
        payload = mc._parse_tool_payload(result)
        self.assertIn("error", payload)
        self.assertNotIn("text", payload, "the old silent-swallow shape must be gone")

    def test_valid_json_dict_on_success_passes_through_unchanged(self):
        result = _ToolResult(
            text='{"items": [], "search_mode": "bm25-only", "degraded": true}'
        )
        payload = mc._parse_tool_payload(result)
        self.assertEqual(
            {"items": [], "search_mode": "bm25-only", "degraded": True}, payload
        )

    def test_valid_json_list_on_success_is_wrapped_as_items(self):
        result = _ToolResult(text='[{"id": "m1"}]')
        payload = mc._parse_tool_payload(result)
        self.assertEqual({"items": [{"id": "m1"}]}, payload)

    def test_a_plain_dict_result_passes_through_as_is(self):
        # _FakeSession (and the real MCP low-level client, in some paths) can
        # hand back an already-decoded dict; must not be re-parsed or rejected.
        self.assertEqual({"items": []}, mc._parse_tool_payload({"items": []}))

    def test_none_result_is_empty_not_an_error(self):
        self.assertEqual({}, mc._parse_tool_payload(None))

    def test_search_raises_on_the_error_payload_it_would_now_receive(self):
        """Integration guard: _search already gates on `.get("error")` — confirm
        the NEW error shape actually trips that gate (the old `{"text": ...}`
        shape did not, which is the whole bug)."""
        client = mc.MeshMemoryClient(question_id="q1")

        class _ErrorSession:
            async def call_tool(self, name, args):
                return _ToolResult(text="recall failed: 500", is_error=True)

        with self.assertRaises(RuntimeError):
            asyncio.run(client._search(_ErrorSession(), "query", 10))


DATASET = Path(__file__).resolve().parent / "data" / "lme_s_24.json"


class TestEveryQuestionCanBeStored(unittest.TestCase):
    """Every question in the dataset must produce keys Mesh will accept.

    Asserted over the whole dataset rather than over the two ids that were
    actually broken: the bug was not "these two ids are unusual", it was "nothing
    checked". A refresh that pulls in `gpt4_*`-style ids again fails here instead
    of silently deleting a category from the gate's coverage.
    """

    @classmethod
    def setUpClass(cls):
        cls.entries = json.loads(DATASET.read_text(encoding="utf-8"))
        assert isinstance(cls.entries, list) and cls.entries, "dataset is empty"

    def test_the_dataset_still_contains_the_ids_that_broke_it(self):
        """Pin the fixture the other tests depend on. If a refresh drops both
        `gpt4_*` ids, the checks below still pass while testing nothing."""
        ids = {e["question_id"] for e in self.entries}
        self.assertTrue(
            {"gpt4_4929293a", "gpt4_7f6b06db"} & ids,
            "no underscore-bearing id left in the dataset — this suite no longer "
            "exercises the sanitizer on a real failing case",
        )

    def test_every_generated_key_is_valid_and_unique(self):
        seen: dict[str, str] = {}
        for entry in self.entries:
            qid = entry["question_id"]
            client = mc.MeshMemoryClient(question_id=qid)
            n = len(entry.get("haystack_dates") or [])
            self.assertGreater(n, 0, f"{qid}: no haystack sessions to key")
            for idx in range(n):
                key = f"{client.key_prefix}-s{idx}"
                self.assertRegex(
                    key,
                    mc.MESH_KEY_RE,
                    f"{qid}: key Mesh would reject with 400",
                )
                # `remember` UPSERTs on the key, so a duplicate does not error —
                # it overwrites another question's haystack and both questions are
                # then scored against half their evidence.
                self.assertNotIn(
                    key, seen, f"key collision between {seen.get(key)} and {qid}"
                )
                seen[key] = qid
        self.assertEqual(
            len(self.entries), len({e["question_id"] for e in self.entries})
        )

    def test_the_tag_keeps_the_raw_id(self):
        """Only the key is sanitized. The tag is the recall filter and the cleanup
        handle, so the id has to survive in it verbatim — an `_` folded to a `-`
        here would stop matching the rows this run itself just stored.

        Both names now carry a run nonce (#eb1c5617), so this asserts the
        PROPERTY — raw id in the tag, sanitized id in the key — rather than a
        literal, which would only re-encode today's format.
        """
        client = mc.MeshMemoryClient(question_id="gpt4_4929293a", run_nonce="n0nce")
        self.assertEqual("bench-n0nce-gpt4_4929293a", client.bench_tag)
        self.assertEqual("bench-n0nce-gpt4-4929293a-4581bcc5", client.key_prefix)
        # The raw id survives unsanitized in the tag...
        self.assertIn("gpt4_4929293a", client.bench_tag)
        # ...and does NOT in the key, which the server validates.
        self.assertNotIn("_", client.key_prefix)

    def test_an_already_safe_id_is_passed_through_untouched(self):
        """22 of 24 ids need nothing done to them, and their keys must not churn:
        an unnecessary rename orphans rows a previous run is still cleaning up."""
        self.assertEqual("184da446", mc.sanitize_key_component("184da446"))

    def test_ids_that_differ_only_in_a_separator_do_not_collide(self):
        """The sanitizer is lossy, and `remember` UPSERTs — so lossiness is not a
        cosmetic concern here, it silently merges two questions' fixtures."""
        self.assertNotEqual(
            mc.sanitize_key_component("gpt4_4929293a"),
            mc.sanitize_key_component("gpt4-4929293a"),
        )

    def test_an_id_shaped_like_a_fold_does_not_collide_with_a_real_fold(self):
        """The two branches must not share an output space.

        A folded id (`<slug>-<8 hex>`) is already key-safe, so feeding it back in
        would take the PASSTHROUGH branch and land on the exact key the fold
        produces for the raw id it came from. `remember` UPSERTs, so the second
        question would overwrite the first's haystack with no error and both
        would then be scored against half their evidence.
        """
        folded = mc.sanitize_key_component("gpt4_4929293a")
        self.assertRegex(folded, r"-[0-9a-f]{8}$")
        self.assertNotEqual(folded, mc.sanitize_key_component(folded))

    def test_the_fold_refusal_does_not_churn_the_ids_that_did_not_need_it(self):
        """The refusal must be narrow: no real id ends in `-<8 hex>`, so all 22
        stable keys stay byte-identical and nothing gets renamed for nothing."""
        for raw in ("184da446", "abc-1234", "a-0123456"):
            with self.subTest(raw=raw):
                self.assertEqual(raw, mc.sanitize_key_component(raw))

    def test_a_degenerate_id_still_yields_a_valid_key(self):
        for raw in ("___", "-", "", "A_B", "a--b", "_lead", "trail_", "x-deadbeef"):
            with self.subTest(raw=raw):
                self.assertRegex(
                    f"bench-{mc.sanitize_key_component(raw)}-s0", mc.MESH_KEY_RE
                )


class TestToolErrorSurvivesTheTransportTeardown(unittest.TestCase):
    """The reported cause must be the tool's rejection, not its unwind artifact.

    Drives the real `_run` through a transport whose teardown raises, which is
    what anyio does when the task groups are cancelled out from under it. A test
    that mocked `asyncio.run` would pass with the fix fully reverted.
    """

    VALIDATION = (
        "Bad Request: Validation failed (key: key must match pattern "
        "^[a-z0-9][a-z0-9-]*[a-z0-9]$)"
    )

    class _BrokenResourceError(Exception):
        """Stands in for `anyio.BrokenResourceError`."""

    def setUp(self):
        mc.MeshMemoryClient._exhausted_questions = 0
        self.addCleanup(setattr, mc.MeshMemoryClient, "_exhausted_questions", 0)
        patcher = mock.patch.object(mc.time, "sleep")
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)

    def _install(self, *, teardown_raises: bool):
        import contextlib
        import types

        broken = self._BrokenResourceError

        @contextlib.asynccontextmanager
        async def _stdio_client(_params):
            try:
                yield (None, None)
            finally:
                if teardown_raises:
                    # Cancelling the transport out from under an in-flight call
                    # raises here, DURING the unwind of the original exception —
                    # and this one replaces it.
                    raise broken("the pipe is gone")

        rejecting = self

        class _Session:
            async def __aenter__(self):
                return self

            async def __aexit__(self, *_exc):
                return False

            async def initialize(self):
                return None

            async def call_tool(self, name, args):
                if name == "remember":
                    return _ToolResult(text=rejecting.VALIDATION, is_error=True)
                return {}

        mcp_mod = types.ModuleType("mcp")
        mcp_mod.ClientSession = lambda _r, _w: _Session()
        mcp_mod.StdioServerParameters = lambda **kw: kw
        stdio_mod = types.ModuleType("mcp.client.stdio")
        stdio_mod.stdio_client = _stdio_client
        client_pkg = types.ModuleType("mcp.client")
        client_pkg.stdio = stdio_mod
        patcher = mock.patch.dict(
            sys.modules,
            {"mcp": mcp_mod, "mcp.client": client_pkg, "mcp.client.stdio": stdio_mod},
        )
        patcher.start()
        self.addCleanup(patcher.stop)

    def _call(self, client):
        return client.ingest_and_search(
            sessions=[[{"role": "user", "content": "x"}]],
            dates=["2023/05/10 (Wed) 01:57"],
            format_session_text=lambda turns, date: "session text",
            query="q",
            top_k=10,
        )

    def test_the_validation_message_is_what_gets_reported(self):
        self._install(teardown_raises=True)
        client = mc.MeshMemoryClient(question_id="gpt4_4929293a")
        with self.assertRaises(BaseException) as caught:
            self._call(client)
        reported = mc.flatten_exc(caught.exception)
        self.assertIn("key must match pattern", reported)
        self.assertNotEqual(
            "BrokenResourceError: the pipe is gone",
            reported,
            "the teardown artifact replaced the cause again",
        )

    def test_the_teardown_artifact_is_kept_as_context_not_dropped(self):
        """Preferring the tool error must not hide the transport failure outright —
        a run where BOTH happened is diagnosed from both halves."""
        self._install(teardown_raises=True)
        with self.assertRaises(BaseException) as caught:
            self._call(mc.MeshMemoryClient(question_id="gpt4_4929293a"))
        self.assertIn("BrokenResourceError", mc.flatten_exc(caught.exception))

    def test_a_permanent_rejection_is_not_retried(self):
        """A 400 will be a 400 four times over. Paying ~50s of backoff per
        question turns a clear failure into a timed-out job."""
        self._install(teardown_raises=True)
        with self.assertRaises(BaseException):
            self._call(mc.MeshMemoryClient(question_id="gpt4_4929293a"))
        self.sleep.assert_not_called()

    def test_a_clean_propagation_is_reported_once(self):
        """No teardown artifact: the RuntimeError already carries the message, so
        it must be passed through rather than re-wrapped around itself."""
        self._install(teardown_raises=False)
        with self.assertRaises(RuntimeError) as caught:
            self._call(mc.MeshMemoryClient(question_id="gpt4_4929293a"))
        self.assertEqual(
            1,
            mc.flatten_exc(caught.exception).count("key must match pattern"),
        )

    def test_a_transport_failure_with_no_tool_error_is_untouched(self):
        """The promotion must not manufacture a cause it does not have."""
        client = mc.MeshMemoryClient(question_id="q1")
        boom = RuntimeError("Connection closed")
        self.assertIs(boom, client._surfaced(boom))


# ---------------------------------------------------------------------------
# 8. RE-ARMED BY ACKNOWLEDGEMENT — the storm guard on the blindness alerts.
# ---------------------------------------------------------------------------
# The alert steps dedup by writing an HTML marker (`<!-- recall-gate-blind
# kind=... -->`) into the comment and refusing to post again while that marker
# is already live on the tracking issue. The check used to read
#
#     (.comments | last | .body) // .body // ""
#
# i.e. ONLY the newest comment. Any comment that is not itself an alert — a
# human acknowledging the issue, asking a question, posting status — displaced
# the marker from the last position and the next run posted the identical alert
# again. Acknowledging an alert was precisely the act that re-armed it: the
# guard was weakest exactly when someone was paying attention.
#
# Observed on evc-mesh#397, 2026-07-28, not inferred:
#     04:43:06Z  github-actions   kind=version-mismatch
#     04:46:22Z  (human comment)  no marker
#     04:58:24Z  github-actions   kind=version-mismatch   <- duplicate
# Run 30328826762 logged `Commented on tracking issue #397.`, not the
# `already reports` branch, so the kind-scoping was correct and irrelevant —
# the search WINDOW is what failed.
#
# These tests EXECUTE the shipped step. The defect under repair is a misread of
# what a `jq` expression selects, so a test that reads the expression back is
# the same act that produced the bug. The script is pulled out of the workflow
# verbatim (never copied here) and run under bash with a stand-in `gh`, so
# deleting or reverting the guard turns this red rather than leaving a
# self-satisfied assertion about a string that no longer runs.


def _action_alert_script() -> str:
    """The shared blindness action's `run:` body, dedented, verbatim.

    Extracted rather than transcribed on purpose: a copy of a rule survives the
    deletion of the rule, and this suite exists to notice deletions.

    This used to read the `run:` block of a named step in memory-bench.yml. The
    shell now lives in a composite action with one caller per arm, so there is
    ONE body to execute and the per-arm question moved to
    `TestEveryInconclusiveArmCanPage` (does each arm reach it?). Both halves are
    needed: executing the script proves the guard works, the registry proves the
    arm that needed it is wired to the script that has it.
    """
    lines = BLINDNESS_ACTION.read_text(encoding="utf-8").splitlines()
    heads = [i for i, ln in enumerate(lines) if ln.strip() == "run: |"]
    assert len(heads) == 1, (
        f"expected exactly one `run: |` in {BLINDNESS_ACTION}, found {len(heads)}. "
        f"The whole point of the action is that alert and resolve share one body; "
        f"splitting it puts the issue lookup back to two copies one layer down."
    )
    indent = len(lines[heads[0]]) - len(lines[heads[0]].lstrip())
    body: list[str] = []
    for line in lines[heads[0] + 1:]:
        if line.strip() and (len(line) - len(line.lstrip())) <= indent:
            break
        body.append(line[indent + 2:])
    script = "\n".join(body)
    assert script.strip(), f"empty run: block in {BLINDNESS_ACTION}"
    assert "${{" not in script, (
        f"{BLINDNESS_ACTION} interpolates a GitHub expression inside `run:`; this "
        f"harness executes the block under plain bash, so what it proves would no "
        f"longer be what CI runs. Move the value into the step's `env:` (as the rest "
        f"of the action already does) or teach this helper to substitute it."
    )
    return script


# Stands in for `gh`. `issue view --jq` is executed FAITHFULLY — the expression
# under test is handed to the real jq against a fixture — because that
# expression is the thing that was wrong. `issue list` is short-circuited to a
# fixed number: locating the tracking issue is not what this pins, and making
# the fixture titles line up would add a moving part with no assertion on it.
_STUB_GH = '''#!/usr/bin/env python3
import os
import subprocess
import sys

argv = sys.argv[1:]


def record(line):
    with open(os.environ["STUB_CALLS"], "a", encoding="utf-8") as fh:
        fh.write(line + "\\n")


if argv[:2] == ["issue", "list"]:
    record("list")
    # Locating the tracking issue is normally not what this pins, so the number
    # is fixed. But WHICH title is open is load-bearing for the per-arm
    # namespacing tests, so when STUB_OPEN_TITLE is set the lookup becomes
    # title-exact — the real `--search ... in:title` plus `select(.title == ...)`
    # pair behaves that way, and modelling it is what lets a test show that one
    # arm's recovery cannot close another arm's episode.
    want = os.environ.get("STUB_OPEN_TITLE")
    if want is not None:
        search = argv[argv.index("--search") + 1] if "--search" in argv else ""
        asked = search.split('"')[1] if search.count('"') >= 2 else search
        print(os.environ.get("STUB_ISSUE_NUM", "") if asked == want else "")
        sys.exit(0)
    print(os.environ.get("STUB_ISSUE_NUM", ""))
    sys.exit(0)

if argv[:2] == ["issue", "close"]:
    record("close:" + argv[2])
    sys.exit(0)

if argv[:2] == ["issue", "view"]:
    record("view")
    expr = argv[argv.index("--jq") + 1]
    done = subprocess.run(
        ["jq", "-r", expr, os.environ["STUB_ISSUE_JSON"]],
        capture_output=True, text=True,
    )
    sys.stderr.write(done.stderr)
    sys.stdout.write(done.stdout)
    sys.exit(done.returncode)

if argv[:2] == ["issue", "comment"]:
    record("comment:" + argv[argv.index("--body") + 1])
    sys.exit(0)

if argv[:2] == ["issue", "create"]:
    record("create")
    sys.exit(0)

record("UNEXPECTED:" + " ".join(argv))
sys.exit(97)
'''


class _AlertHarness(unittest.TestCase):
    """Executes the shared blindness action's shell under bash with a stub `gh`.

    The script is pulled out of `action.yml` verbatim (never copied here) and run
    for real, because the defect under repair was a misread of what a `jq`
    expression SELECTS — so a test that reads the expression back is the same act
    that produced the bug.

    Every arm now runs this one body, so the guard is proven once. What is
    per-arm is the identity handed to it (title, marker slug), and that is what
    the subclasses vary.
    """

    @classmethod
    def setUpClass(cls):
        for tool in ("bash", "jq"):
            if shutil.which(tool) is None:
                raise AssertionError(
                    f"{tool!r} is required to EXECUTE the storm guard; it is present "
                    f"on ubuntu-latest, where this runs as a gate self-check. "
                    f"Skipping instead of failing would leave the guard unproven "
                    f"while reporting green — the exact disease this file pins."
                )

    def arms(self) -> dict[str, dict]:
        """`{job_id: {'title': ..., 'marker-slug': ...}}` from the shipped workflow.

        Read off the workflow rather than listed, so a FOURTH arm is covered by
        every test below the moment it is wired up. #394 added the third arm and
        the suite did not notice, which is the whole reason this card exists.
        """
        return {
            job: {"title": h["alert"]["title"], "marker-slug": h["alert"]["marker-slug"]}
            for job, h in _blindness_calls().items()
        }

    def marker(self, arm: dict, kind: str) -> str:
        """The marker the SCRIPT builds for this arm — read off the script."""
        found = re.search(r'marker="(<!--.*?-->)"', _action_alert_script())
        self.assertIsNotNone(found, "the action no longer builds an HTML dedup marker")
        return (found.group(1)
                .replace("${MARKER_SLUG}", arm["marker-slug"])
                .replace("${REASON_KIND}", kind))

    def run_script(
        self, *, arm: dict, kind: str = "version-mismatch", comments: list[str],
        body: str, mode: str = "alert", script: str | None = None,
        open_title: str | None = None,
    ):
        """Returns (stdout, calls) — `calls` is what the stub `gh` was asked to do,
        so assertions land on the branch TAKEN, never on a count alone (a count is
        also 0 when the script never ran)."""
        script = _action_alert_script() if script is None else script
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            gh = tmp / "gh"
            gh.write_text(_STUB_GH, encoding="utf-8")
            gh.chmod(0o755)
            issue = tmp / "issue.json"
            issue.write_text(
                json.dumps({"body": body, "comments": [{"body": c} for c in comments]}),
                encoding="utf-8",
            )
            calls = tmp / "calls.txt"
            calls.write_text("", encoding="utf-8")
            step = tmp / "step.sh"
            step.write_text(script, encoding="utf-8")

            env = dict(os.environ)
            # Every input the action declares, because the shipped step always
            # passes all of them (defaults are ''), and the body runs under
            # `set -u`.
            env.update(
                PATH=f"{tmp}{os.pathsep}{os.environ['PATH']}",
                GH_TOKEN="stub",
                MODE=mode,
                TITLE=arm["title"],
                MARKER_SLUG=arm["marker-slug"],
                ARM="The arm under test",
                REASON="baseline captured in bm25-only, run served hybrid",
                REASON_KIND=kind,
                STAKES="Nothing is guarded while this is open.",
                LEDE="",
                RECOVERY_NOTE="It is measuring again.",
                RUN_URL="https://example.invalid/run/1",
                STUB_ISSUE_NUM="397",
                STUB_ISSUE_JSON=str(issue),
                STUB_CALLS=str(calls),
            )
            if open_title is not None:
                env["STUB_OPEN_TITLE"] = open_title
            done = subprocess.run(
                ["bash", str(step)], env=env, capture_output=True, text=True, timeout=60,
            )
            self.assertEqual(
                0, done.returncode,
                f"the script itself failed:\nstdout={done.stdout}\nstderr={done.stderr}",
            )
            recorded = [ln for ln in calls.read_text(encoding="utf-8").splitlines() if ln]
            return done.stdout, recorded


class TestAcknowledgingAnAlertDoesNotReArmIt(_AlertHarness):
    """One alert kind, one comment, however many non-alert comments follow.

    Driven once per ARM rather than once per copy of the shell. Before the shell
    was factored, "both alerts carry the same guard" was a claim maintained by
    hand across two near-identical blocks, and the third arm — the required,
    merge-gating one — had no block at all to carry it.
    """

    # -- AC1 -----------------------------------------------------------------
    def test_a_non_alert_comment_does_not_re_arm_the_same_kind(self):
        """#397's exact sequence: alert(K), then a human, then alert(K) again.

        Asserted on the BRANCH TAKEN, not on a comment count — the count is also
        0 when the step never ran, and a guard proven by an absence is the same
        class of evidence as the bug.
        """
        for label, arm in self.arms().items():
            with self.subTest(arm=label):
                marker = self.marker(arm, "version-mismatch")
                out, calls = self.run_script(
                    arm=arm,
                    body="Tracking issue for recall-gate blindness.",
                    comments=[
                        f"{marker}\n\nThe memory recall gate could not produce a verdict.",
                        "Looking at this now — is it the embedder or the baseline?",
                    ],
                    kind="version-mismatch",
                )
                self.assertIn(
                    "already reports", out,
                    f"{label}: a human comment displaced the marker and the guard "
                    f"re-alerted. Acknowledging an alert must not re-arm it.\n{out}",
                )
                self.assertNotIn(
                    "Commented on tracking issue", out,
                    f"{label}: took the commenting branch\n{out}",
                )
                self.assertFalse(
                    [c for c in calls if c.startswith("comment:")],
                    f"{label}: posted a duplicate alert: {calls}",
                )

    # -- AC2 -----------------------------------------------------------------
    def test_a_genuinely_new_reason_kind_still_alerts(self):
        """Without this, "never comment twice" would satisfy AC1 and destroy the
        signal the alert exists to carry. A second, DIFFERENT way of going blind
        is news, and it must reach the issue even though a marker is already on
        it."""
        for label, arm in self.arms().items():
            with self.subTest(arm=label):
                out, calls = self.run_script(
                    arm=arm,
                    body="Tracking issue for recall-gate blindness.",
                    comments=[
                        f"{self.marker(arm, 'version-mismatch')}\n\nFirst kind.",
                        "Ack, on it.",
                    ],
                    kind="mode-mismatch",
                )
                self.assertIn(
                    "Commented on tracking issue", out,
                    f"{label}: a NEW reason kind was suppressed — the fix has "
                    f"traded a storm for silence.\n{out}",
                )
                posted = [c for c in calls if c.startswith("comment:")]
                self.assertEqual(1, len(posted), f"{label}: {calls}")
                self.assertIn(
                    self.marker(arm, "mode-mismatch"), posted[0],
                    f"{label}: the new comment does not carry its own kind marker, "
                    f"so the NEXT run cannot dedup against it.",
                )

    def test_the_issue_body_alone_still_suppresses(self):
        """The alert that OPENS the issue writes its marker into the body, not a
        comment. That case worked before and must keep working: an issue with
        zero comments is the first repeat's normal state."""
        for label, arm in self.arms().items():
            with self.subTest(arm=label):
                out, calls = self.run_script(
                    arm=arm,
                    body=f"{self.marker(arm, 'no-baseline')}\n\nOpened by the first alert.",
                    comments=[],
                    kind="no-baseline",
                )
                self.assertIn("already reports", out, f"{label}: {out}")
                self.assertFalse([c for c in calls if c.startswith("comment:")], label)

    # -- AC3: the harness is not vacuous ------------------------------------
    def test_the_harness_reproduces_the_defect_on_the_old_expression(self):
        """The positive control, and the reason AC1 above means anything.

        Swap only the `jq` window back to what `main` shipped —
        `(.comments | last | .body) // .body // ""` — leaving every other line of
        the step alone, and re-run the AC1 fixture. It must post the duplicate.
        If this ever goes quiet, the harness has stopped exercising the guard and
        AC1 is passing for some other reason.
        """
        old = """--jq '(.comments | last | .body) // .body // ""'"""
        script = _action_alert_script()
        mutated, n = re.subn(r"--jq '\(\[\.body\].*?'", old, script, flags=re.S)
        self.assertEqual(
            1, n,
            f"could not find the fixed `issue view --jq` window to mutate, so this "
            f"control proves nothing. Script:\n{script}",
        )
        self.assertNotEqual(script, mutated)
        for label, arm in self.arms().items():
            with self.subTest(arm=label):
                marker = self.marker(arm, "version-mismatch")
                out, calls = self.run_script(
                    arm=arm, script=mutated,
                    body="Tracking issue for recall-gate blindness.",
                    comments=[
                        f"{marker}\n\nThe memory recall gate could not produce a verdict.",
                        "Looking at this now — is it the embedder or the baseline?",
                    ],
                    kind="version-mismatch",
                )
                self.assertIn(
                    "Commented on tracking issue", out,
                    f"{label}: the old last-comment-only window did NOT duplicate on "
                    f"#397's own sequence. The harness is not driving the guard.\n{out}",
                )
                self.assertTrue([c for c in calls if c.startswith("comment:")], label)


class TestOneArmCannotSilenceOrCloseAnother(_AlertHarness):
    """AC2 + AC3, executed rather than asserted about the YAML.

    Two arms going blind for the SAME reason kind must produce two episodes, and
    one arm recovering must not clear the other's. Both properties are the reason
    a second copy of the shell would have needed a second marker namespace, and
    both are about what happens with identical inputs — so they are driven, not
    read off the file.
    """

    def _pair(self):
        arms = self.arms()
        self.assertGreaterEqual(len(arms), 2, f"need two arms to test collision: {arms}")
        (a_id, a), (b_id, b) = sorted(arms.items())[:2]
        return (a_id, a), (b_id, b)

    def test_the_same_reason_kind_on_two_arms_opens_two_episodes(self):
        """Worst case first: pretend both arms somehow landed on ONE issue, and
        show the kind-scoped marker still lets the second one speak. If the two
        arms shared a marker slug, arm B would read arm A's marker, take the
        `already reports` branch, and go silent on a genuinely separate outage."""
        (a_id, a), (b_id, b) = self._pair()
        kind = "no-baseline"
        out, calls = self.run_script(
            arm=b, kind=kind,
            body=f"{self.marker(a, kind)}\n\nOpened by {a_id}.",
            comments=[],
        )
        self.assertIn(
            "Commented on tracking issue", out,
            f"{b_id} was silenced by {a_id}'s marker for the same kind {kind!r}. The "
            f"marker namespaces have collided, and one arm's outage now hides the "
            f"other's — a new way to go silently blind.\n{out}",
        )
        posted = [c for c in calls if c.startswith("comment:")]
        self.assertEqual(1, len(posted), calls)
        self.assertIn(self.marker(b, kind), posted[0])

    def test_in_the_shipped_wiring_they_do_not_even_share_an_issue(self):
        """And the primary defence, one layer up: the titles differ, so the two
        arms' lookups return different issues and the case above cannot arise in
        production at all. Both layers, because the title is the thing a careless
        edit changes."""
        (a_id, a), (b_id, b) = self._pair()
        self.assertNotEqual(a["title"], b["title"], f"{a_id} vs {b_id}")
        out, calls = self.run_script(
            arm=b, kind="no-baseline", body="", comments=[],
            open_title=a["title"],
        )
        self.assertIn(
            "Opened tracking issue", out,
            f"{b_id} found {a_id}'s open issue and reported into it. The title lookup "
            f"is not arm-exact.\n{out}",
        )
        self.assertIn("create", calls, calls)

    def test_recovery_on_one_arm_does_not_close_the_others_episode(self):
        """AC3's negative half. A resolve that closes by relevance rather than by
        exact title would clear an episode nobody has recovered from — the failure
        direction that LOSES the alarm, and the one a green test is least likely to
        notice."""
        (a_id, a), (b_id, b) = self._pair()
        out, calls = self.run_script(
            arm=b, mode="resolve", body="", comments=[], open_title=a["title"],
        )
        self.assertIn(
            "nothing to clear", out,
            f"{b_id}'s recovery closed {a_id}'s still-open episode.\n{out}",
        )
        self.assertFalse([c for c in calls if c.startswith("close:")], calls)

    def test_and_recovery_on_the_right_arm_does_close_it(self):
        """The positive half — without it, the test above passes on a resolve that
        can never close anything, which is the same blindness wearing a green
        check."""
        (a_id, a), _ = self._pair()
        out, calls = self.run_script(
            arm=a, mode="resolve", body="", comments=[], open_title=a["title"],
        )
        self.assertIn("Closed episode #397", out, f"{a_id}: {out}")
        self.assertIn("close:397", calls, calls)
def _step_if(step_name: str) -> str:
    """The `if:` expression of the named step, AS SHIPPED, flattened to one line.

    Text-level and extracted, for the same reason `_job_blocks` is: the point of
    this helper is to read what the file actually says. Handles both the
    single-line form (`if: a && b`) and the folded form (`if: >-` followed by an
    indented block), because a step that changes between the two must not thereby
    escape the check.
    """
    text = WORKFLOW.read_text(encoding="utf-8")
    needle = f"- name: {step_name}\n"
    start = text.find(needle)
    assert start != -1, (
        f"step {step_name!r} not found in memory-bench.yml — if it was renamed, "
        "this test must be pointed at the new name, not deleted"
    )
    # There are now near-identical alert steps in three jobs, so a duplicated
    # name would make this helper silently return the FIRST one and quietly
    # repoint every pin that uses it at the wrong arm — relaxing the check to a
    # single arm while still reading green. Loud instead.
    assert text.count(needle) == 1, (
        f"step name {step_name!r} appears {text.count(needle)} times in "
        f"memory-bench.yml. This helper resolves by name across the whole file, so "
        f"duplicate names make every `if:` pin read whichever arm comes first. Give "
        f"each arm's step a distinct name."
    )
    body = text[start:]
    m = re.search(r"^\s*if:[ \t]*(.*)$", body, re.M)
    assert m, f"step {step_name!r} has no `if:`"
    first = m.group(1).strip()
    if first not in (">-", ">", "|-", "|"):
        return " ".join(first.split())
    # Folded block: take the indented lines that follow.
    rest = body[m.end():].splitlines()
    indent = None
    parts = []
    for ln in rest[1:] if rest and not rest[0].strip() else rest:
        if not ln.strip():
            break
        cur = len(ln) - len(ln.lstrip())
        if indent is None:
            indent = cur
        elif cur < indent:
            break
        parts.append(ln.strip())
    return " ".join(" ".join(parts).split())


def _event_clauses(expr: str) -> set[str]:
    """The parts of an `if:` that scope it to an EVENT, normalised.

    Splits on top-level `&&` (parenthesised disjuncts stay whole) and keeps the
    conjuncts that mention `github.event_name` or `inputs.expect_commit`. The rc
    predicates are deliberately excluded: alert and resolve are SUPPOSED to
    differ there ('2' vs '0'). What must match is which runs they act on.
    """
    conjuncts, depth, cur = [], 0, ""
    i = 0
    while i < len(expr):
        c = expr[i]
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
        if depth == 0 and expr.startswith("&&", i):
            conjuncts.append(cur)
            cur = ""
            i += 2
            continue
        cur += c
        i += 1
    conjuncts.append(cur)
    return {
        " ".join(c.split())
        for c in conjuncts
        if "github.event_name" in c or "inputs.expect_commit" in c
    }


class TestBlindnessAlertAndResolveAgreeOnEvents(unittest.TestCase):
    """An alert that can be RAISED on more events than it can be RESOLVED on
    bounds an episode by trigger cadence instead of by recovery.

    The recall arm shipped exactly that: the alert had no event clause, the
    resolve carried `github.event_name == 'push'`. A blind episode detected by
    the Sunday schedule stayed open until somebody pushed to main.

    It is load-bearing rather than untidy because the dedup key suppresses a
    same-kind alert while the issue is OPEN — so a stale-open issue MUTES its own
    re-alerts. The widened dedup is right only while the issue closes on
    recovery.

    These tests EXTRACT both conditions from the shipped workflow rather than
    restating them. A restated rule keeps passing after the real one is deleted,
    which is the failure mode this class exists to prevent.
    """

    ALERT = "Alert — the prod recall canary is blind (INCONCLUSIVE)"
    RESOLVE = "Resolve — the prod recall canary is measuring again"
    ADV_ALERT = "Alert — advisory arm measured nothing (INCONCLUSIVE)"
    ADV_RESOLVE = "Resolve advisory blindness alert (arm is measuring again)"

    def test_every_arms_alert_and_resolve_act_on_the_same_events(self):
        """Generalised from the two named pairs to EVERY arm.

        The named-constant version could only ever check the pairs somebody
        remembered to add, and #394 is the proof that that is not a safe
        assumption: it created a third arm and nothing in this suite noticed it had
        no alert at all, let alone a matching resolve. The pairs are now discovered
        from the shipped workflow, so a fourth arm is under this pin the moment it
        is wired up.
        """
        pairs = _blindness_calls()
        self.assertGreaterEqual(len(pairs), 3, f"expected all three arms, got {list(pairs)}")
        for job_id, halves in sorted(pairs.items()):
            with self.subTest(job=job_id):
                alert = _event_clauses(halves["alert"]["if"])
                resolve = _event_clauses(halves["resolve"]["if"])
                self.assertEqual(
                    alert, resolve,
                    f"job {job_id!r}: its blindness alert and its resolve disagree about "
                    f"which events they act on. Whatever can raise the alert must be able "
                    f"to clear it, or the tracking issue outlives the blindness and — via "
                    f"the dedup key — silences its own re-alerts.\n"
                    f"  alert   : {sorted(alert)}\n  resolve : {sorted(resolve)}",
                )

    def test_the_required_arm_never_alerts_from_a_pull_request(self):
        """The branch arm's own scoping, and it is a correctness claim.

        A PR branch's measurement is not authoritative about the repo's state, so a
        PR that breaks only its own baseline must not open a repo-level episode, and
        a PR that fixes its own baseline must not CLOSE one main is still blind on —
        the second direction loses the alarm. And a fork PR gets a read-only
        GITHUB_TOKEN, so alerting there would fail this REQUIRED check for an author
        who cannot clear it (the #342 rule).
        """
        for step in (
            "Alert — the required recall gate is blind (INCONCLUSIVE)",
            "Resolve — the required recall gate is measuring again",
        ):
            with self.subTest(step=step):
                self.assertIn(
                    "github.event_name != 'pull_request'", _step_if(step),
                    f"{step!r} lost its pull_request exclusion. On a fork PR this step "
                    f"cannot write issues, so it would redden the merge gate for an "
                    f"author with no way to clear it.",
                )

    def test_the_branch_pair_carries_no_drill_guard(self):
        """The asymmetry with the prod pair is deliberate and must not be
        'harmonised'.

        `inputs.expect_commit` names a SERVING version for the prod canary to
        confirm; a dispatch that sets it is an operator drill, which is neither an
        incident nor a recovery. The branch arm builds its own server from the
        checkout and never reads that input, so the same clause here would gate on a
        value the job cannot observe — silencing real blindness on any dispatch that
        happened to set an unrelated input. That is the direction that loses the
        alarm, which is why it is pinned rather than left to taste.
        """
        for step in (
            "Alert — the required recall gate is blind (INCONCLUSIVE)",
            "Resolve — the required recall gate is measuring again",
        ):
            with self.subTest(step=step):
                self.assertNotIn(
                    "expect_commit", _step_if(step),
                    f"{step!r} gained the prod arm's drill guard. The branch arm never "
                    f"reads `expect_commit`, so this suppresses real blindness on "
                    f"dispatches that set it for the OTHER arm.",
                )

    def test_neither_half_of_the_recall_arm_is_push_scoped(self):
        """The specific regression: this job is the prod canary and never runs on
        pull_request, so every run measured PROD. Scoping either half to `push`
        discards a schedule's verdict about the very thing it just measured."""
        for step in (self.ALERT, self.RESOLVE):
            with self.subTest(step=step):
                self.assertNotIn(
                    "github.event_name == 'push'", _step_if(step),
                    f"{step!r} is push-scoped again. A schedule measures prod just as "
                    "a push does; bounding the episode by push cadence is the defect "
                    "this test pins.",
                )

    def test_a_drill_can_neither_raise_nor_clear_the_alert(self):
        """`expect_commit` marks an operator drill. A drill is not an incident —
        and by the same token not a recovery, or a dispatch pinned to a commit
        prod happens to be serving could close an episode it could not open."""
        for step in (self.ALERT, self.RESOLVE):
            with self.subTest(step=step):
                self.assertIn(
                    "github.event_name != 'workflow_dispatch' || inputs.expect_commit == ''",
                    _step_if(step),
                    f"{step!r} lost the drill guard. Losing it on the alert charges a "
                    "false page for every drill; losing it on the resolve lets a drill "
                    "silently close a real episode.",
                )

    def test_advisory_arm_alert_and_resolve_also_agree(self):
        """AC4: the second copy of this guard must not drift from the first.

        The advisory arm is intentionally scoped differently from the recall arm —
        it has no version pin and therefore no drill mode, so it carries no event
        clauses at all. What it may NOT do is disagree with itself, which is the
        property under test here and the one that actually bit.
        """
        alert = _event_clauses(_step_if(self.ADV_ALERT))
        resolve = _event_clauses(_step_if(self.ADV_RESOLVE))
        self.assertEqual(
            alert, resolve,
            "The advisory arm's alert and resolve disagree about which events they "
            f"act on.\n  alert   : {sorted(alert)}\n  resolve : {sorted(resolve)}",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
