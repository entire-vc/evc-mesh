#!/usr/bin/env bash
# Decide whether this run's diff touches a memory path — i.e. whether the recall
# gate has anything to measure.
#
# Contract
# --------
#   in : EVENT_NAME, BASE_SHA, HEAD_SHA, MEMORY_PATHS (newline-separated prefixes)
#   out: `relevant=true|false` appended to $GITHUB_OUTPUT (when set), and
#        `memory-relevant: <v>` on stdout. Always exits 0 — this step decides
#        scope, it never decides the verdict.
#
# FAIL-CLOSED IS THE WHOLE POINT. Every path that cannot establish "this diff is
# memory-free" answers `true` and pays the full bench. Unknown must never read as
# clear here: the failure this file exists to prevent (#347) is the gate
# reporting green having measured nothing.
#
# Why merge_group is diffed rather than short-circuited
# -----------------------------------------------------
# It used to be. `[ "$EVENT_NAME" != "pull_request" ] && relevant=true` was
# deliberate — the reasoning being that a merge group is several PRs at once, so
# the bench is paid once per batch instead of once per PR.
#
# That reasoning does not survive contact with the ruleset we actually run:
# `min_entries_to_merge=1` / `min_entries_to_merge_wait_minutes=5` builds groups
# of ONE. There is no batch to amortise over, so the cost lands once per PR —
# and, because an ejected entry rebuilds into a fresh `gh-readonly-queue` branch,
# once per REBUILD. Measured 2026-08-18: the branch arm runs 29-42 min in a merge
# group, and the four PRs that stalled the queue for five hours (#578, #582,
# #588, #589) touch no memory path between them. Each had already been skipped in
# ~45s by this very predicate on its own PR run.
#
# The merge_group payload carries `base_sha` (the main tip the group was built
# on) and `head_sha` (that tip plus the batch), so the diff between them is
# exactly the batch's contents — the same question the PR arm asks, over the same
# file list. Narrowing here does not widen what can reach main ungated: a batch
# containing a memory-path change still diffs true and still pays the full bench.
#
# Other events (push, schedule, workflow_dispatch) keep the short-circuit. They
# have no two-sided diff to speak of and are not on anyone's merge path.
set -uo pipefail

emit() {
  printf 'memory-relevant: %s\n' "$1"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'relevant=%s\n' "$1" >> "$GITHUB_OUTPUT"
  fi
  exit 0
}

case "${EVENT_NAME:-}" in
  pull_request|merge_group) ;;
  *)
    echo "event '${EVENT_NAME:-}' is not diffable here — gating everything."
    emit true
    ;;
esac

# An empty path list would make every diff read as memory-free — the exact
# false-clear this predicate exists to prevent. It is populated from the
# workflow's `env:`, so empty means the wiring broke, not that nothing matters.
if [ -z "$(printf '%s' "${MEMORY_PATHS:-}" | tr -d '[:space:]')" ]; then
  echo "MEMORY_PATHS is empty — the path list did not reach this step, gating everything."
  emit true
fi

if [ -z "${BASE_SHA:-}" ] || [ -z "${HEAD_SHA:-}" ]; then
  echo "base/head sha missing on ${EVENT_NAME} — cannot establish scope, gating everything."
  emit true
fi

if ! changed=$(git diff --name-only "${BASE_SHA}" "${HEAD_SHA}" 2>&1); then
  echo "git diff ${BASE_SHA}..${HEAD_SHA} failed — cannot establish scope, gating everything."
  echo "${changed}"
  emit true
fi

echo "Changed files:"
echo "${changed}"

relevant=false
while IFS= read -r p; do
  [ -z "$p" ] && continue
  if echo "${changed}" | grep -qE "^${p}"; then relevant=true; fi
done <<< "$(printf '%s' "${MEMORY_PATHS:-}" | tr -d '\r')"

emit "${relevant}"
