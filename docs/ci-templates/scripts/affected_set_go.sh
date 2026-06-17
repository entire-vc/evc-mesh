#!/usr/bin/env bash
# Compute the Go affected-set for the coverage gate.
#
# Args:
#   $1  — base SHA to diff against (default: HEAD~1 for push, PR base for PRs)
#
# Outputs (stdout, one per line):
#   Go import paths of packages in
#   `changed_packages ∪ reverse_dep_closure(changed_packages)`
#   — every package that (transitively) imports a changed package.
#
# Usage in CI:
#   bash scripts/affected_set_go.sh "${{ github.event.pull_request.base.sha }}"
#
# Requires: go, git
set -euo pipefail

BASE_SHA="${1:-HEAD~1}"
MODULE_ROOT="${GO_MODULE_ROOT:-.}"

# Step 1: Collect Go files changed relative to base
mapfile -t CHANGED_FILES < <(git diff --name-only "$BASE_SHA" HEAD -- '*.go' 2>/dev/null || true)

if [ "${#CHANGED_FILES[@]}" -eq 0 ]; then
  exit 0  # nothing changed
fi

# Step 2: Unique package dirs containing changed files
declare -A CHANGED_DIRS
for f in "${CHANGED_FILES[@]}"; do
  dir="$(dirname "$f")"
  CHANGED_DIRS["$dir"]=1
done

# Step 3: Resolve import paths for changed packages
declare -A CHANGED_PKGS
for dir in "${!CHANGED_DIRS[@]}"; do
  if [ -d "$dir" ]; then
    import_path="$(cd "$MODULE_ROOT" && go list "./$dir" 2>/dev/null || true)"
    [ -n "$import_path" ] && CHANGED_PKGS["$import_path"]=1
  fi
done

if [ "${#CHANGED_PKGS[@]}" -eq 0 ]; then
  exit 0
fi

# Step 4: Build reverse-dep map — for each package in the module, what does it import?
# (Output: "importer\timport1 import2 ...")
declare -A REVERSE_DEPS  # import_path → "dependent1 dependent2 ..."
while IFS=$'\t' read -r pkg deps_str; do
  for dep in $deps_str; do
    REVERSE_DEPS["$dep"]+=" $pkg"
  done
done < <(cd "$MODULE_ROOT" && go list -f $'{{.ImportPath}}\t{{join .Imports " "}}' ./... 2>/dev/null)

# Step 5: BFS closure over reverse deps
declare -A AFFECTED
for pkg in "${!CHANGED_PKGS[@]}"; do
  AFFECTED["$pkg"]=1
done

QUEUE=("${!CHANGED_PKGS[@]}")
while [ "${#QUEUE[@]}" -gt 0 ]; do
  pkg="${QUEUE[0]}"
  QUEUE=("${QUEUE[@]:1}")  # dequeue
  for dependent in ${REVERSE_DEPS[$pkg]:-}; do
    dependent="${dependent# }"  # trim leading space
    [ -z "$dependent" ] && continue
    if [ -z "${AFFECTED[$dependent]+_}" ]; then
      AFFECTED["$dependent"]=1
      QUEUE+=("$dependent")
    fi
  done
done

# Step 6: Output affected import paths (sorted)
printf '%s\n' "${!AFFECTED[@]}" | sort
