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
# Check the producer before reading its output: process substitutions do not
# propagate their exit status to mapfile (or to a while loop).
CHANGED_OUTPUT=$(git diff --name-only "$BASE_SHA" HEAD -- '*.go')
CHANGED_FILES=()
if [ -n "$CHANGED_OUTPUT" ]; then
  mapfile -t CHANGED_FILES <<< "$CHANGED_OUTPUT"
fi

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
declare -A CHANGED_PKGS=()
declare -A DELETED_DIRS=()
declare -A DELETED_PKGS=()
for dir in "${!CHANGED_DIRS[@]}"; do
  if [ -d "$dir" ]; then
    sources=("$dir"/*.go)
    if [ ! -e "${sources[0]}" ]; then
      DELETED_DIRS["$dir"]=1
      continue
    fi
    import_path="$(cd "$MODULE_ROOT" && go list -race "./$dir")"
    if [ -z "$import_path" ]; then
      echo "ERROR: go list returned no import path for $dir" >&2
      exit 1
    fi
    [ -n "$import_path" ] && CHANGED_PKGS["$import_path"]=1
  else
    DELETED_DIRS["$dir"]=1
  fi
done

if [ "${#DELETED_DIRS[@]}" -gt 0 ]; then
  module_path="$(cd "$MODULE_ROOT" && go list -m -f '{{.Path}}')"
  if [ -z "$module_path" ]; then
    echo "ERROR: go list returned no module path for deleted packages" >&2
    exit 1
  fi
  for dir in "${!DELETED_DIRS[@]}"; do
    import_path="$module_path"
    if [ "$dir" != . ]; then
      import_path="${module_path}/${dir#./}"
    fi
    CHANGED_PKGS["$import_path"]=1
    DELETED_PKGS["$import_path"]=1
  done
fi

if [ "${#CHANGED_PKGS[@]}" -eq 0 ]; then
  exit 0
fi

# Step 4: Build reverse-dep map — for each package in the module, what does it import?
# (Output: "importer\timport1 import2 ...")
declare -A REVERSE_DEPS  # import_path → "dependent1 dependent2 ..."
PACKAGE_GRAPH=$(cd "$MODULE_ROOT" && go list -race -f $'{{.ImportPath}}\t{{join .Imports " "}}' ./...)
if [ -z "$PACKAGE_GRAPH" ]; then
  echo "ERROR: go list returned an empty dependency graph" >&2
  exit 1
fi
while IFS=$'\t' read -r pkg deps_str; do
  for dep in $deps_str; do
    REVERSE_DEPS["$dep"]+=" $pkg"
  done
done <<< "$PACKAGE_GRAPH"

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
# A deleted package is a seed for reverse-dependency checks, but cannot itself
# be tested or measured in the post-image. An unreferenced deletion is empty.
for pkg in "${!DELETED_PKGS[@]}"; do
  unset 'AFFECTED[$pkg]'
done
if [ "${#AFFECTED[@]}" -gt 0 ]; then
  printf '%s\n' "${!AFFECTED[@]}" | sort
fi
