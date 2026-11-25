#!/bin/sh
# semgrep gate: `semgrep ci` that cannot pass without scanning.
#
# Why a wrapper. `semgrep ci` on a merge request computes its own baseline
# (`git fetch <target>` + `git merge-base --all HEAD FETCH_HEAD`). When that
# merge-base fails it aborts the scan before the first rule, reports the error
# and then exits 0 because "there were no blocking findings": an empty scan with
# a green tick. Job 867844 (MR !2080) shipped that way; the finding surfaced
# only on main and stopped the prod trigger. Reproduced: a build dir reused with
# a stale `.git/shallow` cut on the target side makes `git merge-base` exit 1
# with no output, exactly as in that job.
#
# What this script guarantees:
#   merge request: the baseline is GitLab's own MR diff base
#     ($CI_MERGE_REQUEST_DIFF_BASE_SHA) handed to semgrep as --baseline-commit,
#     so semgrep never computes one itself. Baseline missing, not in the clone or
#     not an ancestor of HEAD → exit 1 before the scan.
#   both pipelines: semgrep must write its JSON result (no file = the scan was
#     aborted → exit 1), and it must report at least one scanned file whenever
#     there is something to scan: on main always, on a merge request whenever the
#     diff adds or modifies a text file. A diff of deletions or binaries only has
#     nothing to scan and passes with that stated in the log.
#   semgrep's own exit code (1 = blocking findings) is passed through unchanged.
#
# Deliberately NOT `--no-suppress-errors`: it fails the scan on any reported
# error, rule timeouts included, and the full-tree main scan logs those on a
# contended runner (job 868563). Absence of a result is checked instead.
#
# Known strictness: a merge request whose only text changes sit under a path
# `.semgrepignore` excludes (vendor/, dist/, build/, ...) scans 0 files and is
# refused. One tracked file is under such a path today; widen the scan or move
# the file rather than relaxing this check.
#
# Test: scripts/ci/test-semgrep-gate.sh (job `semgrep-gate-controls`).
set -eu

refuse() {
  echo "REFUSING (semgrep gate): $*" >&2
  echo "A semgrep pass here must mean 'scanned and clean'; this run cannot say that." >&2
  exit 1
}

OUT_DIR=$(mktemp -d)
OUT="$OUT_DIR/semgrep.json"

if [ "${CI_PIPELINE_SOURCE:-}" = "merge_request_event" ]; then
  BASE=${CI_MERGE_REQUEST_DIFF_BASE_SHA:-}
  [ -n "$BASE" ] || refuse "CI_MERGE_REQUEST_DIFF_BASE_SHA is empty: no baseline to diff against"
  git cat-file -e "$BASE^{commit}" 2>/dev/null ||
    refuse "baseline $BASE is not in this clone (shallow? is GIT_DEPTH 0 on the job?)"
  git merge-base --is-ancestor "$BASE" HEAD ||
    refuse "baseline $BASE is not an ancestor of HEAD $(git rev-parse HEAD) in this clone (stale .git/shallow?)"
  # Added, copied, modified, renamed or type-changed text files: what a diff scan must cover.
  # numstat prints "-<TAB>-" for binaries.
  TO_SCAN=$(git diff --numstat --diff-filter=ACMRT "$BASE" HEAD | awk -F'\t' '$1 != "-" { n++ } END { print n + 0 }')
  echo "semgrep gate: baseline $BASE (MR diff base), $TO_SCAN changed text file(s) to scan"
  rc=0
  semgrep ci --baseline-commit "$BASE" --json-output="$OUT" || rc=$?
else
  TO_SCAN=full
  rc=0
  semgrep ci --json-output="$OUT" || rc=$?
fi

[ "$rc" -eq 0 ] || exit "$rc"
[ -s "$OUT" ] || refuse "semgrep exited 0 but wrote no result: the scan was aborted (see the [ERROR] above)"

SCANNED=$(python3 -I -c 'import json, sys; print(len(json.load(open(sys.argv[1]))["paths"]["scanned"]))' "$OUT") ||
  refuse "cannot read paths.scanned from the semgrep result"
echo "semgrep gate: scanned $SCANNED file(s)"

if [ "$SCANNED" -eq 0 ]; then
  if [ "$TO_SCAN" = "0" ]; then
    echo "semgrep gate: the diff adds or modifies no text file (deletions/binaries only); nothing to scan"
  else
    refuse "0 files scanned while there is something to scan ($TO_SCAN)"
  fi
fi
exit 0
