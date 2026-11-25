#!/bin/sh
#
# Behavioural test for the upload body limits of the bundled nginx: an upload
# must reach its backend instead of nginx's default 1 MiB cap answering 413.
#
# Why: measured on the docker install on 2026-11-24, a presigned PUT of
# 1 100 000 bytes through /s3/ got 413 from nginx, so a file above 1 MiB could
# never be uploaded with the two-step upload; the multipart and base64 artifact
# uploads through /api/ hit the same default.
#
# How: runs inside the nginx image itself (no Docker daemon needed, CI job
# nginx-upload-size). The config under test is served on :80; stubs for
# "minio" (127.0.0.1:9000) and "api" (127.0.0.1:8005) accept any body and
# answer with a marker, so a pass proves the bytes arrived there.
#
# Run:  sh deploy/docker/mesh/test-nginx-upload-size.sh [path/to/nginx.conf]
# Needs: root in an nginx:alpine container (curl is in the image).
#
# Red control: pass a copy of nginx.conf without the client_max_body_size
# lines; the test must fail with 413.
set -eu

CONF=${1:-$(dirname "$0")/nginx.conf}

grep -q '[[:space:]]minio$' /etc/hosts || echo "127.0.0.1 api minio" >> /etc/hosts
cp "$CONF" /etc/nginx/conf.d/default.conf
sh "$(dirname "$0")/25-mesh-trusted-proxies.sh"
# The stubs lift their own cap and read the whole body, so a 413 can only come
# from the config under test, and their marker proves the request got through.
cat > /etc/nginx/conf.d/zz-upload-stubs.conf <<'EOF'
server {
    listen 127.0.0.1:9000;
    client_max_body_size 0;
    location / { return 200 "minio\n"; }
}
server {
    listen 127.0.0.1:8005;
    client_max_body_size 0;
    location / { return 200 "api\n"; }
}
EOF

nginx -t
nginx
trap 'nginx -s stop 2>/dev/null || true' EXIT
sleep 1

head -c 2097152 /dev/urandom > /tmp/upload-2mib.bin
head -c 35000000 /dev/zero > /tmp/upload-api.bin

fail=0
check() { # $1 = label, $2 = file, $3 = url, $4 = expected body
  label=$1 file=$2 url=$3 want=$4
  out=$(curl -sS -o /tmp/upload-resp -w '%{http_code}' -X PUT --data-binary "@$file" "$url" || true)
  body=$(cat /tmp/upload-resp 2>/dev/null || true)
  if [ "$out" = "200" ] && [ "$body" = "$want" ]; then
    echo "PASS $label: $out $body"
  else
    echo "FAIL $label: want 200 '$want', got $out '$body'" >&2
    fail=1
  fi
}

check "presigned PUT of 2 MiB through /s3/" /tmp/upload-2mib.bin \
  'http://127.0.0.1/s3/mesh-artifacts/uploads/ws/id?X-Amz-Signature=x' 'minio'
check "35 MB body through /api/ (base64 envelope of a 25 MiB artifact)" /tmp/upload-api.bin \
  'http://127.0.0.1/api/v1/tasks/t/artifacts' 'api'

exit $fail
