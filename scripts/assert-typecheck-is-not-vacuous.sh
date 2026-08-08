#!/usr/bin/env bash
#
# Negative control for `pnpm typecheck`.
#
# A type check that cannot fail is worse than no type check: both humans and CI
# read the green as "types verified" and close on it. That is exactly what
# happened here — `web/tsconfig.json` is solution-style (`"files": []` plus
# `references`), so the old `tsc --noEmit` compiled an empty program and exited
# 0 no matter what was in `src/`. A semantic merge conflict (one commit removes
# an import, another starts using it — no textual overlap, so git merges
# cleanly) rode that false green onto main.
#
# This script proves the command still rejects a bad state: it plants a file
# with a deliberate TS2304, runs `pnpm typecheck`, and requires a NON-zero exit.
# Run it in CI immediately before the real typecheck.
#
# Exit 0 = the type check discriminates. Exit 1 = it is vacuous, do not trust it.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$REPO_ROOT/web"
PROBE="$WEB_DIR/src/__typecheck_negative_control__.ts"

cleanup() { rm -f "$PROBE"; }
trap cleanup EXIT

if [ -e "$PROBE" ]; then
  echo "FAIL: $PROBE already exists — a previous run leaked it. Remove it and re-run." >&2
  exit 1
fi

cat > "$PROBE" <<'PROBE_EOF'
// Temporary negative control planted by scripts/assert-typecheck-is-not-vacuous.sh.
// If you are reading this in a commit or a diff, a run leaked it — delete the file.
export const typecheckNegativeControl = thisIdentifierIsDeliberatelyUndeclared;
PROBE_EOF

echo "── Negative control: planting TS2304 in web/src, expecting typecheck to FAIL ──"

set +e
( cd "$WEB_DIR" && pnpm typecheck ) >/tmp/typecheck-negative-control.log 2>&1
STATUS=$?
set -e

if [ "$STATUS" -eq 0 ]; then
  echo "" >&2
  echo "FAIL: \`pnpm typecheck\` exited 0 on a file containing TS2304." >&2
  echo "      The type check is VACUOUS — its green means nothing." >&2
  echo "      Usual cause: the script points at the solution-style web/tsconfig.json" >&2
  echo "      (\"files\": [] + references) without build mode. Use \`tsc -b\`, which" >&2
  echo "      follows the project references, not a bare \`tsc --noEmit\`." >&2
  echo "" >&2
  echo "--- typecheck output was: ---" >&2
  cat /tmp/typecheck-negative-control.log >&2
  exit 1
fi

if ! grep -qE 'TS2304|Cannot find name' /tmp/typecheck-negative-control.log; then
  echo "" >&2
  echo "FAIL: \`pnpm typecheck\` exited $STATUS, but not because of the planted TS2304." >&2
  echo "      It failed for some other reason, so this run proves nothing about" >&2
  echo "      whether the check discriminates. Output:" >&2
  cat /tmp/typecheck-negative-control.log >&2
  exit 1
fi

echo "OK: typecheck exited $STATUS and reported the planted TS2304 — it discriminates. ✓"
