#!/usr/bin/env bash
#
# Negative control for e2e/tsconfig coverage.
#
# `web/tsconfig.app.json` only includes `src`, and `web/tsconfig.node.json` only
# includes `vite.config.ts` + `vitest.config.ts` — before `tsconfig.e2e.json`
# existed, nothing in the project-reference graph covered `e2e/`,
# `playwright.config.ts`. `pnpm typecheck` (build mode, see
# assert-typecheck-is-not-vacuous.sh for why it must be `-b`) could stay green
# forever while e2e specs rotted, and nobody would see it — the same
# false-green shape one layer up, scoped to a directory instead of the whole
# package.
#
# This script proves the reference graph actually reaches e2e/: it plants a
# file with a deliberate TS2304 under web/e2e/, runs `pnpm typecheck`, and
# requires a NON-zero exit that names the planted error.
#
# Exit 0 = e2e/ is covered. Exit 1 = it is not — a project reference regressed.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$REPO_ROOT/web"
PROBE="$WEB_DIR/e2e/__typecheck_negative_control__.spec.ts"

cleanup() { rm -f "$PROBE"; }
trap cleanup EXIT

if [ -e "$PROBE" ]; then
  echo "FAIL: $PROBE already exists — a previous run leaked it. Remove it and re-run." >&2
  exit 1
fi

cat > "$PROBE" <<'PROBE_EOF'
// Temporary negative control planted by scripts/assert-typecheck-covers-e2e.sh.
// If you are reading this in a commit or a diff, a run leaked it — delete the file.
export const typecheckE2eNegativeControl = thisIdentifierIsDeliberatelyUndeclared;
PROBE_EOF

echo "── Negative control: planting TS2304 in web/e2e, expecting typecheck to FAIL ──"

set +e
( cd "$WEB_DIR" && pnpm typecheck ) >/tmp/typecheck-e2e-negative-control.log 2>&1
STATUS=$?
set -e

if [ "$STATUS" -eq 0 ]; then
  echo "" >&2
  echo "FAIL: \`pnpm typecheck\` exited 0 on a file containing TS2304 under web/e2e/." >&2
  echo "      e2e/ is NOT covered by the project-reference graph — its green means" >&2
  echo "      nothing about e2e specs. Usual cause: tsconfig.e2e.json is missing or" >&2
  echo "      no longer referenced from web/tsconfig.json." >&2
  echo "" >&2
  echo "--- typecheck output was: ---" >&2
  cat /tmp/typecheck-e2e-negative-control.log >&2
  exit 1
fi

if ! grep -qE 'TS2304|Cannot find name' /tmp/typecheck-e2e-negative-control.log; then
  echo "" >&2
  echo "FAIL: \`pnpm typecheck\` exited $STATUS, but not because of the planted TS2304." >&2
  echo "      It failed for some other reason, so this run proves nothing about" >&2
  echo "      whether e2e/ is covered. Output:" >&2
  cat /tmp/typecheck-e2e-negative-control.log >&2
  exit 1
fi

echo "OK: typecheck exited $STATUS and reported the planted TS2304 in e2e/ — it is covered. ✓"
