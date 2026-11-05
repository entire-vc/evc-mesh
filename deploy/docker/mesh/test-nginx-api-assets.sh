#!/bin/sh
#
# Behavioural test for nginx location precedence: /api/ paths that end in a
# static-asset extension (content-addressed theme CSS/JS) must reach the API,
# not be answered from /srv/web by the static-asset regex location.
#
# How: runs inside the nginx image (CI job nginx-api-assets). The config under
# test serves :80; a stub "api" on 127.0.0.1:8005 answers 200 with an
# "api-stub" marker (and echoes Authorization), so a request that reached the
# static location is told apart by body content, not only by status.
#
# Run:  sh deploy/docker/mesh/test-nginx-api-assets.sh [path/to/nginx.conf]
# Red control: pass a copy of nginx.conf with `^~ ` removed from `location ^~ /api/`;
# the test must fail.
set -eu

CONF=${1:-$(dirname "$0")/nginx.conf}

grep -q '[[:space:]]minio$' /etc/hosts || echo "127.0.0.1 api minio" >> /etc/hosts
cp "$CONF" /etc/nginx/conf.d/default.conf
sh "$(dirname "$0")/25-mesh-trusted-proxies.sh"
mkdir -p /srv/web
echo '<html>spa-shell</html>' > /srv/web/index.html
echo 'body{}' > /srv/web/app.css
cat > /etc/nginx/conf.d/zz-api-stub.conf <<'EOF2'
server {
    listen 127.0.0.1:8005;
    location / {
        add_header X-Seen-Auth "$http_authorization" always;
        return 200 "api-stub $request_uri\n";
    }
}
EOF2

nginx -t
nginx
trap 'nginx -s stop 2>/dev/null || true' EXIT
sleep 1

fail=0
# $1 label, $2 status fragment, $3 body fragment, rest wget args (URL last)
check() {
  label=$1 want=$2 body=$3
  shift 3
  out=$(wget -S -O - "$@" 2>&1 || true)
  status=$(printf '%s\n' "$out" | grep -m1 'HTTP/' || true)
  case $status in *"$want"*) ;; *) echo "FAIL $label: want $want, got '$status'" >&2; fail=1; return ;; esac
  case $out in *"$body"*) echo "PASS $label: $status" ;; *) echo "FAIL $label: body lacks '$body'" >&2; fail=1 ;; esac
}

H=0fc8a4fa213156ef7953f2bd227f03c792e1a762f0cb12264656199d80b0d24f
check "api css asset reaches API" 200 "api-stub /api/v1/theme/assets/$H.css" "http://127.0.0.1/api/v1/theme/assets/$H.css"
check "api js asset reaches API"  200 "api-stub /api/v1/theme/assets/$H.js"  "http://127.0.0.1/api/v1/theme/assets/$H.js"
check "api json path reaches API" 200 "api-stub" "http://127.0.0.1/api/v1/theme/manifest.json"
check "Authorization kept on api asset" 200 "X-Seen-Auth: Bearer t" --header 'Authorization: Bearer t' "http://127.0.0.1/api/v1/theme/assets/$H.css"
check "extensionless api path reaches API" 200 "api-stub" "http://127.0.0.1/api/v1/theme/assets/not-a-hash"
# contract kept outside /api/
check "static file still served from web root" 200 "body{}" "http://127.0.0.1/app.css"
got=$(wget -S -O /dev/null http://127.0.0.1/missing.js 2>&1 | grep -m1 'HTTP/' || true)
case $got in *404*) echo "PASS missing static file 404: $got" ;; *) echo "FAIL missing static file: '$got'" >&2; fail=1 ;; esac
check "SPA route falls back to index" 200 "spa-shell" "http://127.0.0.1/some/spa/route"
check "oauth reaches API" 200 "api-stub /oauth/token" "http://127.0.0.1/oauth/token"

exit $fail
