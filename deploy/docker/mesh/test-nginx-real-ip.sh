#!/usr/bin/env bash
#
# Behavioural test for the client-IP boundary of the bundled nginx
# (deploy/docker/mesh/nginx.conf + 25-mesh-trusted-proxies.sh).
#
# What it guards: mesh-mcp keys its per-IP auth-failure budget on the LEFTMOST
# X-Forwarded-For entry (evc-mesh-mcp ratelimit.go clientIP). If this nginx
# appends to whatever the client sent ($proxy_add_x_forwarded_for), a directly
# reachable instance lets any client (a) mint a fresh bucket per request with a
# random XFF -> the limiter limits nothing, and (b) burn another address's
# budget by sending it as XFF. The fix is that the backend never sees a value a
# client wrote: XFF is honoured only from a trusted hop, resolved to a single
# address by nginx's realip module, and re-emitted as $remote_addr.
#
# How: a real nginx container (stock image + the files under test mounted in)
# in front of an echo backend, on Docker networks whose subnets decide whether
# the calling client counts as trusted. 198.51.100.0/24 (TEST-NET-2) is public
# for our purposes, i.e. NOT in the default trusted set; 10.201.77.0/24 is
# private, i.e. IS in it.
#
# Run:  deploy/docker/mesh/test-nginx-real-ip.sh
# Needs a running Docker daemon and the nginx image (pulled if absent).
# Override the config under test: NGINX_CONF=/path/to/nginx.conf
#
# Red control: run it with NGINX_CONF pointing at the pre-fix nginx.conf; the
# spoofing assertions must fail. A test that has never been seen failing is
# not evidence.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
CONF=${NGINX_CONF:-$HERE/nginx.conf}
RENDER=${NGINX_RENDER_SCRIPT:-$HERE/25-mesh-trusted-proxies.sh}
IMG=${NGINX_IMAGE:-nginx:1.27-alpine}
ID=meshrealip$$
NET_PUB=$ID-pub
NET_PRIV=$ID-priv
FAILS=0
# Under $HOME, not /tmp: Docker Desktop on macOS only bind-mounts shared paths.
mkdir -p "$HOME/.cache"
TMP=$(mktemp -d "$HOME/.cache/mesh-realip-test.XXXXXX")

cleanup() {
  docker rm -f "$ID-backend" "$ID-nginx" >/dev/null 2>&1 || true
  docker network rm "$NET_PUB" "$NET_PRIV" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAILS=$((FAILS + 1)); }

# expect_eq <label> <want> <got>
expect_eq() {
  if [ "$2" = "$3" ]; then pass "$1 (got '$3')"; else fail "$1: want '$2', got '$3'"; fi
}

# Echo backend: answers on the ports the bundled nginx proxies to and reports
# the X-Forwarded-For it actually received. Aliased as api/minio/mcp so the
# literal upstreams in nginx.conf resolve.
cat >"$TMP/backend.conf" <<'EOF'
server {
    listen 8005;
    listen 9000;
    listen 8081;
    location / { return 200 "$http_x_forwarded_for\n"; }
}
EOF

docker network create --subnet 198.51.100.0/24 "$NET_PUB" >/dev/null
docker network create --subnet 10.201.77.0/24 "$NET_PRIV" >/dev/null

docker run -d --name "$ID-backend" --network "$NET_PUB" \
  --network-alias api --network-alias minio --network-alias mcp \
  -v "$TMP/backend.conf:/etc/nginx/conf.d/default.conf:ro" "$IMG" >/dev/null
docker network connect --alias api --alias minio --alias mcp "$NET_PRIV" "$ID-backend"

# start_nginx [ENV=VAL ...] — (re)creates the nginx under test.
start_nginx() {
  docker rm -f "$ID-nginx" >/dev/null 2>&1 || true
  local mounts=(-v "$CONF:/etc/nginx/conf.d/default.conf:ro")
  [ -f "$RENDER" ] && mounts+=(-v "$RENDER:/docker-entrypoint.d/25-mesh-trusted-proxies.sh:ro")
  local envs=()
  for kv in "$@"; do envs+=(-e "$kv"); done
  docker run -d --name "$ID-nginx" --network "$NET_PUB" --ip 198.51.100.10 \
    "${envs[@]}" "${mounts[@]}" "$IMG" >/dev/null
  docker network connect --ip 10.201.77.10 "$NET_PRIV" "$ID-nginx"
  for _ in $(seq 1 30); do
    docker exec "$ID-nginx" sh -c 'nginx -t' >/dev/null 2>&1 && \
      [ "$(docker inspect -f '{{.State.Running}}' "$ID-nginx")" = true ] && return 0
    sleep 0.3
  done
  docker logs "$ID-nginx" 2>&1 | tail -20
  return 1
}

