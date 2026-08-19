#!/usr/bin/env bash
# Regenerate internal/textanchor/testdata/frontend-anchors.json — the anchors the
# frontend builder produces for scripts/anchor-contract/cases.json.
#
# The fixture is what makes the cross-implementation test a drift detector rather
# than two sets of numbers somebody typed to agree. It is generated, never
# hand-edited: run this after any deliberate change to the resolution rules on
# either side, and read the diff as "this is what moved".
#
# Both halves of the contract then assert against it — the frontend in
# web/src/lib/doc-comments/__tests__/anchor-contract.test.ts, the server in
# internal/textanchor/contract_test.go — so a one-sided change goes red on its
# own build.
set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "$REPO_ROOT/web"

if [ ! -d node_modules ]; then
  echo "web/node_modules is missing — run 'pnpm install' in web/ first" >&2
  exit 1
fi

ANCHOR_WRITE=1 ./node_modules/.bin/vitest run --coverage.enabled=false \
  src/lib/doc-comments/__tests__/anchor-contract.test.ts

echo "wrote internal/textanchor/testdata/frontend-anchors.json"
