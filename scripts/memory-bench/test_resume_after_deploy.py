#!/usr/bin/env python3
"""Does the version pin WAIT for a pending deploy and then RESUME the measurement?

Same convention as the sibling self-checks: stdlib `unittest`, no Mesh, no
network beyond a loopback stub, no pytest.

`test_serving_version.py` already covers `wait_for_version` with an injected
`fetch`. That proves the loop's logic; it does not prove the branch works when
every collaborator is real. This file runs `check_serving_version.py` as a
**subprocess** against a **real loopback HTTP endpoint that really flips** and a
**real git repository**, so the parts an injected fetch skips are exercised too:
the urllib request, the JSON shape, `parse_deploy_paths` reading an actual
`deploy-backend.yml`, `git merge-base`/`git diff` against actual commits, and the
process exit code the workflow branches on.

Hermetic by construction. `check_serving_version.py` derives `REPO_ROOT` from its
own `__file__`, so copying it into a synthesized repo points every git call at
that repo. Nothing here depends on evc-mesh's own history, which keeps the test
honest on a shallow clone or a fork.

WHY THIS EXISTS (#e8570874 AC3). The defect was a gate that measured whatever
binary happened to be serving. The fix must do two separable things — refuse a
deploy that never lands, and *resume* one that does — and a guard that only
refuses is the failure this card's sibling (#3ce651a0) recorded: fail-closed is
not the safe direction.

WHY THE ASSERTIONS ARE NOT JUST `rc`. Mutating `classify()` to accept anything
reproduces the original defect exactly, and under that mutant the resume arm
**still exits 0** — a pin that never waits and a pin that waited correctly return
the same code. Only the observable waiting separates them, so the wait is
asserted on the log AND on wall-clock, not on the exit status.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
CLI_NAME = "check_serving_version.py"

# Mirrors deploy-backend.yml's shape, including the `!**/*_test.go` exclusion —
# so this file also pins that a test-only delta is `equivalent` (no wait) rather
# than an eternal wait for a deploy that will never come.
DEPLOY_WORKFLOW = """\
name: Deploy backend
on:
  push:
    branches: [main]
    paths:
      - 'cmd/**'
      - 'internal/**'
      - 'go.mod'
      - 'go.sum'
      - 'migrations/**'
      - '!**/*_test.go'
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
"""

POLL_SECONDS = 0.5
FLIP_AFTER = 2


class _VersionStub(HTTPServer):
    """Serves `old` for the first `flip_after` polls, then `new` for ever."""

    old = ""
    new = ""
    flip_after = 0
    hits = 0


class _Handler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 - stdlib signature
        if self.path != "/api/version":
            self.send_error(404)
            return
        self.server.hits += 1
        commit = self.server.new if self.server.hits > self.server.flip_after else self.server.old
        body = json.dumps(
            {
                "service": "evc-mesh-api",
                "environment": "prod",
                "commit": commit,
                "version": commit[:7],
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


def _serve(old: str, new: str, flip_after: int) -> _VersionStub:
    srv = _VersionStub(("127.0.0.1", 0), _Handler)
    srv.old, srv.new, srv.flip_after, srv.hits = old, new, flip_after, 0
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def _stop(srv: _VersionStub) -> None:
    """`shutdown()` alone leaves the listening socket open (ResourceWarning)."""
    srv.shutdown()
    srv.server_close()


class ResumeAfterDeploy(unittest.TestCase):
    """The pin waits out a landing deploy and then measures the right binary."""

    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.mkdtemp(prefix="resume-after-deploy-")
        repo = Path(cls._tmp) / "repo"
        (repo / "scripts" / "memory-bench").mkdir(parents=True)
        (repo / ".github" / "workflows").mkdir(parents=True)
        (repo / "internal").mkdir()

        # The real CLI, so REPO_ROOT resolves to this synthesized repo.
        shutil.copy2(SCRIPT_DIR / CLI_NAME, repo / "scripts" / "memory-bench" / CLI_NAME)
        (repo / ".github" / "workflows" / "deploy-backend.yml").write_text(DEPLOY_WORKFLOW)

        cls.repo = repo
        cls.cli = repo / "scripts" / "memory-bench" / CLI_NAME

        def git(*args):
            out = subprocess.run(
                ["git", "-c", "user.email=t@t", "-c", "user.name=t", *args],
                cwd=repo, capture_output=True, text=True, check=False,
            )
            if out.returncode != 0:
                raise AssertionError(f"git {args} failed: {out.stderr}")
            return out.stdout.strip()

        git("init", "-q", "-b", "main")
        (repo / "internal" / "app.go").write_text("package app\n")
        git("add", "-A")
        git("commit", "-q", "-m", "base")
        cls.base = git("rev-parse", "HEAD")

        # A REAL deployable change: internal/**, not a _test.go.
        (repo / "internal" / "app.go").write_text("package app\n\nconst V = 2\n")
        git("add", "-A")
        git("commit", "-q", "-m", "deployable change")
        cls.deployable = git("rev-parse", "HEAD")

        # A test-only change on top: matches internal/** but excluded by
        # '!**/*_test.go', so no deploy will ever land for it.
        (repo / "internal" / "app_test.go").write_text("package app\n")
        git("add", "-A")
        git("commit", "-q", "-m", "test-only change")
        cls.test_only = git("rev-parse", "HEAD")

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls._tmp, ignore_errors=True)

    def _run(self, base_url, expect, wait_seconds):
        started = time.monotonic()
        proc = subprocess.run(
            [
                sys.executable, str(self.cli),
                "--base-url", base_url,
                "--expect", expect,
                "--poll-seconds", str(POLL_SECONDS),
                "--wait-seconds", str(wait_seconds),
            ],
            capture_output=True, text=True, check=False,
            env={**os.environ, "GITHUB_OUTPUT": str(Path(self._tmp) / "gh_output")},
        )
        return proc, proc.stdout + proc.stderr, time.monotonic() - started

    # ------------------------------------------------------------------
    # control — without this, "it resumed" can mean "it never waited"
    # ------------------------------------------------------------------

    def test_the_stub_really_flips(self):
        """A stub that silently failed to flip would make the resume test vacuous."""
        import urllib.request

        srv = _serve(self.base, self.deployable, FLIP_AFTER)
        self.addCleanup(_stop, srv)
        url = f"http://127.0.0.1:{srv.server_address[1]}/api/version"
        seen = []
        for _ in range(FLIP_AFTER + 2):
            with urllib.request.urlopen(url, timeout=5) as resp:
                seen.append(json.load(resp)["commit"])
        self.assertEqual(seen[0], self.base, f"stub did not start on the old commit: {seen}")
        self.assertEqual(seen[-1], self.deployable, f"stub never flipped: {seen}")

    # ------------------------------------------------------------------
    # the branch under test
    # ------------------------------------------------------------------

    def test_waits_for_the_deploy_then_resumes(self):
        srv = _serve(self.base, self.deployable, FLIP_AFTER)
        self.addCleanup(_stop, srv)
        proc, out, elapsed = self._run(
            f"http://127.0.0.1:{srv.server_address[1]}", self.deployable, wait_seconds=30.0
        )

        self.assertEqual(proc.returncode, 0, f"expected the gate to resume and pass:\n{out}")

        # rc alone is NOT enough: a classify() that accepts anything also exits 0.
        # The waiting is the only observable that separates the two.
        waits = out.count("[version] waiting")
        self.assertGreaterEqual(
            waits, FLIP_AFTER,
            f"expected >={FLIP_AFTER} poll waits, saw {waits} — the pin passed without "
            f"waiting, which is the original defect:\n{out}",
        )
        self.assertGreaterEqual(
            elapsed, FLIP_AFTER * POLL_SECONDS * 0.8,
            f"returned in {elapsed:.2f}s — too fast to have polled:\n{out}",
        )
        self.assertIn(
            "a deploy is due and has not landed", out,
            f"never recognised the pending deploy while waiting:\n{out}",
        )
        self.assertIn(
            f"attempt {FLIP_AFTER + 1}: serving {self.deployable[:7]}", out,
            f"did not succeed on the attempt after the flip:\n{out}",
        )
        self.assertNotIn(
            "GATE_REASON", out,
            f"resumed but still emitted a refusal reason:\n{out}",
        )

    def test_refuses_when_the_deploy_never_lands(self):
        """Resuming must not have been bought by weakening the refusal."""
        srv = _serve(self.base, self.deployable, flip_after=10**9)
        self.addCleanup(_stop, srv)
        proc, out, _ = self._run(
            f"http://127.0.0.1:{srv.server_address[1]}", self.deployable, wait_seconds=1.2
        )

        self.assertEqual(proc.returncode, 2, f"expected rc 2 (inconclusive):\n{out}")
        self.assertIn("version-mismatch", out, f"reason not named:\n{out}")
        self.assertIn(self.base[:7], out, f"does not say what prod was serving:\n{out}")
        self.assertIn(self.deployable[:7], out, f"does not say what was under test:\n{out}")
        self.assertIn("Nothing was measured", out, f"does not state nothing was enforced:\n{out}")

    def test_a_test_only_delta_is_equivalent_rather_than_an_eternal_wait(self):
        """`!**/*_test.go` means no deploy will land — waiting for one never ends.

        Regression pin for the coupling between `deploy-backend.yml`'s exclusion
        and this script's pathspec translation. Before `to_git_pathspec`, a bare
        `!` went straight to `git diff`, which does not read it as an exclusion,
        so a test-only delta looked deployable and the pin polled to timeout for
        a deploy that could never arrive.
        """
        srv = _serve(self.deployable, self.deployable, flip_after=10**9)
        self.addCleanup(_stop, srv)
        proc, out, _ = self._run(
            f"http://127.0.0.1:{srv.server_address[1]}", self.test_only, wait_seconds=1.2
        )

        self.assertEqual(
            proc.returncode, 0,
            f"a test-only delta must be accepted immediately, not waited on:\n{out}",
        )
        self.assertIn("equivalent", out, f"not classified as equivalent:\n{out}")
        self.assertNotIn(
            "[version] waiting", out,
            f"waited for a deploy that will never land:\n{out}",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
