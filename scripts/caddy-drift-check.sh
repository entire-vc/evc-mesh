#!/usr/bin/env bash
# Compares the tracked prod Caddyfile (deploy/caddy/mesh-vm.Caddyfile) against the
# live file on mesh-vm. Read-only — never writes anything to the remote host.
#
# Exit 0 = repo and live host match (no drift).
# Exit 1 = drift detected, OR the host could not be reached/read — fail-closed.
#   A warning comment on the file ("must stay identical") does not enforce
#   anything on its own; this script is what makes that claim checkable and
#   makes a future divergence loud instead of silent (Mesh e8c44540).
set -euo pipefail

REPO_FILE="${1:-deploy/caddy/mesh-vm.Caddyfile}"
HOST="${CADDY_DRIFT_HOST:-mesh-vm}"
REMOTE_PATH="${CADDY_DRIFT_REMOTE_PATH:-/etc/caddy/Caddyfile}"

if [ ! -f "$REPO_FILE" ]; then
  echo "::error::repo file not found: $REPO_FILE" >&2
  exit 1
fi

TMPFILE=$(mktemp)
trap 'rm -f "$TMPFILE"' EXIT

if ! ssh -o ConnectTimeout=15 -o BatchMode=yes "$HOST" "cat '$REMOTE_PATH'" > "$TMPFILE" 2>/dev/null; then
  echo "::error::could not read $REMOTE_PATH on $HOST via ssh — treating as drift (fail-closed, not 'no drift')" >&2
  exit 1
fi

if ! diff -u "$REPO_FILE" "$TMPFILE"; then
  echo "::error::DRIFT DETECTED: $REPO_FILE (repo, tracked in git) differs from ${HOST}:${REMOTE_PATH} (live prod)." >&2
  echo "::error::Either the repo copy is stale (someone edited prod directly — pull it back with this same script's remote read and open a PR), or a PR landed but was never deployed via deploy-caddy.yml." >&2
  exit 1
fi

echo "No drift: $REPO_FILE matches ${HOST}:${REMOTE_PATH}"
