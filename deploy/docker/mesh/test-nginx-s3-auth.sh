#!/bin/sh
#
# Behavioural test for the /s3/ proxy of the bundled nginx: a request that
# carries an Authorization header must reach MinIO without it.
#
# Why: the SPA downloads a chat attachment with its Bearer token, the API
# answers 302 to a presigned URL on the same origin (/s3/...), and a browser
# keeps Authorization across a same-origin redirect. MinIO refuses a presigned
# URL that also has an Authorization header with 400 "request has multiple
# authentication types", so the image never rendered.
#
# How: runs inside the nginx image itself (no Docker daemon needed, CI job
# nginx-s3-auth). The config under test is served on :80; a stub "minio" on
# 127.0.0.1:9000 answers 400 when it sees Authorization, 200 otherwise.
#
# Run:  sh deploy/docker/mesh/test-nginx-s3-auth.sh [path/to/nginx.conf]
# Needs: root in an nginx:alpine container ("api" and "minio" resolve to
# 127.0.0.1 via /etc/hosts; the script adds them).
#
# Red control: pass a copy of nginx.conf without the
# `proxy_set_header Authorization "";` line; the test must fail.
set -eu

CONF=${1:-$(dirname "$0")/nginx.conf}

grep -q '[[:space:]]minio$' /etc/hosts || echo "127.0.0.1 api minio" >> /etc/hosts
cp "$CONF" /etc/nginx/conf.d/default.conf
sh "$(dirname "$0")/25-mesh-trusted-proxies.sh"
cat > /etc/nginx/conf.d/zz-minio-stub.conf <<'EOF'
server {
    listen 127.0.0.1:9000;
    location / {
        if ($http_authorization) { return 400 "multiple authentication types\n"; }
        return 200 "object\n";
    }
}
EOF

nginx -t
nginx
trap 'nginx -s stop 2>/dev/null || true' EXIT
sleep 1

fail=0
check() { # $1 = label, $2 = expected status line fragment, rest = wget args
  label=$1 want=$2
  shift 2
  got=$(wget -S -O /dev/null "$@" 2>&1 | grep -m1 'HTTP/' || true)
  case $got in
    *"$want"*) echo "PASS $label: $got" ;;
    *) echo "FAIL $label: want $want, got '$got'" >&2; fail=1 ;;
  esac
}

URL='http://127.0.0.1/s3/mesh-artifacts/chat/a/b?X-Amz-Signature=x'
check "presigned GET, no Authorization" "200" "$URL"
check "presigned GET with Bearer (same-origin redirect)" "200" --header 'Authorization: Bearer token' "$URL"

exit $fail
