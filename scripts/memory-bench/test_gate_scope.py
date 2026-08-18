#!/usr/bin/env python3
"""Self-check for the scope predicate of the recall gate (scope_relevant.sh).

    python scripts/memory-bench/test_gate_scope.py

Background — the defect this pins
---------------------------------
`Does this change touch memory?` used to short-circuit to relevant=true for any
event that was not `pull_request`. On `merge_group` that meant every merge group
paid the full branch bench — measured 29-42 min — whatever it contained. The
ruleset builds groups of one (`min_entries_to_merge=1`), so there was no batch to
amortise the cost over, and an ejected entry rebuilt into a fresh queue branch
and paid it again. On 2026-08-18 that held the queue for five hours over four
PRs (#578/#582/#588/#589) which touch no memory path between them — each already
skipped in ~45s by this same predicate on its own PR run.

The invariant this file defends, in BOTH directions:

  * a diff containing a memory path is gated, on either event (no new way to
    reach main unmeasured — the failure of #347);
  * a diff containing none is skipped, on either event;
  * anything that cannot be established — missing shas, unreadable diff, empty
    path list, an event with no two-sided diff — is gated. Unknown must never
    read as clear.

The last group is why every negative case below asserts `true`, not an error:
this step decides scope, never the verdict, so its only safe answer when blind
is "measure everything".
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "scope_relevant.sh"

MEMORY_PATHS = "\n".join([
    "internal/service/memory_service.go",
    "internal/repository/postgres/memory_repo.go",
    "internal/mcp/tools.go",
    "scripts/memory-bench/",
])


def _git(repo: Path, *args: str) -> str:
    return subprocess.run(
        ["git", *args], cwd=repo, check=True,
        capture_output=True, text=True,
    ).stdout.strip()


def _repo_with_change(files: list[str]) -> tuple[Path, str, str, tempfile.TemporaryDirectory]:
    """A throwaway repo with a base commit and one commit touching `files`."""
    tmp = tempfile.TemporaryDirectory()
    repo = Path(tmp.name)
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "config", "user.email", "gate@example.invalid")
    _git(repo, "config", "user.name", "gate")
    (repo / "README.md").write_text("base\n")
    _git(repo, "add", "-A")
    _git(repo, "commit", "-qm", "base")
    base = _git(repo, "rev-parse", "HEAD")
    for f in files:
        p = repo / f
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text("touched\n")
    _git(repo, "add", "-A")
    _git(repo, "commit", "-qm", "change")
    head = _git(repo, "rev-parse", "HEAD")
    return repo, base, head, tmp


def run_scope(repo: Path, **env_overrides: str) -> tuple[int, str, str]:
    """Run the predicate; returns (rc, stdout, relevant-as-written-to-GITHUB_OUTPUT)."""
    out_file = repo / "_gh_output"
    out_file.write_text("")
    env = {
        **os.environ,
        "MEMORY_PATHS": MEMORY_PATHS,
        "GITHUB_OUTPUT": str(out_file),
    }
    env.update(env_overrides)
    proc = subprocess.run(
        ["bash", str(SCRIPT)], cwd=repo, env=env,
        capture_output=True, text=True,
    )
    written = ""
    for line in out_file.read_text().splitlines():
        if line.startswith("relevant="):
            written = line.split("=", 1)[1]
    return proc.returncode, proc.stdout, written


class ScopeNarrowsOnBothDiffableEvents(unittest.TestCase):
    """The positive half: the predicate must be able to answer BOTH ways."""

    def test_memory_path_is_gated_on_pull_request(self):
        repo, base, head, tmp = _repo_with_change(["internal/mcp/tools.go"])
        with tmp:
            rc, _, rel = run_scope(repo, EVENT_NAME="pull_request", BASE_SHA=base, HEAD_SHA=head)
        self.assertEqual(rc, 0)
        self.assertEqual(rel, "true")

    def test_memory_path_is_gated_on_merge_group(self):
        """The #347 direction. Narrowing must not open a way to main unmeasured."""
        repo, base, head, tmp = _repo_with_change(["internal/mcp/tools.go"])
        with tmp:
            rc, _, rel = run_scope(repo, EVENT_NAME="merge_group", BASE_SHA=base, HEAD_SHA=head)
        self.assertEqual(rc, 0)
        self.assertEqual(rel, "true")

    def test_unrelated_diff_is_skipped_on_pull_request(self):
        repo, base, head, tmp = _repo_with_change(["Makefile"])
        with tmp:
            rc, _, rel = run_scope(repo, EVENT_NAME="pull_request", BASE_SHA=base, HEAD_SHA=head)
        self.assertEqual(rc, 0)
        self.assertEqual(rel, "false")

    def test_unrelated_diff_is_skipped_on_merge_group(self):
        """The whole point of the change: #589 (Makefile) must not pay 40 minutes."""
        repo, base, head, tmp = _repo_with_change(["Makefile"])
        with tmp:
            rc, _, rel = run_scope(repo, EVENT_NAME="merge_group", BASE_SHA=base, HEAD_SHA=head)
        self.assertEqual(rc, 0)
        self.assertEqual(rel, "false")

    def test_the_four_stalled_prs_would_have_been_skipped(self):
        """Regression anchor for 2026-08-18. Real file lists from #578/#582/#588/#589."""
        for label, files in {
            "#582": [".github/workflows/ci.yml"],
            "#589": ["Makefile"],
            "#588": ["internal/repository/postgres/agent_repo.go"],
            "#578": ["internal/service/agent_service.go", "internal/domain/agent.go",
                     "migrations/20260817092_agents_api_key_sha256.sql"],
        }.items():
            with self.subTest(pr=label):
                repo, base, head, tmp = _repo_with_change(files)
                with tmp:
                    _, _, rel = run_scope(repo, EVENT_NAME="merge_group",
                                          BASE_SHA=base, HEAD_SHA=head)
                self.assertEqual(rel, "false", f"{label} should not pay the bench")

    def test_a_batch_mixing_memory_and_unrelated_files_is_gated(self):
        """A real merge group is several PRs; one memory path in it gates the batch."""
        repo, base, head, tmp = _repo_with_change(
            ["Makefile", "internal/repository/postgres/memory_repo.go"]
        )
        with tmp:
            _, _, rel = run_scope(repo, EVENT_NAME="merge_group", BASE_SHA=base, HEAD_SHA=head)
        self.assertEqual(rel, "true")

    def test_prefix_paths_match_their_directory(self):
        """`scripts/memory-bench/` is a prefix, not a file."""
        repo, base, head, tmp = _repo_with_change(["scripts/memory-bench/run_ci.py"])
        with tmp:
            _, _, rel = run_scope(repo, EVENT_NAME="merge_group", BASE_SHA=base, HEAD_SHA=head)
        self.assertEqual(rel, "true")


