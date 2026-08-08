#!/usr/bin/env python3
"""The merge queue's preconditions, pinned so they cannot rot back.

A GitHub merge queue runs the *required* status checks on the `merge_group`
event, against the batched tree, and waits for the same context strings it waits
for on a pull request. Three properties have to hold together, and each one is
invisible in review:

  1. every workflow that produces a required context must list `merge_group:`.
     A workflow that does not simply produces nothing there — so the queue entry
     waits for a check that is never requested. With `enforce_admins: true` and
     six required contexts that is not a slow merge, it is a repository that
     cannot merge anything, with no override available to anyone including the
     owner. This is the whole reason the trigger landed as its own change,
     *before* the queue was turned on.

  2. nothing that touches production may run there. A queue branch is a
     candidate, not `main`; it is not deployed anywhere. A prod-judging job
     recruited onto this event answers a question nobody asked, contends for the
     one shared server, and can open incident issues about a tree that never
     shipped.

  3. the diff-based gates must widen to the batch. `HEAD~1` is the right base for
     a squashed push to `main` and wrong for a merge group, which is base plus
     one commit per queued PR: on a batch of three it scores the last PR and lets
     the other two in unmeasured. That failure is silent and green.

Properties 2 and 3 share one root cause worth stating plainly, because it is the
reason this file exists rather than a line in a review checklist: **adding a
trigger changes the meaning of every `!= 'pull_request'` and every `else` branch
already in the tree.** Those were written when the event list was shorter, they
were correct then, and they quietly recruit the new event the moment it appears.
Grep for the negations before adding an event, not after.

Stdlib only, text-level, ~instant. Text-level on purpose, the same reasoning
`test_gate_blindness.py` gives: what GitHub matches is text, and a structural
parse normalises away the very thing under test.
"""

from __future__ import annotations

import re
import sys
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
WORKFLOWS = REPO / ".github" / "workflows"

# The literal contexts `main`'s branch protection requires, with
# enforce_admins: true. Verified live 2026-08-08:
#   gh api repos/entire-vc/evc-mesh/branches/main/protection \
#     --jq .required_status_checks.contexts
# Hard-coded rather than fetched: this must pass offline, and a check that
# silently skips when the network is down is the blind gate it is guarding
# against. If protection changes, this list changes in the same PR.
REQUIRED_CONTEXTS = [
    "Lint",
    "Test",
    "Build",
    "Authed E2E",
    "Go coverage ≥80% (affected-set)",
    "Memory recall gate",
]

# Deploying from a queue branch would ship a candidate that no one has merged.
DEPLOY_WORKFLOWS = ["deploy-backend.yml", "deploy-frontend.yml"]


def _workflow_files() -> dict[str, str]:
    return {p.name: p.read_text(encoding="utf-8") for p in sorted(WORKFLOWS.glob("*.yml"))}


def _on_block(text: str) -> str:
    """The `on:` mapping, up to the next top-level key."""
    m = re.search(r"^on:\s*$", text, re.M)
    if not m:
        return ""
    rest = text[m.end():]
    nxt = re.search(r"^[a-zA-Z_][a-zA-Z0-9_-]*:", rest, re.M)
    return rest[: nxt.start()] if nxt else rest


def _jobs(text: str) -> dict[str, str]:
    """`{job_id: raw yaml}` for one workflow."""
    parts = text.split("\njobs:\n", 1)
    if len(parts) < 2:
        return {}
    body = parts[1]
    starts = [(m.start(), m.group(1)) for m in re.finditer(r"^  ([a-zA-Z0-9_-]+):$", body, re.M)]
    out = {}
    for i, (pos, job_id) in enumerate(starts):
        end = starts[i + 1][0] if i + 1 < len(starts) else len(body)
        out[job_id] = body[pos:end]
    return out


def _job_name(block: str) -> str | None:
    m = re.search(r'^    name: "?(.+?)"?\s*$', block, re.M)
    return m.group(1).strip() if m else None


def _header(block: str) -> str:
    """Everything before `steps:` — where `if:`/`concurrency:` live."""
    return block.split("    steps:", 1)[0]


MERGE_GROUP_RE = re.compile(r"^  merge_group:\s*$", re.M)
PULL_REQUEST_RE = re.compile(r"^  pull_request:\s*$", re.M)


def _producers() -> dict[str, tuple[str, str]]:
    """`{required context: (workflow filename, job block)}`.

    Scoped to workflows that run on `pull_request`, and that scoping is
    load-bearing rather than tidiness. Job NAMES are not unique across this repo:
    `deploy-backend.yml` also has jobs named exactly `Lint` and `Test`, so an
    unscoped search finds two producers for those contexts and picks whichever
    sorted last. Branch protection is unambiguous only because that workflow is
    push/dispatch-only and never reports on a PR — which is precisely the
    property being relied on here, so it is the property to filter by.

    It is also the correct rule on its own terms: the set of workflows that must
    gain `merge_group` is exactly the set that gates a PR today. A workflow that
    does not run on `pull_request` produces no context the queue is waiting for.
    """
    found: dict[str, tuple[str, str]] = {}
    for fname, text in _workflow_files().items():
        if not PULL_REQUEST_RE.search(_on_block(text)):
            continue
        for _job_id, block in _jobs(text).items():
            name = _job_name(block)
            if name in REQUIRED_CONTEXTS:
                found[name] = (fname, block)
    return found


