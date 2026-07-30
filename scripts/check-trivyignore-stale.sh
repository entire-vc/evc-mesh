#!/usr/bin/env bash
# Fail when .trivyignore suppresses a finding ID that trivy cannot see at all.
#
# Why this exists: a suppression whose finding has gone away does not read as
# stale — it reads as clean. CVE-2026-32285 was accepted 2026-05-23 as a
# TEMPORARY ignore, fixed by Dependabot 15 minutes later (#123), and dropped
# from the dependency tree entirely on 2026-07-08 (#257). The line sat in
# .trivyignore for 68 days. Nothing was exposed only because the dependency
# never came back — indirect dependencies return without anyone deciding to
# bring them back, and trivy would have said nothing.
#
# Usage: check-trivyignore-stale.sh [ignore-file] [trivy-json-report]
#
# The report MUST come from a trivy run with NO severity filter, NO
# ignore-unfixed and NO --ignorefile, so that "absent from the report" really
# means "not in the tree" rather than "filtered out". A narrower report would
# make live suppressions look stale and get them deleted.

set -euo pipefail

ignore_file=${1:-.trivyignore}
report=${2:-trivy-all.json}

if [[ ! -f $ignore_file ]]; then
  echo "no $ignore_file — nothing to check"
  exit 0
fi

# Suppression IDs: first field of every non-comment, non-blank line. The plain
# .trivyignore format allows trailing fields (e.g. `CVE-x exp:2026-01-01`), so
# take field 1 rather than the whole line.
# (No `mapfile` — this script is also run by hand on macOS, which ships bash 3.2.)
listed=()
while IFS= read -r id; do
  [ -n "$id" ] && listed+=("$id")
done < <(awk '{ sub(/#.*/, "") } NF { print $1 }' "$ignore_file")

if [ ${#listed[@]} -eq 0 ]; then
  echo "$ignore_file has no suppression entries — nothing to check"
  exit 0
fi

# Fail closed: an unreadable report cannot distinguish a stale suppression from
# a clean tree, and "cannot tell" must not pass as "fine".
if [[ ! -s $report ]]; then
  echo "::error::$report is missing or empty — cannot verify suppressions are live. Refusing to pass."
  exit 1
fi

if ! found=$(jq -er '[.Results[]?.Vulnerabilities[]?.VulnerabilityID, .Results[]?.Misconfigurations[]?.ID, .Results[]?.Secrets[]?.RuleID] | unique | .[]' "$report"); then
  echo "::error::could not extract finding IDs from $report (malformed report, or trivy found nothing at all)."
  echo "::error::A report with zero findings makes every suppression look stale, so this is treated as a failure, not a pass."
  jq -r '{SchemaVersion, results: (.Results | length)}' "$report" 2>&1 | head -5 || head -c 400 "$report"
  exit 1
fi

stale=()
for id in "${listed[@]}"; do
  grep -qxF "$id" <<<"$found" || stale+=("$id")
done

if [ ${#stale[@]} -gt 0 ]; then
  echo "::error::${#stale[@]} suppression(s) in $ignore_file match nothing trivy can see."
  echo
  echo "A suppression that matches nothing is not clean — it is a muzzle waiting for"
  echo "the finding to come back, and it will come back silently. Remove these lines:"
  echo
  for id in "${stale[@]}"; do
    echo "  $id"
    grep -n -B6 -m1 -E "^[[:space:]]*${id}([[:space:]]|$)" "$ignore_file" | sed 's/^/      /' || true
  done
  echo
  echo "If a listed finding IS still real, the report was built too narrowly — it must"
  echo "come from a trivy run without severity/ignore-unfixed/ignorefile filtering."
  exit 1
fi

echo "all ${#listed[@]} suppression(s) in $ignore_file still match a live trivy finding"