# call <network> <client-ip> <nginx-ip> <path> [XFF] — prints the XFF the
# backend received, or NONE.
call() {
  local net=$1 cip=$2 nip=$3 path=$4 xff=${5-}
  local hdr=()
  [ -n "$xff" ] && hdr=(--header "X-Forwarded-For: $xff")
  local out
  out=$(docker run --rm --network "$net" --ip "$cip" "$IMG" \
    wget -q -O- -T 5 "${hdr[@]}" "http://$nip$path" 2>/dev/null | tr -d '\r\n' || true)
  # An empty answer is a failed request, never a value two calls can "agree" on.
  echo "${out:-NONE}"
}

PUB_CLIENT=198.51.100.50
PUB_NGINX=198.51.100.10
PRIV_CLIENT=10.201.77.50
PRIV_NGINX=10.201.77.10

echo "== 1. untrusted client (public address), MESH_TRUSTED_PROXIES unset"
start_nginx
for path in /mcp/sse /mcp /.well-known/oauth-protected-resource /api/x /ws; do
  a=$(call "$NET_PUB" "$PUB_CLIENT" "$PUB_NGINX" "$path" "10.9.9.9")
  b=$(call "$NET_PUB" "$PUB_CLIENT" "$PUB_NGINX" "$path" "10.8.8.8")
  expect_eq "$path: spoofed XFF A is replaced by the real peer" "$PUB_CLIENT" "$a"
  expect_eq "$path: spoofed XFF B lands in the SAME bucket as A"  "$a" "$b"
done
expect_eq "no XFF at all resolves to the peer" "$PUB_CLIENT" \
  "$(call "$NET_PUB" "$PUB_CLIENT" "$PUB_NGINX" /mcp/sse)"

echo "== 2. trusted edge (private address, default trust set)"
expect_eq "edge-supplied client IP is honoured" "203.0.113.9" \
  "$(call "$NET_PRIV" "$PRIV_CLIENT" "$PRIV_NGINX" /mcp/sse "203.0.113.9")"
expect_eq "spoofed prefix before the edge-appended entry is dropped" "203.0.113.9" \
  "$(call "$NET_PRIV" "$PRIV_CLIENT" "$PRIV_NGINX" /mcp/sse "6.6.6.6, 203.0.113.9")"
expect_eq "same on /api/" "203.0.113.9" \
  "$(call "$NET_PRIV" "$PRIV_CLIENT" "$PRIV_NGINX" /api/x "6.6.6.6, 203.0.113.9")"

echo "== 3. MESH_TRUSTED_PROXIES adds a public edge to the trust set"
start_nginx MESH_TRUSTED_PROXIES=198.51.100.0/24,192.0.2.0/24
expect_eq "public trusted edge: client IP honoured" "203.0.113.9" \
  "$(call "$NET_PUB" "$PUB_CLIENT" "$PUB_NGINX" /mcp/sse "6.6.6.6, 203.0.113.9")"

echo "== 4. malformed MESH_TRUSTED_PROXIES fails closed (nginx does not start)"
docker rm -f "$ID-nginx" >/dev/null 2>&1 || true
if [ -f "$RENDER" ]; then
  docker run -d --name "$ID-nginx" --network "$NET_PUB" \
    -e 'MESH_TRUSTED_PROXIES=1.2.3.0/24; allow all' \
    -v "$CONF:/etc/nginx/conf.d/default.conf:ro" \
    -v "$RENDER:/docker-entrypoint.d/25-mesh-trusted-proxies.sh:ro" "$IMG" >/dev/null
  sleep 2
  state=$(docker inspect -f '{{.State.Running}}' "$ID-nginx")
  expect_eq "container refused to start on an injected value" "false" "$state"
  if docker logs "$ID-nginx" 2>&1 | grep -q "refusing MESH_TRUSTED_PROXIES entry"; then
    pass "and it said why"
  else
    fail "container stopped, but not with the render script's refusal message"
  fi
else
  fail "render script $RENDER not found"
fi

echo
if [ "$FAILS" -eq 0 ]; then echo "ALL PASS"; else echo "$FAILS FAILED"; exit 1; fi
