#!/usr/bin/env bash
# One-command local Mesh stand: throwaway Postgres + Redis → API (cmd/api) →
# seed data → vite dev → a URL you can log into with a browser, without a
# single prod credential.
#
# Why this exists: §1k requires a real screenshot on every UI task, and the
# native (non-Casdoor) Mesh login has no agent-usable credential on the Mac
# Mini — agent keys authorize REST, not a browser session, and the CI E2E
# user is a viewer that can't seed data. This was worked out live during D7
# (#fd2bfec6) and independently again by Garfield (#62e58c9d) the same day;
# this script is that recipe made reusable instead of re-derived per task.
#
# Usage:
#   scripts/local-stack.sh up          # bring the whole stand up, seed, print URL
#   scripts/local-stack.sh screenshot  # authed 1440 + 393 screenshots of the seeded doc
#   scripts/local-stack.sh status      # show what's running
#   scripts/local-stack.sh teardown    # stop everything, verify ports are free
#   scripts/local-stack.sh selftest    # negative-control test of the screenshot helper
#                                       # (no stack needed — safe to run any time)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$ROOT_DIR/.local-stack"
WEB_DIR="$ROOT_DIR/web"

# Non-standard ports on purpose (Trap 1, measured live 2026-08-20: port 8005
# is routinely occupied by another agent's own dev API on this Mac Mini, and
# that API shares the checked-in deploy/docker/mesh docker-compose postgres —
# so reusing default ports risks either colliding or, worse, quietly reading
# someone else's running stack). Override any of these via env if they clash
# with you specifically.
PG_PORT="${LOCAL_STACK_PG_PORT:-55432}"
REDIS_PORT="${LOCAL_STACK_REDIS_PORT:-56379}"
API_PORT="${LOCAL_STACK_API_PORT:-8095}"
WEB_PORT="${LOCAL_STACK_WEB_PORT:-3007}"

# Container names used to be fixed constants regardless of port — so a second
# agent picking a free PG_PORT/REDIS_PORT (exactly what check_port_free's own
# error message told them to do) still collided on `docker rm -f` at the
# start of `up`, silently nuking the first agent's running stand (#5729d210,
# measured live 2026-08-21: stand A's containers were recreated out from
# under it at new ports by stand B, A's API then failed every DB call while
# `status` kept reporting green — see the reachability rewrite below).
# Scoping the name to the stack identifier means two stands never share a
# container name in the first place. Override LOCAL_STACK_NAME for a memorable
# name; it defaults to PG_PORT since that's already the per-agent knob.
STACK_ID="${LOCAL_STACK_NAME:-$PG_PORT}"
STACK_LABEL="mesh-local-stack"

PG_CONTAINER="mesh-local-stack-pg-${STACK_ID}"
REDIS_CONTAINER="mesh-local-stack-redis-${STACK_ID}"

SEED_EMAIL="local-stack@example.test"
SEED_PASSWORD="LocalStack1"
SEED_NAME="Local Stack"

API_URL="http://localhost:${API_PORT}"
WEB_URL="http://localhost:${WEB_PORT}"

log() { printf '[local-stack] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "'$1' is required but not on PATH"
}

port_owner() {
  # Prints the PID(s)/container listening on a port, or nothing if free.
  # lsof exits 1 when nothing matches — the common case — which under `set -e`
  # + pipefail would otherwise kill the whole script on every free-port check.
  lsof -nP -iTCP:"$1" -sTCP:LISTEN 2>/dev/null | awk 'NR>1{print $1, $2}' || true
}

check_port_free() {
  local port="$1" what="$2"
  local owner
  owner="$(port_owner "$port")"
  if [ -n "$owner" ]; then
    die "port ${port} (for ${what}) is already in use by: ${owner}. Set LOCAL_STACK_${what^^}_PORT to a free one — container names are now scoped to the port (or LOCAL_STACK_NAME), so 'up' only ever touches its own containers, never another agent's."
  fi
}

