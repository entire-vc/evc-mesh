#!/usr/bin/env bash
# Enforces two migration version invariants:
#   1. No duplicate version numbers across all migrations in MIGRATIONS_DIR.
#   2. Any migration added in this branch has version > max(base branch).
#   3. No migration file that already exists on the base branch is deleted or renamed
#      (a rename is a delete of the old name). Production applies migrations under the
#      version in the filename, so removing or renumbering one that has shipped leaves
#      goose with an applied version it can no longer find and an unapplied one below it
#      (#3cb090a0: i18n_locale 001 -> 002 after prod had applied 001).
#
# Deliberate exceptions live in scripts/migration-gate-exceptions.txt, one per line:
#   allow-add <file>      may be added with a version <= max(base), e.g. restoring a number
#   allow-remove <file>   may be deleted/renamed away, e.g. never applied on production
#
# Run in CI on PRs that touch migrations/. Also safe to run locally.
#
# Usage:
#   ./scripts/check-migration-version.sh [migrations-dir]
#
# Environment — the base branch to compare against, first one set wins:
#   MIGRATION_BASE_REF                    — explicit override, for local runs
#   CI_MERGE_REQUEST_TARGET_BRANCH_NAME   — GitLab, set on merge_request_event
#   GITHUB_BASE_REF                       — GitHub Actions, set on pull_request
#   (default: main)
#
# Both CI variables are read because the canon repository moved to GitLab
# (2026-08-23) while the GitHub workflow still exists on the showcase mirror.
# Reading only one of them is how this check came to be inert on the canon for
# two weeks: the script was fine, nothing called it (#3d18da50).
set -euo pipefail

MIGRATIONS_DIR="${1:-migrations}"
BASE_REF="${MIGRATION_BASE_REF:-${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-${GITHUB_BASE_REF:-main}}}"

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
new_files=$(git diff --name-only --diff-filter=A --no-renames "origin/${BASE_REF}" -- "${MIGRATIONS_DIR}/*.sql" 2>/dev/null || true)

EXCEPTIONS_FILE="${MIGRATION_GATE_EXCEPTIONS:-scripts/migration-gate-exceptions.txt}"
exception_listed() {  # kind file
    # Field compare, not a regex: file names carry dots, and a bare entry (no trailing
    # comment) has no character after the name for a pattern to anchor on.
    [[ -f "$EXCEPTIONS_FILE" ]] && awk -v k="$1" -v f="$2" '$1 == k && $2 == f { found = 1 } END { exit !found }' "$EXCEPTIONS_FILE"
}

violations=()
for f in $new_files; do
    base=$(basename "$f")
    if exception_listed allow-add "$base"; then
        echo "NOTE: $base version check skipped (allow-add in $EXCEPTIONS_FILE)."
        continue
    fi
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

# ── 4. Ratchet: a migration already on base must not be deleted or renamed ──
removed=$(git diff --name-only --diff-filter=D --no-renames "origin/${BASE_REF}...HEAD" -- "${MIGRATIONS_DIR}/*.sql" 2>/dev/null || true)
removal_violations=()
for f in $removed; do
    base=$(basename "$f")
    exception_listed allow-remove "$base" && continue
    removal_violations+=("  $base")
done
if (( ${#removal_violations[@]} > 0 )); then
    echo "" >&2
    echo "ERROR: migration(s) that exist on origin/${BASE_REF} are deleted or renamed:" >&2
    printf '%s\n' "${removal_violations[@]}" >&2
    echo "" >&2
    echo "Production may already have applied them under this version; goose up then fails." >&2
    echo "Fix: restore the file. If it was provably never applied, add 'allow-remove <file>' to $EXCEPTIONS_FILE with the evidence in the MR." >&2
    exit 1
fi

echo "OK: ${#all_vers[@]} migration(s), no duplicates, all new versions > ${max_base}, none removed."
