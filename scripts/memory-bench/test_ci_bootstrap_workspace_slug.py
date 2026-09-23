#!/usr/bin/env python3
"""Self-check for ci_bootstrap.py's BENCH_WORKSPACE_SLUG propagation.

    python scripts/memory-bench/test_ci_bootstrap_workspace_slug.py

Same convention as the sibling self-checks: stdlib `unittest`, no Mesh, no
network. `ci_bootstrap._req` is the harness's only I/O boundary — mocked here
rather than any transport layer underneath it.

Background (#1974ef80): TWO independent guards blocked the branch/ephemeral
arm, discovered one at a time from live CI logs — fixing only the first still
left the gate measuring zero questions.

1. `assert_bench_workspace()` (client-side, mesh_client_stdio.py) refuses to
   run unless `MESH_AGENT_KEY` resolves to the workspace named by
   `BENCH_WORKSPACE_SLUG` (default "lme-bench"). The branch arm never gets
   that workspace — it registers the first user against a fresh,
   empty-per-job database, and that registration auto-creates a workspace
   slugged `ws-<uuid8>`, a different random value on every run. Nothing set
   `BENCH_WORKSPACE_SLUG`, so the guard refused every single time — 24/24
   questions, every run, for 16 days — and every run still went GREEN,
   because "the gate found nothing" and "the gate could not look" produce
   the identical rc=2 INCONCLUSIVE the workflow treats as advisory-only.

2. `enforceReservedTags` (server-side, internal/service/memory_service.go)
   rejects the fixture tag "lme-bench" (mesh_client_stdio.SHARED_TAG's
   default, applied to every `remember` call) from any workspace not
   flagged `is_bench=true` in the database. That flag is backfilled by a
   migration for exactly ONE prod workspace UUID and has deliberately no API
   to grant it (domain.Workspace.IsBench's own comment) — so the branch
   arm's throwaway workspace can NEVER earn it. Fixing guard 1 alone
   surfaced this immediately: the workspace guard passed, then every single
   `remember` call 400'd with "tag `lme-bench` is reserved for the
   benchmark workspace" — same 24/24-questions-measured-zero shape, new
   cause, live pipeline #4283 (2026-09-22, before this file's second fix).

The fix makes ci_bootstrap.py write BOTH the workspace slug it actually
observed (guard 1) AND a same-run-unique, provably-non-reserved fixture tag
override (guard 2) — telling each guard the truth about this run instead of
comparing against a hardcoded assumption that could never hold for a
throwaway workspace.

The properties pinned here:

  * BENCH_WORKSPACE_SLUG in the env file always matches the slug embedded in
    the MESH_AGENT_KEY this same run minted — by construction, not by
    coincidence — for both the auto-created-workspace path and the
    explicit-create path.
  * BENCH_SHARED_TAG in the env file is written too, and is never the
    literal "lme-bench" — the one value guaranteed to fail guard 2.
  * assert_bench_workspace() actually accepts what this script writes: this
    is checked by running the REAL guard against the REAL env file this
    script produced, not by asserting equality on strings and hoping the
    guard agrees.
  * mesh_client_stdio.SHARED_TAG actually picks up BENCH_SHARED_TAG in a
    fresh process, same process-boundary reasoning as the slug guard.
  * A workspace response with no `slug` fails the bootstrap closed, loudly,
    rather than silently omitting either variable and leaving both guards to
    compare against their stale defaults again.
  * RED CONTROL (guard 1): pinning BENCH_WORKSPACE_SLUG to the OLD hardcoded
    value ("lme-bench") against a freshly-minted ephemeral-workspace key is
    exactly last run's failure and must still raise.
  * RED CONTROL (guard 2): SHARED_TAG with no override is still the literal
    "lme-bench" — pinning this stops a future edit from quietly dropping the
    override and believing guard 1's fix was the whole story, same mistake
    this file's own history just made once already.
"""

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(__file__))

import ci_bootstrap  # noqa: E402
import mesh_client_stdio  # noqa: E402