safe_rm_container() {
  # Removes a container by name, but only if it actually carries our stack
  # label — refuses instead of blindly `docker rm -f`ing whatever happens to
  # own that name. Belt-and-suspenders on top of the per-stack naming above:
  # naming already prevents cross-stack collisions in the normal case, this
  # catches the residual case of a same-named container that isn't ours.
  local name="$1"
  local existing
  existing="$(docker ps -aq --filter "name=^${name}\$" 2>/dev/null || true)"
  [ -z "$existing" ] && return 0
  local label
  label="$(docker inspect --format "{{ index .Config.Labels \"${STACK_LABEL}\" }}" "$name" 2>/dev/null || true)"
  if [ "$label" != "$STACK_ID" ]; then
    die "container ${name} exists but is not labeled ${STACK_LABEL}=${STACK_ID} (found: '${label:-<none>}') — refusing to remove a container that may not be ours. Investigate manually: docker inspect ${name}"
  fi
  docker rm -f "$name" >/dev/null 2>&1 || true
}

wait_for_http() {
  local url="$1" label="$2" timeout="${3:-60}"
  local waited=0
  until curl -fsS -o /dev/null "$url" 2>/dev/null; do
    sleep 1
    waited=$((waited + 1))
    if [ "$waited" -ge "$timeout" ]; then
      die "$label did not become ready at $url within ${timeout}s"
    fi
  done
  log "$label ready ($url, ${waited}s)"
}

cmd_up() {
  require_cmd docker
  require_cmd go
  require_cmd curl
  require_cmd jq
  require_cmd pnpm

  mkdir -p "$STATE_DIR"

  check_port_free "$PG_PORT" pg
  check_port_free "$REDIS_PORT" redis
  check_port_free "$API_PORT" api
  check_port_free "$WEB_PORT" web

  log "starting throwaway postgres (port ${PG_PORT}) and redis (port ${REDIS_PORT}), stack id ${STACK_ID}"
  safe_rm_container "$PG_CONTAINER"
  safe_rm_container "$REDIS_CONTAINER"
  docker run -d --name "$PG_CONTAINER" --label "${STACK_LABEL}=${STACK_ID}" \
    -e POSTGRES_USER=mesh -e POSTGRES_PASSWORD=mesh -e POSTGRES_DB=mesh \
    -p "${PG_PORT}:5432" postgres:16-alpine >/dev/null
  docker run -d --name "$REDIS_CONTAINER" --label "${STACK_LABEL}=${STACK_ID}" \
    -p "${REDIS_PORT}:6379" redis:7-alpine >/dev/null

  log "waiting for postgres to accept connections"
  local waited=0
  until docker exec "$PG_CONTAINER" pg_isready -U mesh >/dev/null 2>&1; do
    sleep 1
    waited=$((waited + 1))
    [ "$waited" -ge 30 ] && die "postgres did not become ready in 30s"
  done
  log "postgres ready"

  log "building mesh-api"
  (cd "$ROOT_DIR" && go build -o "$STATE_DIR/mesh-api" ./cmd/api)

  log "starting API on ${API_URL} (migrations apply automatically on boot)"
  (
    cd "$ROOT_DIR" && \
    SERVER_PORT="$API_PORT" \
    DB_HOST=localhost DB_PORT="$PG_PORT" DB_USER=mesh DB_PASSWORD=mesh DB_NAME=mesh DB_SSL_MODE=disable \
    REDIS_HOST=localhost REDIS_PORT="$REDIS_PORT" \
    JWT_SECRET=local-dev-secret-not-for-prod \
    MESH_ALLOW_REGISTRATION=true \
    MESH_CORS_ORIGINS="$WEB_URL" \
    MESH_RATE_LIMIT_ENABLED=false \
    nohup "$STATE_DIR/mesh-api" > "$STATE_DIR/api.log" 2>&1 &
    echo $! > "$STATE_DIR/api.pid"
  )
  wait_for_http "$API_URL/health" "API" 45

  seed_data

  log "starting vite dev server on ${WEB_URL} (same-origin proxy → API on ${API_PORT}, avoids the CORS+credentials trap)"
  (
    cd "$WEB_DIR" && \
    VITE_API_URL= VITE_DEV_API_PORT="$API_PORT" \
    nohup pnpm exec vite --port "$WEB_PORT" --strictPort > "$STATE_DIR/vite.log" 2>&1 &
    echo $! > "$STATE_DIR/vite.pid"
  )
  wait_for_http "$WEB_URL/" "vite dev server" 45

  echo
  log "=== local stand is up — zero prod credentials used ==="
  log "URL:      ${WEB_URL}/login"
  log "email:    ${SEED_EMAIL}"
  log "password: ${SEED_PASSWORD}"
  log "seeded doc: $(jq -r .doc_url "$STATE_DIR/seed.json")"
  log "next: scripts/local-stack.sh screenshot   (or log in yourself in a real browser)"
  log "when done: scripts/local-stack.sh teardown"
}

seed_data() {
  log "seeding: user, workspace, project, document, comments (reply / resolved / whole-page thread)"

  local reg
  reg="$(curl -fsS -X POST "$API_URL/api/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg e "$SEED_EMAIL" --arg p "$SEED_PASSWORD" --arg n "$SEED_NAME" \
      '{email:$e, password:$p, name:$n}')")" || die "register failed"
  local token
  token="$(echo "$reg" | jq -r '.tokens.access_token')"
  [ -n "$token" ] && [ "$token" != "null" ] || die "register did not return an access token: $reg"

  auth() { curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' "$@"; }

  local ws_id ws_slug
  local workspaces
  workspaces="$(auth "$API_URL/api/v1/workspaces")" || die "GET /workspaces failed"
  ws_id="$(echo "$workspaces" | jq -r '.[0].id')"
  ws_slug="$(echo "$workspaces" | jq -r '.[0].slug')"
  [ -n "$ws_id" ] && [ "$ws_id" != "null" ] || die "no default workspace after register: $workspaces"

  local proj
  proj="$(auth -X POST "$API_URL/api/v1/workspaces/$ws_id/projects" \
    -d '{"name":"Local Stack Demo","slug":"local-stack-demo"}')" || die "create project failed"
  local proj_id proj_slug
  proj_id="$(echo "$proj" | jq -r '.id')"
  proj_slug="$(echo "$proj" | jq -r '.slug')"
  [ -n "$proj_id" ] && [ "$proj_id" != "null" ] || die "create project did not return an id: $proj"

  # Enough paragraphs to be worth scrolling — an empty page proves nothing
  # about a screenshot helper (§1k: "нечего смотреть на пустой странице").
  local body
  body="$(cat <<'MD'
# Welcome to the local stand

This document exists only to have something worth commenting on and worth
scrolling — the whole point of scripts/local-stack.sh is to never again need
prod credentials just to see whether a UI change landed where it should.

## First section

Paragraph one carries the quote that the first comment thread points at, so
resolving it and re-anchoring it after an edit are both exercised by hand
whenever this document is used for that kind of testing.

Paragraph two is filler, present only to push the next section far enough
down the page that a naive `fullPage: true` screenshot — one that captures
just the outer browser viewport rather than the actual scrolling content
area — would visibly fail to show it. That gap is exactly what
screenshot.mjs's viewport-growth step exists to close.

## Second section

Paragraph three carries the quote that the second comment thread points at.
That thread gets resolved by the seed script, so a screenshot taken with
"show resolved" toggled on and off looks meaningfully different.

Paragraph four is more filler, for the same reason as paragraph two: give the
page real height before anyone screenshots it.

## Closing

The seed also adds a third thread with no anchor at all — a comment on the
document as a whole rather than any particular sentence — because that is a
real, supported shape and a screenshot helper that only ever sees anchored
threads hasn't actually been exercised against the page.
MD
)"

  local doc
  doc="$(auth -X POST "$API_URL/api/v1/projects/$proj_id/documents" \
    -d "$(jq -n --arg t "Welcome to the local stand" --arg s "welcome" --arg b "$body" \
      '{title:$t, slug:$s, body:$b}')")" || die "create document failed"
  local doc_id
  doc_id="$(echo "$doc" | jq -r '.id')"
  [ -n "$doc_id" ] && [ "$doc_id" != "null" ] || die "create document did not return an id: $doc"

  docauth() { curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' "$@"; }

  # Thread 1: anchored comment + a nested reply.
  local c1 c1_id
  c1="$(docauth -X POST "$API_URL/api/v1/documents/$doc_id/comments" \
    -d '{"quote":"resolving it and re-anchoring it after an edit are both exercised by hand","body":"Does this survive a re-anchor after the paragraph is edited?"}')" \
    || die "comment 1 failed"
  c1_id="$(echo "$c1" | jq -r '.id')"
  docauth -X POST "$API_URL/api/v1/documents/$doc_id/comments" \
    -d "$(jq -n --arg p "$c1_id" '{parent_comment_id:$p, body:"Agreed — this is the exact reply-nesting case."}')" \
    >/dev/null || die "reply to comment 1 failed"

  # Thread 2: anchored comment, then resolved.
  local c2 c2_id
  c2="$(docauth -X POST "$API_URL/api/v1/documents/$doc_id/comments" \
    -d '{"quote":"That thread gets resolved by the seed script","body":"Confirmed, marking resolved."}')" \
    || die "comment 2 failed"
  c2_id="$(echo "$c2" | jq -r '.id')"
  docauth -X POST "$API_URL/api/v1/document-comments/$c2_id/resolve" -d '{}' >/dev/null \
    || die "resolving comment 2 failed"

  # Thread 3: no anchor at all — a whole-document comment.
  docauth -X POST "$API_URL/api/v1/documents/$doc_id/comments" \
    -d '{"body":"General note on the whole page, no particular sentence."}' >/dev/null \
    || die "whole-page comment failed"

  local doc_url="${WEB_URL}/w/${ws_slug}/p/${proj_slug}/docs/${doc_id}"
  jq -n \
    --arg email "$SEED_EMAIL" --arg password "$SEED_PASSWORD" \
    --arg login_url "${WEB_URL}/login" --arg doc_url "$doc_url" \
    --arg ws_slug "$ws_slug" --arg proj_slug "$proj_slug" --arg doc_id "$doc_id" \
    '{email:$email, password:$password, login_url:$login_url, doc_url:$doc_url, ws_slug:$ws_slug, proj_slug:$proj_slug, doc_id:$doc_id}' \
    > "$STATE_DIR/seed.json"
  log "seeded: $doc_url"
}

cmd_screenshot() {
  [ -f "$STATE_DIR/seed.json" ] || die "no seed.json — run 'scripts/local-stack.sh up' first"
  require_cmd node
  local login_url doc_url email password
  login_url="$(jq -r .login_url "$STATE_DIR/seed.json")"
  doc_url="$(jq -r .doc_url "$STATE_DIR/seed.json")"
  email="$(jq -r .email "$STATE_DIR/seed.json")"
  password="$(jq -r .password "$STATE_DIR/seed.json")"

  mkdir -p "$STATE_DIR/screenshots"
  (cd "$WEB_DIR" && node scripts/local-stack/screenshot.mjs \
    --login-url "$login_url" --email "$email" --password "$password" \
    --url "$doc_url" \
    --widths "${LOCAL_STACK_SCREENSHOT_WIDTHS:-1440,393}" \
    --out-dir "$STATE_DIR/screenshots" \
    --prefix doc \
    --assert-visible "article,[data-testid=doc-comment-thread]")
  log "screenshots in $STATE_DIR/screenshots"
}

cmd_selftest() {
  require_cmd node
  (cd "$WEB_DIR" && node scripts/local-stack/screenshot-selftest.mjs)
}

cmd_status() {
  # Checks REACHABILITY, not "does a pid/container exist" — the latter stayed
  # green on a stand whose postgres container had been ripped out from under
  # it by another agent (#5729d210: `status` said "running" / "Up" on every
  # line while the API was 500ing on every request, only api.log showed the
  # truth). A container that exists but refuses connections, or a pid that's
  # alive but wedged, must print red here.
  local red green reset
  red=$'\033[31m'; green=$'\033[32m'; reset=$'\033[0m'
  ok() { printf '  %s%s%s\n' "$green" "$1" "$reset"; }
  bad() { printf '  %sDOWN — %s%s\n' "$red" "$1" "$reset"; }

  echo "postgres (${PG_CONTAINER}, port ${PG_PORT}):"
  if docker exec "$PG_CONTAINER" pg_isready -U mesh >/dev/null 2>&1; then
    ok "up, accepting connections"
  else
    bad "container missing or not accepting connections"
  fi

  echo "redis (${REDIS_CONTAINER}, port ${REDIS_PORT}):"
  if [ "$(docker exec "$REDIS_CONTAINER" redis-cli ping 2>/dev/null)" = "PONG" ]; then
    ok "up, responding to PING"
  else
    bad "container missing or not responding"
  fi

  echo "API (${API_URL}):"
  if curl -fsS -o /dev/null "$API_URL/health" 2>/dev/null; then
    ok "up, /health OK"
  else
    bad "/health unreachable"
  fi

  echo "vite (${WEB_URL}):"
  if curl -fsS -o /dev/null "$WEB_URL/" 2>/dev/null; then
    ok "up, responding"
  else
    bad "not responding"
  fi

  [ -f "$STATE_DIR/seed.json" ] && { echo "seeded doc:"; jq -r '"  " + .doc_url' "$STATE_DIR/seed.json"; }
}

cmd_teardown() {
  log "stopping vite and API"
  for pidfile in "$STATE_DIR/vite.pid" "$STATE_DIR/api.pid"; do
    if [ -f "$pidfile" ]; then
      local pid
      pid="$(cat "$pidfile")"
      kill "$pid" >/dev/null 2>&1 || true
      rm -f "$pidfile"
    fi
  done
  # Belt and suspenders: vite forks a child (esbuild); kill anything still
  # bound to our ports rather than trusting the parent pid alone.
  for port in "$API_PORT" "$WEB_PORT"; do
    local pids
    pids="$(lsof -ti "tcp:$port" 2>/dev/null || true)"
    [ -n "$pids" ] && kill -9 $pids >/dev/null 2>&1 || true
  done

  log "removing postgres and redis containers (stack id ${STACK_ID})"
  safe_rm_container "$PG_CONTAINER"
  safe_rm_container "$REDIS_CONTAINER"

  sleep 1
  local remaining=""
  for port in "$PG_PORT" "$REDIS_PORT" "$API_PORT" "$WEB_PORT"; do
    [ -n "$(port_owner "$port")" ] && remaining="$remaining $port"
  done
  if [ -n "$remaining" ]; then
    die "teardown incomplete — still occupied:$remaining"
  fi
  log "teardown complete, all four ports (${PG_PORT} ${REDIS_PORT} ${API_PORT} ${WEB_PORT}) confirmed free"
}

case "${1:-}" in
  up) cmd_up ;;
  seed) mkdir -p "$STATE_DIR"; seed_data ;;
  screenshot) cmd_screenshot ;;
  status) cmd_status ;;
  teardown) cmd_teardown ;;
  selftest) cmd_selftest ;;
  *)
    echo "usage: $0 {up|screenshot|status|teardown|selftest}" >&2
    exit 1
    ;;
esac
