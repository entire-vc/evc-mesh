#!/usr/bin/env python3
"""Self-check for confirm_embedder_alive.sh — the mid-job embedder liveness gate.

    python scripts/memory-bench/test_confirm_embedder_alive.py

Background — the defect this pins
----------------------------------
The CI embedder sidecar (ci_embedder.py, port 8099) is confirmed healthy once,
at its own startup step. Several minutes and several steps (migrations, Go
build, mesh-mcp checkout + cross-compile) separate that confirmation from the
first step that actually depends on it. A crash or OOM-kill in that window
went unnoticed until scored questions started failing — 24/24 "could not run"
with no named cause (#352a0b11), because every individual recall/remember
call just failed and the run silently degraded to INCONCLUSIVE.

This script is meant to be run as its OWN CI step right before measurement
starts, so a dead embedder there is one loud, specific failure instead of a
mystery diagnosed after the fact.

Both directions use a REAL server / a REAL closed port — not a mock of curl's
exit code — because the shell script's own health check is the thing being
proven, not a description of it.
"""

from __future__ import annotations

import http.server
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "confirm_embedder_alive.sh"


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class _HealthzHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 - http.server's naming convention
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):  # silence per-request stderr noise
        pass


def run_check(url: str, log_path: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["bash", str(SCRIPT), url, str(log_path)],
        capture_output=True, text=True, timeout=15,
    )


class TestEmbedderLivenessGate(unittest.TestCase):
    def test_positive_control_a_real_server_passes(self):
        """A genuinely reachable embedder must pass cleanly, rc=0."""
        port = _free_port()
        server = http.server.HTTPServer(("127.0.0.1", port), _HealthzHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                log_path = Path(tmp) / "embedder.log"
                result = run_check(f"http://127.0.0.1:{port}/healthz", log_path)
        finally:
            server.shutdown()
            thread.join(timeout=5)

        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("confirmed alive", result.stdout)

    def test_negative_control_a_genuinely_dead_port_fails_loudly(self):
        """A closed port (no listener at all — the crash/OOM-kill shape) must
        fail with rc=1 and a NAMED cause, never silently pass."""
        dead_port = _free_port()  # bound-then-released: guaranteed nothing listens
        with tempfile.TemporaryDirectory() as tmp:
            log_path = Path(tmp) / "embedder.log"
            log_path.write_text("embedder crashed: OOMKilled\n")
            result = run_check(f"http://127.0.0.1:{dead_port}/healthz", log_path)

        self.assertEqual(1, result.returncode)
        combined = result.stdout + result.stderr
        self.assertIn("::error::", combined)
        self.assertIn("unreachable right before measurement started", combined)
        # The named cause: the failure explicitly surfaces the log content and
        # the process check, not just "curl failed".
        self.assertIn("embedder crashed: OOMKilled", combined)
        self.assertIn("ci_embedder.py process is not running", combined)

    def test_negative_control_missing_log_file_does_not_crash_the_check(self):
        """No embedder.log at all (process left no trace) must still fail
        loudly with a named cause, not a shell error from a missing file."""
        dead_port = _free_port()
        with tempfile.TemporaryDirectory() as tmp:
            missing_log = Path(tmp) / "does-not-exist.log"
            result = run_check(f"http://127.0.0.1:{dead_port}/healthz", missing_log)

        self.assertEqual(1, result.returncode)
        combined = result.stdout + result.stderr
        self.assertIn("::error::", combined)
        self.assertIn("no embedder.log", combined)


if __name__ == "__main__":
    unittest.main()
