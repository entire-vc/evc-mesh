#!/usr/bin/env bash
# Run the affected-set diff gate. Test/profile/measurement errors are fatal;
# only a successfully resolved empty set or exclusively main packages skip it.
set -euo pipefail

BASE="${1:?coverage_gate.sh requires a non-empty diff base}"
echo "Diff base: $BASE"
AFFECTED=$(bash docs/ci-templates/scripts/affected_set_go.sh "$BASE")
echo "Affected packages: $AFFECTED"
if [ -z "$AFFECTED" ]; then
  echo "No surviving Go packages in this diff — nothing to gate."
  exit 0
fi

coverage_tmp=$(mktemp -d "${TMPDIR:-/tmp}/coverage-gate.XXXXXXXX")
trap 'python3 -c "import shutil,sys; shutil.rmtree(sys.argv[1])" "$coverage_tmp"' EXIT
profiles=()
exclusions=()
FAILED=0
while IFS= read -r pkg; do
  [ -z "$pkg" ] && continue
  metadata=$(go list -race -f '{{.Name}} {{.Dir}} {{join .GoFiles " "}} {{join .CgoFiles " "}}' "$pkg")
  read -r name dir files <<< "$metadata"
  if [ -z "$name" ] || [ -z "$dir" ]; then
    echo "ERROR: missing package metadata for $pkg" >&2
    exit 1
  fi
  relative_dir="${dir#"$PWD"}"
  relative_dir="${relative_dir#/}"
  # Use the same toolchain/build flags as go test to identify inactive sources.
  # They have no profile under this configuration, even in library packages.
  ignored=$(go list -race -f '{{join .IgnoredGoFiles " "}}' "$pkg")
  for file in $ignored; do
    exclusions+=(--exclude-file "${relative_dir:+$relative_dir/}$file")
  done
  if [ "$name" = main ]; then
    echo "skip (main package, not unit-testable): $pkg"
    for file in $files; do
      exclusions+=(--exclude-file "${relative_dir:+$relative_dir/}$file")
    done
    continue
  fi
  profile="$coverage_tmp/cover_${#profiles[@]}.out"
  profiles+=("$profile")
  go test -race -covermode=atomic -coverprofile="$profile" "$pkg" 2>&1 || {
    FAILED=1
    echo "FAIL: $pkg"
  }
done <<< "$AFFECTED"

# Never let a missing profile hide a failed test. A successful test without its
# requested profile is an error too; the merger checks every expected path.
if [ "$FAILED" -ne 0 ]; then
  echo "ERROR: one or more affected packages failed to test" >&2
  exit 1
fi
if [ "${#profiles[@]}" -eq 0 ]; then
  echo "Affected set is confirmed main-only — nothing to gate."
  exit 0
fi

python3 scripts/ci/merge_coverage_profiles.py "$coverage_tmp/merged.out" "${profiles[@]}"
go tool cover -func="$coverage_tmp/merged.out" > "$coverage_tmp/summary.txt"
cat "$coverage_tmp/summary.txt"
cp "$coverage_tmp/summary.txt" "${CI_PROJECT_DIR:-$PWD}/coverage-affected-summary.txt"
if ! grep -Eq '^total:.*[0-9]+\.[0-9]+%$' "$coverage_tmp/summary.txt"; then
  echo "ERROR: coverage summary has no total" >&2
  exit 1
fi

# Exit 2 identifies an infrastructure/measurement error, distinct from the
# below-threshold verdict (1), but both must fail the required job.
python3 scripts/ci/diff_coverage.py --profile "$coverage_tmp/merged.out" \
  --base "$BASE" --threshold 80 "${exclusions[@]}"