def _assert_bench_workspace_in_a_fresh_process(env_overrides):
    """Runs the REAL assert_bench_workspace() in a brand-new interpreter with
    exactly `env_overrides` layered onto the current environment.

    Not a shortcut for `mesh_client_stdio.assert_bench_workspace()` in-process:
    `BENCH_WORKSPACE_SLUG` is a module-level constant resolved from the
    environment once, at import time (mesh_client_stdio.py:76) — correct for
    the real pipeline, where ci_bootstrap.py and the step that later imports
    this module are two separate `python3 …` invocations sharing a sourced env
    file, but wrong for a long-lived test process that imported the module
    once, before any test's env-patching ran. A subprocess reproduces the
    real pipeline's process boundary instead of asserting against a constant
    this test process itself made stale.

    Returns (returncode, stderr) — never raises for a non-zero exit, so
    callers can assert on the failure shape same as production actually sees.
    """
    env = dict(os.environ)
    env.update(env_overrides)
    proc = subprocess.run(
        [sys.executable, "-c", "import mesh_client_stdio as m; m.assert_bench_workspace()"],
        cwd=os.path.dirname(__file__),
        env=env,
        capture_output=True,
        text=True,
        timeout=30,
    )
    return proc.returncode, proc.stderr


def _shared_tag_in_a_fresh_process(env_overrides):
    """Same process-boundary reasoning as `_assert_bench_workspace_in_a_fresh_process`,
    for the OTHER module-level constant this fix touches: SHARED_TAG (also resolved
    from the environment once, at import time). Prints the value so the caller can
    assert on it without needing IPC beyond stdout."""
    env = dict(os.environ)
    env.update(env_overrides)
    proc = subprocess.run(
        [sys.executable, "-c", "import mesh_client_stdio as m; print(m.SHARED_TAG, end='')"],
        cwd=os.path.dirname(__file__),
        env=env,
        capture_output=True,
        text=True,
        timeout=30,
    )
    return proc.stdout


def _fake_req(responses):
    """Replays a fixed sequence of (status, body) tuples, one per call,
    regardless of method/url — ci_bootstrap.py's calls are strictly ordered
    (register -> list workspaces -> [create workspace] -> create agent), so a
    queue is a faithful and much smaller fake than a routed one."""
    it = iter(responses)

    def _req(method, url, body=None, token=None, timeout=30):  # noqa: ARG001
        return next(it)

    return _req


