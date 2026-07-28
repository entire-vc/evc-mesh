#!/bin/bash
# Block until no memory-bench run is in flight against the shared prod endpoint.
#
# The previous incarnation of this probe FAIL-OPENED and re-caused the incident it
# existed to prevent: `gh run list --jq` without `--json` exits 1 with EMPTY
# stdout, the count built on it read as 0, and "window is clear" was published
# while a run had been hogging prod the whole time. So:
#
#   * `--json` comes BEFORE `--jq`, and gh's exit code is asserted, not assumed;
#   * an unparseable/empty response is exit 2 (UNKNOWN), never "clear";
#   * a positive control runs first — the probe must be able to SEE a live run,
#     otherwise a 0 proves nothing about the window and only that the probe is
#     broken.
#
# Exit codes:  0 window clear   1 gave up (still busy at deadline)   2 probe broken

set -uo pipefail

REPO=entire-vc/evc-mesh
WF=memory-bench.yml
DEADLINE=$(( $(date +%s) + ${MAX_WAIT:-3600} ))
POLL=${POLL:-60}

probe() {  # prints "<in_flight_count> <total_rows>", or returns non-zero
  local out rc
  out=$(gh run list --repo "$REPO" --workflow "$WF" --limit 30 \
          --json status,databaseId,headBranch,createdAt 2>&1); rc=$?
  if [ $rc -ne 0 ]; then
    echo "PROBE_FAILED gh exit=$rc: $out" >&2
    return 2
  fi
  printf '%s' "$out" | python3 -c '
import json, sys
rows = json.load(sys.stdin)
if not rows:
    sys.exit(3)                      # empty page: cannot conclude anything
live = [r for r in rows if r["status"] in ("in_progress", "queued")]
for r in live:
    sys.stderr.write("  LIVE {} {} {}\n".format(r["databaseId"], r["headBranch"], r["createdAt"]))
sys.stdout.write("{} {}\n".format(len(live), len(rows)))
' || return 2
}

echo "[$(date -u +%H:%M:%SZ)] positive control — the probe must be able to see a live run"
first=$(probe); prc=$?
[ $prc -ne 0 ] && { echo "probe is broken; refusing to report a clear window"; exit 2; }
echo "[$(date -u +%H:%M:%SZ)] baseline: in_flight=${first% *} of ${first#* } rows"
if [ "${first% *}" -eq 0 ]; then
  echo "NOTE: zero in flight at the very first poll — the positive control did not"
  echo "      fire, so this 0 is unverified. Re-checking once after one poll interval."
  sleep "$POLL"
  first=$(probe) || exit 2
fi

while :; do
  res=$(probe); rc=$?
  [ $rc -ne 0 ] && { echo "probe broke mid-wait — not reporting clear"; exit 2; }
  n=${res% *}
  now=$(date -u +%H:%M:%SZ)
  if [ "$n" -eq 0 ]; then
    echo "[$now] WINDOW CLEAR (0 in flight of ${res#* } rows examined)"
    exit 0
  fi
  if [ "$(date +%s)" -ge "$DEADLINE" ]; then
    echo "[$now] gave up: still $n in flight at the deadline"
    exit 1
  fi
  echo "[$now] $n in flight — waiting ${POLL}s"
  sleep "$POLL"
done
