#!/usr/bin/env bash
# Live regression check: fresh stack, readable seeded document and comments,
# then teardown (including object storage). Override LOCAL_STACK_* for isolation.
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

token="$(curl -fsS -X POST "$API_URL/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq '{email,password}' "$STATE_DIR/seed.json")" | jq -er '.tokens.access_token')"
doc_id="$(jq -er '.doc_id' "$STATE_DIR/seed.json")"
curl -fsS "$API_URL/documents/$doc_id" -H "Authorization: Bearer $token" \
  | jq -e '.title == "Welcome to the local stand" and (.body | contains("## Closing"))' >/dev/null
curl -fsS "$API_URL/documents/$doc_id/comments?include_resolved=true" -H "Authorization: Bearer $token" \
  | jq -e '(.items | length) == 4 and any(.items[]; .parent_comment_id != null) and any(.items[]; .resolved_at != null) and any(.items[]; .body == "General note on the whole page, no particular sentence.")' >/dev/null
curl -fsS "http://localhost:${LOCAL_STACK_WEB_PORT:-3007}/login" \
  | grep 'src="/src/main.tsx"' >/dev/null
bash "$STACK" status
bash "$STACK" teardown
trap - EXIT
echo 'PASS: up, stored document body, four comments, web entry and teardown'
