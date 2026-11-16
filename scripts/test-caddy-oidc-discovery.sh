#!/usr/bin/env sh
# Behavioural check: in deploy/caddy/mesh.Caddyfile, OpenID
# Connect discovery and JWKS reach mesh-api, not the SPA. Runs the SHIPPED
# `handle /.well-known/openid-configuration`, `handle /oauth/*` and SPA
# `handle` blocks (cut verbatim; upstream pointed at a stub, SPA root at a
# fixture) in a real Caddy and reads the bodies.
#
# Usage: scripts/test-caddy-oidc-discovery.sh [Caddyfile]   (needs caddy + curl)
# Red control: a copy of the Caddyfile without the discovery block must fail.
set -eu

CADDYFILE="${1:-deploy/caddy/mesh.Caddyfile}"
CADDY="${CADDY:-caddy}"
PORT="${PORT:-18941}"
STUB="${STUB:-18942}"
T="$(mktemp -d)"
PID=""
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; rm -rf "$T"; }
trap cleanup EXIT INT TERM

fail() { echo "FAIL — $*" >&2; exit 1; }

mkdir -p "$T/web/current"
echo '<html>spa-shell</html>' > "$T/web/current/index.html"

TAB="$(printf '\t')"
oidc_block="$(sed -n "/^${TAB}handle \/\.well-known\/openid-configuration {/,/^${TAB}}/p" "$CADDYFILE")"
oauth_block="$(sed -n "/^${TAB}handle \/oauth\/\* {/,/^${TAB}}/p" "$CADDYFILE")"
spa_block="$(sed -n "/^${TAB}handle {/,/^${TAB}}/p" "$CADDYFILE")"
[ -n "$oauth_block" ] || fail "no 'handle /oauth/*' block in $CADDYFILE"
[ -n "$spa_block" ] || fail "no SPA 'handle {' block in $CADDYFILE"

{
  printf '{\n\tadmin off\n\tauto_https off\n}\n'
  printf ':%s {\n\trespond "api-stub {uri}" 200\n}\n' "$STUB"
  printf ':%s {\n' "$PORT"
  printf '%s\n%s\n%s\n' "$oidc_block" "$oauth_block" "$spa_block" \
    | sed "s#127.0.0.1:8005#127.0.0.1:$STUB#g; s#/opt/evc-mesh/web#$T/web#g"
  printf '}\n'
} > "$T/Caddyfile"

"$CADDY" validate --config "$T/Caddyfile" --adapter caddyfile >/dev/null 2>&1 || {
  cat "$T/Caddyfile"; "$CADDY" validate --config "$T/Caddyfile" --adapter caddyfile; fail "extracted config does not validate"; }
"$CADDY" run --config "$T/Caddyfile" --adapter caddyfile >"$T/caddy.log" 2>&1 &
PID=$!
i=0
until curl -s -o /dev/null "http://127.0.0.1:$PORT/"; do
  i=$((i + 1)); [ "$i" -lt 50 ] || { cat "$T/caddy.log"; fail "caddy did not start"; }
  sleep 0.1
done

# check <path> <body fragment>
check() {
  body="$(curl -s "http://127.0.0.1:$PORT$1")"
  echo "$body" | grep -qF "$2" || fail "$1: body '$body' lacks '$2'"
  echo "ok  $1 -> $2"
}

check /.well-known/openid-configuration "api-stub /.well-known/openid-configuration"
check /oauth/jwks "api-stub /oauth/jwks"
check "/connect/consent?client_id=x" "spa-shell"
echo "PASS caddy OIDC discovery routing"