class TestEveryRequiredContextIsProducedOnMergeGroup(unittest.TestCase):
    def test_all_six_producers_are_discoverable(self):
        """Negative control for every other test in this file.

        Each test below iterates the producers this finds. If discovery breaks —
        a `name:` reformatted, a job moved — the iteration goes empty and every
        assertion passes vacuously, which reads exactly like a healthy run. So
        assert the population first, by count and by name.
        """
        found = _producers()
        self.assertEqual(
            sorted(REQUIRED_CONTEXTS), sorted(found),
            f"could not locate a job producing every required context. "
            f"Found {sorted(found)}; expected {sorted(REQUIRED_CONTEXTS)}. "
            f"Either a required check is produced by nobody (every PR is BLOCKED "
            f"forever — see evc-mesh#320 / #394), or this test's discovery broke "
            f"and the merge-queue assertions below are now vacuous.",
        )

    def test_each_producing_workflow_listens_on_merge_group(self):
        for context, (fname, _block) in sorted(_producers().items()):
            with self.subTest(context=context, workflow=fname):
                on = _on_block((WORKFLOWS / fname).read_text(encoding="utf-8"))
                self.assertRegex(
                    on, MERGE_GROUP_RE,
                    f"{fname} produces the required context {context!r} but does not "
                    f"trigger on `merge_group`. A merge queue waits for that context "
                    f"on that event; this workflow would never request it, so every "
                    f"queue entry waits forever and — with enforce_admins: true — "
                    f"nobody can override. Do not enable the queue until this is "
                    f"green.",
                )


class TestProductionIsUnreachableFromAQueueBranch(unittest.TestCase):
    def test_no_deploy_workflow_listens_on_merge_group(self):
        for fname in DEPLOY_WORKFLOWS:
            with self.subTest(workflow=fname):
                path = WORKFLOWS / fname
                self.assertTrue(path.exists(), f"{fname} is gone — update DEPLOY_WORKFLOWS")
                self.assertNotRegex(
                    _on_block(path.read_text(encoding="utf-8")), MERGE_GROUP_RE,
                    f"{fname} would deploy from a merge-queue branch. A queue branch "
                    f"is a candidate that may still be ejected; only `main` ships.",
                )

    def test_the_prod_canary_does_not_run_on_merge_group(self):
        """A job reaching the one live server must not be recruited by a new event.

        It used to say `if: github.event_name != 'pull_request'`, which was true
        of exactly the events that existed when it was written. Adding
        `merge_group:` to `on:` would have enrolled it silently — the negation
        adopts every event added after it. Now an allow-list, so a future trigger
        is opted IN deliberately or not at all.
        """
        for fname, text in _workflow_files().items():
            on = _on_block(text)
            if not MERGE_GROUP_RE.search(on):
                continue
            for job_id, block in _jobs(text).items():
                if "secrets.MESH_API_URL" not in block:
                    continue
                header = _header(block)
                with self.subTest(workflow=fname, job=job_id):
                    self.assertNotIn(
                        "github.event_name != 'pull_request'", header,
                        f"{fname}:{job_id} targets production and gates itself with a "
                        f"NEGATION of pull_request, in a workflow that now also runs on "
                        f"merge_group — so it would judge production on every merge "
                        f"group. Spell the guard as an allow-list of the events on "
                        f"which the job is meaningful.",
                    )
                    self.assertRegex(
                        header, r"github\.event_name\s*==\s*'(push|schedule|workflow_dispatch)'",
                        f"{fname}:{job_id} targets production but its `if:` is not an "
                        f"allow-list of events; it may run on merge_group.",
                    )


class TestDiffBasedGatesWidenToTheWholeBatch(unittest.TestCase):
    """`HEAD~1` on a merge group measures the last PR and passes the rest."""

    CI = WORKFLOWS / "ci.yml"

    def test_coverage_gate_resolves_base_from_the_merge_group_payload(self):
        text = self.CI.read_text(encoding="utf-8")
        self.assertIn(
            "github.event.merge_group.base_sha", text,
            "ci.yml runs on merge_group but never reads "
            "`github.event.merge_group.base_sha`, so its diff base falls through to "
            "the push default `HEAD~1`. A queue branch is the base plus one commit "
            "per queued PR: on a batch of three that scores only the last one and "
            "the other two enter main unmeasured — green, and wrong.",
        )

    def test_the_threshold_step_does_not_re_derive_the_base(self):
        """One fact, one expression.

        The base used to be computed twice from two copies of the same
        conditional. Two encodings of one fact drift, and the direction that
        drift takes here is the dangerous one: a narrower base means fewer
        changed lines, which reads as a pass.
        """
        text = self.CI.read_text(encoding="utf-8")
        self.assertEqual(
            1, len(re.findall(r"github\.event\.pull_request\.base\.sha", text)),
            "ci.yml derives the PR base SHA in more than one place. Resolve it once "
            "and republish it as a step output; a second copy is a silent-drift "
            "surface on a gate whose failure mode is a false green.",
        )

    def test_head_tilde_one_is_not_the_fallback_for_an_unknown_event(self):
        """`HEAD~1` must be reached by naming `push`, never by `else`."""
        text = self.CI.read_text(encoding="utf-8")
        self.assertNotRegex(
            text, r"github\.event_name\s*==\s*'pull_request'[^\n]*\|\|\s*'HEAD~1'",
            "the diff base still falls back to HEAD~1 for any event that is not a "
            "pull request. That branch is what silently accepted merge_group.",
        )


if __name__ == "__main__":
    print(f"merge-queue preconditions — repo: {REPO}", file=sys.stderr)
    unittest.main(verbosity=2)
