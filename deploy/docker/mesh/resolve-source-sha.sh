#!/bin/sh
#
# Prints the evc-mesh commit a Docker build is made from, for the web
# bundle's stamp (dist/.build-sha, see buildSha() in web/vite.config.ts).
#
# The web build stage has no .git (it copies web/ only), so vite cannot ask
# git itself and the stamp used to be skipped. This runs in a
# small stage of its own over the whole build context — the evc-mesh checkout
# docker-compose.prod.yml points at, never a deploy repository around it — and
# reads HEAD the same way the go-builder stage of Dockerfile does for
# /api/version, so the API and the bundle name the same commit.
#
# Sources, in order:
#   1. git HEAD of <src_dir> (a normal clone, --depth 1 included);
#   2. <explicit_sha> (build arg MESH_SOURCE_SHA) for a context without .git
#      (a tarball). It must be a full 40-hex commit SHA; when git also knows
#      HEAD, the two must agree.
# Neither → prints nothing and exits 0: the build goes on unstamped and
# web/perf/check-bundle-budget.mjs refuses that dist. Never prints a guess.
# A malformed or contradicting MESH_SOURCE_SHA fails the build (exit 1).
#
# Usage: sh resolve-source-sha.sh <src_dir> [explicit_sha]
# Test:  sh deploy/docker/mesh/test-resolve-source-sha.sh
set -eu

SRC=${1:?usage: resolve-source-sha.sh <src_dir> [explicit_sha]}
EXPLICIT=$(printf '%s' "${2:-}" | tr -d '[:space:]')

if [ -n "$EXPLICIT" ] && ! printf '%s\n' "$EXPLICIT" | grep -Eqx '[0-9a-f]{40}'; then
  echo "resolve-source-sha: MESH_SOURCE_SHA='$EXPLICIT' is not a full 40-hex commit SHA" >&2
  exit 1
fi

FROM_GIT=""
# Only a .git that belongs to <src_dir> itself: without this test git
# would walk up to an enclosing repository (a deploy checkout around it).
if [ -e "$SRC/.git" ]; then
  FROM_GIT=$(git -c safe.directory='*' -C "$SRC" rev-parse --verify --quiet 'HEAD^{commit}' 2>/dev/null || true)
fi

if [ -n "$FROM_GIT" ] && [ -n "$EXPLICIT" ] && [ "$FROM_GIT" != "$EXPLICIT" ]; then
  echo "resolve-source-sha: MESH_SOURCE_SHA=$EXPLICIT but the checkout's HEAD is $FROM_GIT" >&2
  exit 1
fi

printf '%s' "${FROM_GIT:-$EXPLICIT}"
