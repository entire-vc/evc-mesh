#!/bin/sh
# Decide which Go packages this pipeline tests, and leave the answer in a file.
#
#   sh scripts/ci/go_selection.sh [go_affected_packages.py options]
#
# Writes $GO_SELECTION_FILE (default go-selection.txt): the single word `ALL`, or one
# import path per line, or nothing at all. The `selection: ...` line that says which,
# and why, goes to the job log.
#
# Only a merge request is narrowed. A push to main, a schedule or a manual run has no
# diff base and always tests everything: the whole suite on main is what catches a
# narrowing mistake after it has merged. Every way this script can fail ends in ALL.
#
# The `selection: ...` line is also kept in $out.reason, so the manifest can carry it to
# coverage-gate.
out="${GO_SELECTION_FILE:-go-selection.txt}"
if [ "${CI_PIPELINE_SOURCE:-}" != merge_request_event ]; then
  echo "selection: ALL because this is a ${CI_PIPELINE_SOURCE:-local} pipeline, not a merge request" > "$out.reason"
  echo ALL > "$out"
  cat "$out.reason" >&2
  exit 0
fi
if ! python3 scripts/ci/go_affected_packages.py "$@" > "$out.tmp" 2> "$out.reason"; then
  echo "selection: ALL because the selector could not run" > "$out.reason"
  echo ALL > "$out"
  rm -f "$out.tmp"
  cat "$out.reason" >&2
  exit 0
fi
mv "$out.tmp" "$out"
cat "$out.reason" >&2