class TestBenchWorkspaceSlugMatchesTheMintedKey(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.env_file = Path(self.tmpdir.name) / "bench.env"

    def _invoke(self, responses, argv_extra=()):
        argv = [
            "ci_bootstrap.py",
            "--api-url", "http://127.0.0.1:8005",
            "--env-file", str(self.env_file),
            *argv_extra,
        ]
        with mock.patch.object(sys, "argv", argv), \
             mock.patch.object(ci_bootstrap, "_req", _fake_req(responses)), \
             mock.patch.object(ci_bootstrap, "wait_for_api", lambda *a, **kw: None):
            return ci_bootstrap.main()

    def _env_lines(self):
        return dict(
            line.split("=", 1) for line in self.env_file.read_text().splitlines() if line
        )

    def test_auto_created_workspace_slug_is_written_and_guard_accepts_it(self):
        # Registration auto-creates a workspace (the common CI case, #1974ef80):
        # GET /workspaces comes back non-empty, so ci_bootstrap never POSTs its
        # own — it must use THIS workspace's slug, not "recall-gate-bench".
        rc = self._invoke([
            (201, {"tokens": {"access_token": "tok"}}),                         # register
            (200, [{"id": "ws-1", "slug": "ws-8ea5842c"}]),                     # list workspaces
            (201, {"agent": {"id": "a1"}, "api_key": "agk_ws-8ea5842c_deadbeef"}),  # create agent
        ])
        self.assertEqual(0, rc)

        env = self._env_lines()
        self.assertEqual("ws-8ea5842c", env["BENCH_WORKSPACE_SLUG"])
        self.assertEqual("agk_ws-8ea5842c_deadbeef", env["MESH_AGENT_KEY"])
        self.assertEqual("ws-8ea5842c", env["BENCH_SHARED_TAG"])
        self.assertNotEqual("lme-bench", env["BENCH_SHARED_TAG"],
                             "the whole point is this is never the reserved value")

        # The point of the whole fix: the REAL guard, in a fresh process that
        # sees this env the way the real next CI step would, must not refuse.
        rc, stderr = _assert_bench_workspace_in_a_fresh_process(env)
        self.assertEqual(0, rc, stderr)

        # Guard 2: SHARED_TAG must actually pick up the override too.
        self.assertEqual("ws-8ea5842c", _shared_tag_in_a_fresh_process(env))

    def test_explicitly_created_workspace_slug_is_written_and_guard_accepts_it(self):
        # GET /workspaces comes back empty (a database with self-registration
        # but no auto-workspace) -> ci_bootstrap POSTs its own "recall-gate-bench".
        rc = self._invoke([
            (201, {"tokens": {"access_token": "tok"}}),                         # register
            (200, []),                                                          # list workspaces (empty)
            (201, {"id": "ws-2", "slug": "recall-gate-bench"}),                 # create workspace
            (201, {"agent": {"id": "a1"}, "api_key": "agk_recall-gate-bench_cafef00d"}),  # create agent
        ])
        self.assertEqual(0, rc)

        env = self._env_lines()
        self.assertEqual("recall-gate-bench", env["BENCH_WORKSPACE_SLUG"])
        self.assertEqual("recall-gate-bench", env["BENCH_SHARED_TAG"])

        rc, stderr = _assert_bench_workspace_in_a_fresh_process(env)
        self.assertEqual(0, rc, stderr)
        self.assertEqual("recall-gate-bench", _shared_tag_in_a_fresh_process(env))

    def test_a_workspace_response_missing_slug_fails_closed(self):
        with self.assertRaises(SystemExit) as ctx:
            self._invoke([
                (201, {"tokens": {"access_token": "tok"}}),
                (200, [{"id": "ws-1"}]),  # no "slug" key at all
            ])
        self.assertIn("slug", str(ctx.exception).lower())
        self.assertFalse(self.env_file.exists() and self.env_file.read_text(),
                          "a fail-closed bootstrap must not leave a half-written env file "
                          "for a later step to source blind")

    def test_RED_CONTROL_the_old_static_default_still_gets_refused(self):
        """Proves the fix is a real comparison, not a guard that now rubber-stamps
        everything. If someone "fixes" this by hardcoding BENCH_WORKSPACE_SLUG to
        the literal default instead of reading what ci_bootstrap.py observed, this
        must still fail exactly like the 16-day outage did — this is the exact
        env shape every one of those 36 runs actually had."""
        rc, stderr = _assert_bench_workspace_in_a_fresh_process({
            "MESH_AGENT_KEY": "agk_ws-8ea5842c_deadbeef",
            "BENCH_WORKSPACE_SLUG": "lme-bench",
        })
        self.assertNotEqual(0, rc, "the guard must not silently accept a mismatch")
        self.assertIn("REFUSING TO RUN", stderr)
        self.assertIn("ws-8ea5842c", stderr)

    def test_RED_CONTROL_shared_tag_defaults_to_the_reserved_value(self):
        """Pins that SHARED_TAG's DEFAULT (no BENCH_SHARED_TAG set) is still the
        literal "lme-bench" — i.e. still reserved, still rejected by the server
        for any non-privileged workspace. If a future edit ever changes this
        default, ci_bootstrap.py's override stops being necessary silently, and
        if someone instead reverts ci_bootstrap.py without noticing this default
        never changed, this is the test that catches it: the override is load-
        bearing precisely because the default never stops being reserved."""
        got = _shared_tag_in_a_fresh_process({})
        self.assertEqual("lme-bench", got)


if __name__ == "__main__":
    unittest.main(verbosity=2)
