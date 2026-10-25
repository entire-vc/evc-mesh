#!/usr/bin/env bash
# Red/green controls for scripts/check-migration-version.sh in a throwaway repo:
# deleting or renaming a migration that already exists on base must fail,
# and the explicit exception file must be the only way past it.
set -euo pipefail

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/check-migration-version.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q -b main origin && cd origin
git config user.email t@example.com && git config user.name t
mkdir migrations
for v in 20260101001_a 20260101002_b 20260101003_c; do echo "-- $v" > "migrations/$v.sql"; done
git add . && git commit -q -m base
git clone -q "$tmp/origin" "$tmp/work" && cd "$tmp/work"
git config user.email t@example.com && git config user.name t
git switch -q -c topic

run() { MIGRATION_BASE_REF=main MIGRATION_GATE_EXCEPTIONS="${1:-/nonexistent}" bash "$SCRIPT" migrations >"$tmp/out" 2>&1; }
expect_fail() { if run "${2:-}"; then echo "FAIL (expected red): $1"; cat "$tmp/out"; exit 1; fi; echo "ok red: $1"; }
expect_pass() { run "${2:-}" || { echo "FAIL (expected green): $1"; cat "$tmp/out"; exit 1; }; echo "ok green: $1"; }

expect_pass "untouched branch"

git mv migrations/20260101002_b.sql migrations/20260101009_b.sql && git commit -qam rename
expect_fail "renaming an existing migration"
printf 'allow-remove 20260101002_b.sql\nallow-add 20260101009_b.sql\n' > "$tmp/ex"
expect_pass "rename with explicit exception" "$tmp/ex"

git reset -q --hard origin/main && git rm -q migrations/20260101003_c.sql && git commit -qm drop
expect_fail "deleting an existing migration"

git reset -q --hard origin/main && echo x > migrations/20260102001_new.sql && git add . && git commit -qm add
expect_pass "adding a newer migration"
# Two branches that each took the next free version and were merged one after the
# other leave a duplicate on main itself (an internal ticket, 20261025001): the directory check
# must go red on that tree even where no base branch is available to compare with.
git reset -q --hard origin/main && echo x > migrations/20260101003_other.sql && git add . && git commit -qm dup
expect_fail "duplicate version in the tree"
if MIGRATION_BASE_REF=no-such-branch bash "$SCRIPT" migrations >"$tmp/out" 2>&1; then
    echo "FAIL (expected red): duplicate version without a base branch"; cat "$tmp/out"; exit 1
fi
echo "ok red: duplicate version without a base branch"
echo "all migration-gate controls passed"
