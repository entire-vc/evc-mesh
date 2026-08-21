#!/usr/bin/env bash
# Re-confirm the CI embedder sidecar is alive right before anything measures
# through it — not just at its own startup step. See the "Confirm the
# embedder is still alive before measuring" step in memory-bench.yml for why:
# several minutes and several steps separate embedder startup from the first
# step that depends on it, long enough for a crash or an OOM-kill in between
# to go unnoticed until a scored question fails and the run degrades to
# INCONCLUSIVE with no named cause (#352a0b11).
#
# Usage: confirm_embedder_alive.sh <healthz-url> <embedder-log-path>
# Exits 0 and prints a confirmation if the embedder answers; exits 1 with a
# named diagnostic (log tail + process check) if it does not.
set -euo pipefail

healthz_url="${1:?usage: confirm_embedder_alive.sh <healthz-url> <embedder-log-path>}"
embedder_log="${2:?usage: confirm_embedder_alive.sh <healthz-url> <embedder-log-path>}"

if curl -sf --max-time 5 "$healthz_url" > /dev/null; then
  echo "embedder confirmed alive immediately before measurement"
  exit 0
fi

echo "::error::embedder at $healthz_url is unreachable right before measurement started — it was healthy at its own startup step, so it died or hung in between. Nothing below this step can be trusted; failing fast instead of letting every recall/remember call degrade the run to INCONCLUSIVE."
echo "::group::embedder.log"
tail -100 "$embedder_log" 2>/dev/null || echo "(no embedder.log — process left no trace)"
echo "::endgroup::"
pgrep -fl ci_embedder || echo "ci_embedder.py process is not running"
exit 1
