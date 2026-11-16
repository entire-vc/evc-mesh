#!/bin/sh
#
# Behavioural test: OpenID Connect discovery and JWKS must reach the API, not
# the SPA. Without its own location
# /.well-known/openid-configuration falls through to `location /` and answers
# 200 text/html, which an OIDC client reads as "no provider here".
#
# How: runs inside the nginx image (CI job nginx-oidc-discovery). The config
# under test serves :80; a stub "api" on 127.0.0.1:8005 answers with an
# "api-stub" marker, so a request that reached the SPA is told apart by body.
#
# Run:  sh deploy/docker/mesh/test-nginx-oidc-discovery.sh [path/to/nginx.conf]
# Red control: pass a copy of nginx.conf without the
# `location = /.well-known/openid-configuration` block; the test must fail.
set -eu

CONF=${1:-$(dirname "$0")/nginx.conf}

grep -q '[[:space:]]minio$' /etc/hosts || echo "127.0.0.1 api minio" >> /etc/hosts
cp "$CONF" /etc/nginx/conf.d/default.conf
sh "$(dirname "$0")/25-mesh-trusted-proxies.sh"
mkdir -p /srv/web
echo '<html>spa-shell</html>' > /srv/web/index.html
cat > /etc/nginx/conf.d/zz-api-stub.conf <<'EOF2'
server {
    listen 127.0.0.1:8005;
    location / {
        return 200 "api-stub $request_uri\n";
    }
}
EOF2

nginx -t
nginx
trap 'nginx -s stop 2>/dev/null || true' EXIT
sleep 1

fail=0
# $1 label, $2 body fragment, $3 URL
check() {
  out=$(wget -q -O - "$3" 2>&1 || true)
  case $out in *"$2"*) echo "PASS $1" ;; *) echo "FAIL $1: body '$out' lacks '$2'" >&2; fail=1 ;; esac
}

check "openid discovery reaches API" "api-stub /.well-known/openid-configuration" "http://127.0.0.1/.well-known/openid-configuration"
check "jwks reaches API" "api-stub /oauth/jwks" "http://127.0.0.1/oauth/jwks"
check "RFC 8414 metadata still reaches API" "api-stub /.well-known/oauth-authorization-server" "http://127.0.0.1/.well-known/oauth-authorization-server"
check "consent screen stays with the SPA" "spa-shell" "http://127.0.0.1/connect/consent?client_id=x"

exit $fail
