#!/usr/bin/env bash
# Live regression check: fresh stack, api.pid naming the real API listener,
# document create over the API returning a real 201, readable seeded document
# and comments, then teardown (including object storage, no containers left).
# Override LOCAL_STACK_* for isolation.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STACK="$ROOT_DIR/scripts/local-stack.sh"
API_URL="http://localhost:${LOCAL_STACK_API_PORT:-8095}/api/v1"
STATE_DIR="$ROOT_DIR/.local-stack"
# up refuses occupied ports before creating anything; only clean up once those
# checks passed, so a failed smoke run cannot tear down an existing stand.
for port in "${LOCAL_STACK_PG_PORT:-55432}" "${LOCAL_STACK_REDIS_PORT:-56379}" \
  "${LOCAL_STACK_API_PORT:-8095}" "${LOCAL_STACK_WEB_PORT:-3007}" "${LOCAL_STACK_S3_PORT:-59002}"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "smoke: port $port is occupied; choose free LOCAL_STACK_* ports" >&2
    exit 1
  fi
done
trap 'bash "$STACK" teardown' EXIT
bash "$STACK" up
echo 'UP_EXIT=0'

# api.pid must name the process listening on the API port — a lying pidfile
# makes teardown's primary kill a no-op and orphans the server (#4b1131f7).
api_pid="$(cat "$STATE_DIR/api.pid")"
listener="$(lsof -nP -tiTCP:"${LOCAL_STACK_API_PORT:-8095}" -sTCP:LISTEN 2>/dev/null | sort -u | paste -sd ' ' - || true)"
[ "$api_pid" = "$listener" ] \
  || { echo "smoke: api.pid ($api_pid) is not the API listener ($listener)" >&2; exit 1; }

token="$(curl -fsS -X POST "$API_URL/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq '{email,password}' "$STATE_DIR/seed.json")" | jq -er '.tokens.access_token')"
doc_id="$(jq -er '.doc_id' "$STATE_DIR/seed.json")"

# Document creation must be a real 201, not just any 2xx — this is the exact
# call that 500'd when the stack had no object storage (#1646aa2a).
proj_id="$(jq -er '.proj_id' "$STATE_DIR/seed.json")"
status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  "$API_URL/projects/$proj_id/documents" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"title":"Smoke second document","slug":"smoke-second-doc","body":"smoke: assert document create is a real 201"}')"
[ "$status" = "201" ] \
  || { echo "smoke: document create returned $status, expected 201" >&2; exit 1; }
curl -fsS "$API_URL/documents/$doc_id" -H "Authorization: Bearer $token" \
  | jq -e '.title == "Welcome to the local stand" and (.body | contains("## Closing"))' >/dev/null
curl -fsS "$API_URL/documents/$doc_id/comments?include_resolved=true" -H "Authorization: Bearer $token" \
  | jq -e '(.items | length) == 4 and any(.items[]; .parent_comment_id != null) and any(.items[]; .resolved_at != null) and any(.items[]; .body == "General note on the whole page, no particular sentence.")' >/dev/null
curl -fsS "http://localhost:${LOCAL_STACK_WEB_PORT:-3007}/login" \
  | grep 'src="/src/main.tsx"' >/dev/null
bash "$STACK" status
bash "$STACK" teardown
# Teardown must leave nothing behind: the stack script itself dies loudly if
# any port is still occupied; here we add the container half of the contract.
leftover_containers="$(docker ps -aq --filter "label=mesh-local-stack=${LOCAL_STACK_NAME:-${LOCAL_STACK_PG_PORT:-55432}}" | wc -l | tr -d ' ')"
[ "$leftover_containers" = "0" ] \
  || { echo "smoke: $leftover_containers stack container(s) survived teardown" >&2; exit 1; }
trap - EXIT
echo 'PASS: up, api.pid == listener, document create 201, stored document body, four comments, web entry, teardown with no leftovers'
