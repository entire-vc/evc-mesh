#!/usr/bin/env bash
#
# Guards the class of defect that broke the 2026-07-29 upgrade of an external
# self-host instance: a compose variable declared `${VAR:?...}` — required,
# no default — that did not exist in .env.prod.example when that operator
# installed. Compose refuses to parse the file, so `docker compose up` fails
# before it touches a container, and every existing install is stuck until
# someone edits .env by hand. Nothing on our own prod ever notices, because
# our environment has had the variable all along.
#
# The rule this enforces: the set of required-without-default variables is
# fixed and enumerated below. Adding one is not forbidden — it is made
# deliberate. If you add a `:?` variable you must add it here too, and by
# doing so you are asserting that every existing installation already has it
# set, i.e. that it shipped in .env.prod.example strictly before it became
# required. If it did not, give it a default instead and let the stack cope
# with the empty case.
#
# Each variable is also checked to appear in .env.prod.example, so `diff`
# against a running install still surfaces what is missing.
#
# Run: deploy/docker/mesh/check-required-env.sh
set -euo pipefail

cd "$(dirname "$0")"

COMPOSE_FILE=docker-compose.prod.yml
EXAMPLE_FILE=.env.prod.example

# Required since 7e51b4e ("Docker-first deployment for OSS self-hosting"),
# the first release anyone could install from, and present in
# .env.prod.example from that same commit. GRAFANA_PASSWORD only became
# `:?` later (#437) but shipped in the example from the start, so no
# existing install is missing the key.
ALLOWED_REQUIRED=(
  GRAFANA_PASSWORD
  JWT_SECRET
  MINIO_ACCESS_KEY
  MINIO_SECRET_KEY
  POSTGRES_PASSWORD
  REDIS_PASSWORD
)

actual=$(grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*:\?' "$COMPOSE_FILE" |
  sed -E 's/^\$\{//; s/:\?$//' | sort -u)

expected=$(printf '%s\n' "${ALLOWED_REQUIRED[@]}" | sort -u)

status=0

added=$(comm -23 <(printf '%s\n' "$actual") <(printf '%s\n' "$expected"))
if [ -n "$added" ]; then
  status=1
  echo "FAIL: $COMPOSE_FILE requires variables that are not in the allowlist:" >&2
  printf '  %s\n' $added >&2
  cat >&2 <<'EOF'

  A newly required variable with no default breaks `docker compose up` for
  every existing install that does not already have it — at config-parse
  time, before anything starts. Either:

    - give it a default:  ${VAR:-sensible-default}, or have the stack
      generate it (see the init-secrets service), or
    - if every existing install genuinely already sets it, add it to
      ALLOWED_REQUIRED in this script and say why in the comment.

EOF
fi

removed=$(comm -13 <(printf '%s\n' "$actual") <(printf '%s\n' "$expected"))
if [ -n "$removed" ]; then
  status=1
  echo "FAIL: allowlisted variables are no longer required by $COMPOSE_FILE:" >&2
  printf '  %s\n' $removed >&2
  echo "  Drop them from ALLOWED_REQUIRED so this list keeps meaning something." >&2
fi

for var in $actual; do
  if ! grep -qE "^${var}=" "$EXAMPLE_FILE"; then
    status=1
    echo "FAIL: $var is required by $COMPOSE_FILE but absent from $EXAMPLE_FILE." >&2
    echo "  Operators diff their .env against that file to find what is missing." >&2
  fi
done

if [ "$status" -eq 0 ]; then
  echo "OK: $(printf '%s\n' "$actual" | wc -l | tr -d ' ') required variables, all allowlisted and documented in $EXAMPLE_FILE."
fi

exit "$status"
