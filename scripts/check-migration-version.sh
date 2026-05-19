#!/usr/bin/env bash
# Enforces two migration version invariants:
#   1. No duplicate version numbers across all migrations in MIGRATIONS_DIR.
#   2. Any migration added in this branch has version > max(base branch).
#
# Run in CI on PRs that touch migrations/. Also safe to run locally.
#
# Usage:
#   ./scripts/check-migration-version.sh [migrations-dir]
#
# Environment (set automatically in GitHub Actions pull_request events):
#   GITHUB_BASE_REF — base branch name (default: main)
set -euo pipefail

MIGRATIONS_DIR="${1:-migrations}"
BASE_REF="${GITHUB_BASE_REF:-main}"

if [[ ! -d "$MIGRATIONS_DIR" ]]; then
    echo "ERROR: migrations dir '$MIGRATIONS_DIR' not found." >&2
    exit 1
fi

# ── 1. Collect all version numbers ───────────────────────────────────────────
all_vers=()
while IFS= read -r f; do
    base=$(basename "$f")
    ver="${base%%_*}"
    if [[ "$ver" =~ ^[0-9]+$ ]]; then
        all_vers+=("$ver")
    else
        echo "WARNING: skipping non-standard filename: $base" >&2
    fi
done < <(find "$MIGRATIONS_DIR" -maxdepth 1 -name '*.sql' | sort)

echo "Found ${#all_vers[@]} migration(s) in '${MIGRATIONS_DIR}'."

# ── 2. Duplicate version check ───────────────────────────────────────────────
if (( ${#all_vers[@]} > 0 )); then
    dups=$(printf '%s\n' "${all_vers[@]}" | sort -n | uniq -d)
    if [[ -n "$dups" ]]; then
        echo "ERROR: Duplicate migration version numbers:" >&2
        echo "$dups" >&2
        exit 1
    fi
fi

# ── 3. New-migration version > max(base) check ───────────────────────────────
# Requires that origin/<BASE_REF> is available (git fetch must have run first).
if ! git rev-parse "origin/${BASE_REF}" &>/dev/null 2>&1; then
    echo "SKIP: origin/${BASE_REF} not found — skipping base-branch comparison."
    echo "OK: ${#all_vers[@]} migration(s), no duplicates."
    exit 0
fi

max_base=0
while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    ver="${name%%_*}"
    if [[ "$ver" =~ ^[0-9]+$ ]] && (( ver > max_base )); then
        max_base=$ver
    fi
done < <(git ls-tree --name-only "origin/${BASE_REF}" "${MIGRATIONS_DIR}/" 2>/dev/null \
         | xargs -I{} basename {} 2>/dev/null \
         || true)

echo "Max version on origin/${BASE_REF}: ${max_base}"

# Files added in this branch compared to base
new_files=$(git diff --name-only --diff-filter=A "origin/${BASE_REF}" -- "${MIGRATIONS_DIR}/*.sql" 2>/dev/null || true)

violations=()
for f in $new_files; do
    base=$(basename "$f")
    ver="${base%%_*}"
    if [[ "$ver" =~ ^[0-9]+$ ]] && (( ver <= max_base )); then
        violations+=("  $base  →  version $ver ≤ max $max_base on origin/${BASE_REF}")
    fi
done

if (( ${#violations[@]} > 0 )); then
    echo "" >&2
    echo "ERROR: New migration(s) have out-of-order version number(s):" >&2
    printf '%s\n' "${violations[@]}" >&2
    echo "" >&2
    echo "Policy: every new migration must have version > ${max_base}." >&2
    echo "Fix: rename the file to use today's timestamp as prefix." >&2
    today=$(date -u +%Y%m%d)
    next=$(( ${#all_vers[@]} + 1 ))
    echo "Example prefix: ${today}$(printf '%03d' ${next})" >&2
    exit 1
fi

echo "OK: ${#all_vers[@]} migration(s), no duplicates, all new versions > ${max_base}."