class UnknownReadsAsGated(unittest.TestCase):
    """The negative half: every blind path must answer `true`, never `false`."""

    def test_missing_base_sha_gates(self):
        repo, _, head, tmp = _repo_with_change(["Makefile"])
        with tmp:
            rc, out, rel = run_scope(repo, EVENT_NAME="merge_group", BASE_SHA="", HEAD_SHA=head)
        self.assertEqual(rc, 0)
        self.assertEqual(rel, "true")
        self.assertIn("cannot establish scope", out)

    def test_missing_head_sha_gates(self):
        repo, base, _, tmp = _repo_with_change(["Makefile"])
        with tmp:
            _, _, rel = run_scope(repo, EVENT_NAME="pull_request", BASE_SHA=base, HEAD_SHA="")
        self.assertEqual(rel, "true")

    def test_unresolvable_sha_gates(self):
        """A shallow clone, or a base that never reached this checkout."""
        repo, _, head, tmp = _repo_with_change(["Makefile"])
        with tmp:
            rc, out, rel = run_scope(
                repo, EVENT_NAME="merge_group",
                BASE_SHA="0" * 40, HEAD_SHA=head,
            )
        self.assertEqual(rc, 0, "a failed diff must not take the job down with it")
        self.assertEqual(rel, "true")
        self.assertIn("cannot establish scope", out)

    def test_empty_memory_paths_gates(self):
        """The list not reaching the step must not read as `nothing is relevant`."""
        repo, base, head, tmp = _repo_with_change(["internal/mcp/tools.go"])
        with tmp:
            _, out, rel = run_scope(
                repo, EVENT_NAME="merge_group",
                BASE_SHA=base, HEAD_SHA=head, MEMORY_PATHS="",
            )
        self.assertEqual(rel, "true")
        self.assertIn("MEMORY_PATHS is empty", out)

    def test_push_and_schedule_keep_the_short_circuit(self):
        repo, base, head, tmp = _repo_with_change(["Makefile"])
        with tmp:
            for event in ("push", "schedule", "workflow_dispatch", ""):
                with self.subTest(event=event or "<unset>"):
                    _, _, rel = run_scope(repo, EVENT_NAME=event, BASE_SHA=base, HEAD_SHA=head)
                    self.assertEqual(rel, "true")


class WorkflowActuallyCallsIt(unittest.TestCase):
    """A predicate the workflow does not call is a predicate that gates nothing.

    Pins the wiring rather than the body: both arms must invoke the script and
    must feed it the SHAs. Checked by parsing the YAML, not by grepping text —
    a step renamed or re-indented is still wired, a step whose `run:` lost the
    call is not, and only one of those should go red.
    """

    def setUp(self):
        try:
            import yaml  # noqa: F401
        except ImportError:
            self.skipTest("PyYAML not installed")

    def test_both_arms_call_the_script_with_shas(self):
        import yaml
        wf = yaml.safe_load(
            (Path(__file__).resolve().parents[2] / ".github/workflows/memory-bench.yml").read_text()
        )
        found = 0
        for job_id, job in wf["jobs"].items():
            for step in job.get("steps", []):
                if step.get("id") != "scope":
                    continue
                found += 1
                self.assertIn(
                    "scope_relevant.sh", step.get("run", ""),
                    f"job {job_id}: scope step no longer calls the predicate",
                )
                env = step.get("env", {})
                for key in ("EVENT_NAME", "BASE_SHA", "HEAD_SHA"):
                    self.assertIn(key, env, f"job {job_id}: scope step does not pass {key}")
                for side, key in (("base", "BASE_SHA"), ("head", "HEAD_SHA")):
                    self.assertIn(
                        f"merge_group.{side}_sha", env[key],
                        f"job {job_id}: {key} does not read the merge_group payload — "
                        "merge groups would go back to paying the full bench blind",
                    )
        self.assertEqual(found, 2, "expected exactly two scope steps (branch arm + prod arm)")


if __name__ == "__main__":
    unittest.main(verbosity=2)
