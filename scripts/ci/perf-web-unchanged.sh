#!/usr/bin/env bash
# Exit 0 when the branch changes nothing in the measured frontend (web/) or the
# harness (docs/perf/) since its merge-base with <base> (three-dot: a branch
# behind a main that moved web/ still introduces no web change): the branch and main builds are then the
# same code and any p95 delta is runner noise (perf-task-open).
# Exit 1 when they differ, 2 on a git error (fail-closed: the job then measures).
#   scripts/ci/perf-web-unchanged.sh [base-ref]   (default origin/main)
set -u
base="${1:-origin/main}"
git rev-parse --verify -q "$base^{commit}" >/dev/null || { echo "perf-web-unchanged: no ref $base" >&2; exit 2; }
git diff --quiet "$base...HEAD" -- web docs/perf
rc=$?
[ "$rc" -le 1 ] || rc=2
exit "$rc"
