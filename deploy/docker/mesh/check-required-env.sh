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
# BOTH compose files are checked, not just the production one. The first
# version of this script hardcoded docker-compose.prod.yml, which made the
# gate blind to exactly the same defect one file over: docs/self-hosting.md
# presents docker-compose.yml as the default Quick Start path and names it
# again for local port config, so a variable made required there breaks those
# installs identically. A guard that covers one of two entry points is worse
# than none, because it gets believed and then stops being watched.
#
# Run: deploy/docker/mesh/check-required-env.sh
set -euo pipefail

cd "$(dirname "$0")"

COMPOSE_FILES=(docker-compose.prod.yml docker-compose.yml)
EXAMPLE_FILE=.env.prod.example

# Required since 7e51b4e ("Docker-first deployment for OSS self-hosting"),
# the first release anyone could install from, and present in
# .env.prod.example from that same commit. GRAFANA_PASSWORD only became
# `:?` later (#437) but shipped in the example from the start, so no
# existing install is missing the key.
#
# The allowlist is shared across both compose files: a variable required by
# either one must be listed here. It is not a per-file expectation — as of
# this writing docker-compose.yml requires none of them, and that is fine.
ALLOWED_REQUIRED=(
  GRAFANA_PASSWORD
  JWT_SECRET
  MINIO_ACCESS_KEY
  MINIO_SECRET_KEY
  POSTGRES_PASSWORD
  REDIS_PASSWORD
)

# Compose has two required-with-no-default forms and they fail identically:
# ${VAR:?msg} errors when VAR is unset OR empty, ${VAR?msg} only when unset.
# The colon is optional, so match it that way — an earlier version of this
# script required it and let the second form through silently.
#
# A file with no required variables is a legitimate result, not an error:
# grep exits 1 on no match, which under `set -o pipefail` would abort the
# script silently mid-loop. docker-compose.yml requires none today, so this
# is the normal path, not an edge case.
required_vars_in() {
  { grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*:?\?' "$1" || true; } |
    sed -E 's/^\$\{//; s/:?\?$//' | sort -u
}

expected=$(printf '%s\n' "${ALLOWED_REQUIRED[@]}" | sort -u)

status=0
all_actual=""

for compose_file in "${COMPOSE_FILES[@]}"; do
  actual=$(required_vars_in "$compose_file")
  all_actual=$(printf '%s\n%s' "$all_actual" "$actual")

  added=$(comm -23 <(printf '%s\n' "$actual") <(printf '%s\n' "$expected"))
  if [ -n "$added" ]; then
    status=1
    echo "FAIL: $compose_file requires variables that are not in the allowlist:" >&2
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

  for var in $actual; do
    if ! grep -qE "^${var}=" "$EXAMPLE_FILE"; then
      status=1
      echo "FAIL: $var is required by $compose_file but absent from $EXAMPLE_FILE." >&2
      echo "  Operators diff their .env against that file to find what is missing." >&2
    fi
  done
done

# Staleness is a property of the allowlist as a whole, not of either file: an
# entry earns its place by being required SOMEWHERE. Checking this per-file
# would fail the moment one compose file legitimately stopped needing a
# variable the other still requires.
union=$(printf '%s\n' "$all_actual" | grep -v '^$' | sort -u)
removed=$(comm -13 <(printf '%s\n' "$union") <(printf '%s\n' "$expected"))
if [ -n "$removed" ]; then
  status=1
  echo "FAIL: allowlisted variables are no longer required by any of ${COMPOSE_FILES[*]}:" >&2
  printf '  %s\n' $removed >&2
  echo "  Drop them from ALLOWED_REQUIRED so this list keeps meaning something." >&2
fi

if [ "$status" -eq 0 ]; then
  echo "OK: $(printf '%s\n' "$union" | wc -l | tr -d ' ') required variables across ${COMPOSE_FILES[*]}, all allowlisted and documented in $EXAMPLE_FILE."
fi

exit "$status"
