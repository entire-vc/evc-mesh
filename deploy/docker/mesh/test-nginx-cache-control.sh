#!/bin/sh
#
# Behavioural test for the cache policy of the shipped nginx config:
# the SPA shell (index.html, served for every extensionless
# route) and the unhashed files from web/public (sw.js, config.json,
# instance-theme.js) must carry `Cache-Control: no-cache`, or a browser keeps
# an old shell after a deploy and requests asset hashes that are gone (white
# screen). Hashed Vite chunks under /assets/ are immutable, but a missing
# chunk must never be cached as immutable. X-Robots-Tag from the
# server block must survive the per-location add_header.
#
# How: runs inside the nginx image (CI job nginx-cache-control) against a
# fake web root; headers are read with curl (busybox wget hides the headers
# of a 404, which would make the 404 checks vacuous).
#
# Run:  sh deploy/docker/mesh/test-nginx-cache-control.sh [path/to/nginx.conf]
# Red control: pass a copy of nginx.conf with every `add_header Cache-Control`
# line removed; the test must fail.
set -eu

CONF=${1:-$(dirname "$0")/nginx.conf}

grep -q '[[:space:]]minio$' /etc/hosts || echo "127.0.0.1 api minio" >> /etc/hosts
cp "$CONF" /etc/nginx/conf.d/default.conf
sh "$(dirname "$0")/25-mesh-trusted-proxies.sh"
mkdir -p /srv/web/assets /srv/web/theme-shared
echo '<html>spa-shell</html>' > /srv/web/index.html
echo 'self.addEventListener("fetch",()=>{})' > /srv/web/sw.js
echo '{}' > /srv/web/config.json
echo '/* theme */' > /srv/web/instance-theme.js
echo 'export{}' > /srv/web/theme-shared/react.js
echo 'console.log(1)' > /srv/web/assets/index-abc123.js

nginx -t
nginx
trap 'nginx -s stop 2>/dev/null || true' EXIT
sleep 1

fail=0
# $1 label, $2 path, $3 wanted status; rest: header lines that must appear
# ("+Header: value") or must not ("-fragment"), matched case-insensitively.
check() {
  label=$1 path=$2 want=$3
  shift 3
  out=$(curl -sS -D - -o /dev/null "http://127.0.0.1$path" 2>&1 | tr -d '\r' || true)
  status=$(printf '%s\n' "$out" | grep -m1 '^HTTP/' || true)
  case $status in *" $want "*) ;; *) echo "FAIL $label: want $want, got '$status'" >&2; fail=1; return ;; esac
  for h in "$@"; do
    frag=${h#?}
    case $h in
      +*) printf '%s\n' "$out" | grep -qi "^[[:space:]]*$frag" || { echo "FAIL $label: missing '$frag'" >&2; fail=1; return; } ;;
      -*) if printf '%s\n' "$out" | grep -qi "$frag"; then echo "FAIL $label: unexpected '$frag'" >&2; fail=1; return; fi ;;
    esac
  done
  echo "PASS $label: $status"
}

NC='+Cache-Control: no-cache'
RB='+X-Robots-Tag: noindex, nofollow'
check "SPA route /w/x revalidates"     /w/x                  200 "$NC" "$RB"
check "root / revalidates"             /                     200 "$NC" "$RB"
check "index.html revalidates"         /index.html           200 "$NC" "$RB"
check "sw.js revalidates"              /sw.js                200 "$NC" "$RB"
check "config.json revalidates"        /config.json          200 "$NC" "$RB"
check "instance-theme.js revalidates"  /instance-theme.js    200 "$NC" "$RB"
check "theme-shared shim revalidates"  /theme-shared/react.js 200 "$NC" "$RB"
check "hashed asset is immutable"      /assets/index-abc123.js 200 '+Cache-Control: public, max-age=31536000, immutable' "$RB"
check "missing asset 404, not immutable" /assets/index-gone.js 404 '-immutable' "$RB"
check "missing static 404, not immutable" /missing.js        404 '-immutable' "$RB"

exit $fail
