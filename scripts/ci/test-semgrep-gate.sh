#!/bin/sh
#
# Red/green control for semgrep-gate.sh. Runs the REAL semgrep
# (the job uses the semgrep/semgrep image) on throwaway git repos with one local
# rule, so it needs no registry and no network. A file:// remote stands in for
# GitLab; the job-token URL `semgrep ci` builds for its own target fetch is
# rewritten to it with url.<path>.insteadOf.
#
#   BLIND  stale target-side .git/shallow (the job 867844 condition):
#          bare `semgrep ci` exits 0 without scanning — asserted, so the
#          reproduction itself is checked — and the gate scans the diff and
#          catches the planted finding (exit 1).
#   RED    baseline not in the clone (HEAD cut by depth 1): bare `semgrep ci`
#          exits 0 blind; the gate refuses (exit 1).
#   RED    CI_MERGE_REQUEST_DIFF_BASE_SHA empty → refused.
#   RED    semgrep exits 0 and writes no result (stub) → refused.
#   RED    the diff changes a text file and semgrep scans 0 files → refused.
#   RED    main pipeline, semgrep scans 0 files (stub) → refused.
#   RED    planted finding on an ordinary MR → semgrep's exit 1 passes through.
#   GREEN  ordinary clean MR → exit 0, "Scan was limited to files changed since
#          baseline commit", 1 file scanned.
#   GREEN  deletions-only and binary-only MRs → exit 0, nothing to scan.
#   GREEN  main pipeline, full scan → exit 0.
#
# `--prove-controls`: runs the same cases against a bare `semgrep ci` in place of
# the gate and requires the suite to FAIL; a suite that a bare `semgrep ci`
# passes would not be guarding anything.
#
# Exit 0 = the gate discriminates. Non-zero = do not trust it.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
GATE=${SEMGREP_GATE_UNDER_TEST:-$HERE/semgrep-gate.sh}

if [ "${1:-}" = "--prove-controls" ]; then
  BARE=$(mktemp -d)/bare-gate.sh
  printf '#!/bin/sh\nexec semgrep ci\n' > "$BARE"
  if SEMGREP_GATE_UNDER_TEST=$BARE sh "$0" >/dev/null 2>&1; then
    echo "control bare-semgrep-ci: PASSED the suite — the suite does not discriminate" >&2
    exit 1
  fi
  echo "control bare-semgrep-ci: failed as expected"
  exec sh "$0"
fi

command -v semgrep >/dev/null || { echo "semgrep not on PATH" >&2; exit 2; }

T=$(mktemp -d)
FAILS=0
export SEMGREP_SEND_METRICS=off SEMGREP_ENABLE_VERSION_CHECK=0 SEMGREP_RULES="$T/rules.yml"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
# A private HOME: semgrep's cache and any git config stay inside the throwaway dir.
export HOME="$T/home"; mkdir -p "$HOME"

cat > "$T/rules.yml" <<'EOF'
rules:
  - id: planted-shell-exec
    languages: [go]
    severity: ERROR
    message: planted finding for the semgrep gate test
    pattern: exec.Command("sh", "-c", $X)
EOF

# ── fixture: origin with main and one branch per case ──────────────────────
git init -q --bare "$T/origin.git"
git init -q "$T/seed"
(
  cd "$T/seed"
  git checkout -q -b main
  mkdir -p pkg vendor
  printf 'package pkg\n\nfunc A() int { return 1 }\n' > pkg/a.go
  printf 'package pkg\n\nfunc Old() {}\n' > pkg/old.go
  printf 'vendor/\n' > .semgrepignore
  git add -A && git commit -qm base
  for i in 1 2 3; do echo "line $i" > "main$i.txt"; git add -A; git commit -qm "main $i"; done
  git remote add origin "file://$T/origin.git"
  git push -q origin main
  # Every MR forks from main~1, so main has moved on since (as on a live target).
  git checkout -q -b mr-find main~1
  printf 'package pkg\n\nimport "os/exec"\n\nfunc B(x string) { _ = exec.Command("sh", "-c", x).Run() }\n' > pkg/b.go
  git add -A && git commit -qm find
  git checkout -q -b mr-clean main~1
  printf 'package pkg\n\nfunc A() int { return 2 }\n' > pkg/a.go
  git add -A && git commit -qm clean
  git checkout -q -b mr-del main~1
  git rm -q pkg/old.go && git commit -qm del
  git checkout -q -b mr-bin main~1
  printf '\211PNG\r\n\032\n\000\000\000\rIHDR' > img.png
  git add -A && git commit -qm bin
  git checkout -q -b mr-ign main~1
  printf 'package v\n\nfunc V() {}\n' > vendor/v.go
  git add -A && git commit -qm ign
  git push -q origin mr-find mr-clean mr-del mr-bin mr-ign
)

# clone <dir> <branch> [deep|stale-target|depth1]
clone() {
  git init -q "$1"
  git -C "$1" remote add origin "file://$T/origin.git"
  case "${3:-deep}" in
    deep) git -C "$1" fetch -q origin "+refs/heads/$2:refs/remotes/origin/$2" ;;
    # An earlier job in the same build dir left the target tip shallow-cut.
    stale-target)
      git -C "$1" fetch -q --depth 1 origin main
      git -C "$1" fetch -q --depth 20 origin "+refs/heads/$2:refs/remotes/origin/$2" ;;
    depth1) git -C "$1" fetch -q --depth 1 origin "+refs/heads/$2:refs/remotes/origin/$2" ;;
  esac
  git -C "$1" checkout -q --detach "origin/$2"
}

# run <dir> <mr|main> <diff-base> <cmd...>: emulated GitLab job environment.
run() {
  dir=$1 src=$2 base=$3; shift 3
  tokurl="https://gitlab-ci-token:"
  tokurl="${tokurl}fake@git.example.invalid/g/p"
  (
    cd "$dir"
    if [ "$src" = mr ]; then
      export CI_PIPELINE_SOURCE=merge_request_event CI_MERGE_REQUEST_TARGET_BRANCH_NAME=main
      export CI_MERGE_REQUEST_DIFF_BASE_SHA="$base"
    else
      export CI_PIPELINE_SOURCE=push CI_COMMIT_BRANCH=main
      unset CI_MERGE_REQUEST_DIFF_BASE_SHA CI_MERGE_REQUEST_TARGET_BRANCH_NAME 2>/dev/null || true
    fi
    export CI=true GITLAB_CI=true CI_JOB_TOKEN=fake CI_PROJECT_PATH=g/p
    export CI_PROJECT_URL=https://git.example.invalid/g/p CI_MERGE_REQUEST_PROJECT_URL=https://git.example.invalid/g/p
    CI_COMMIT_SHA=$(git rev-parse HEAD); export CI_COMMIT_SHA
    export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="url.file://$T/origin.git.insteadOf" GIT_CONFIG_VALUE_0="$tokurl"
    "$@"
  )
}

# expect <name> <want-rc> <log> <grep-pattern> <cmd...>
expect() {
  name=$1 want=$2 log=$3 pat=$4; shift 4
  rc=0
  "$@" > "$log" 2>&1 || rc=$?
  if [ "$rc" != "$want" ]; then
    echo "FAIL $name: exit $rc, want $want"; sed 's/^/    | /' "$log" | tail -n 15; FAILS=$((FAILS + 1)); return 0
  fi
  if [ -n "$pat" ] && ! grep -qE "$pat" "$log"; then
    echo "FAIL $name: exit $rc ok, but no /$pat/ in the output"; sed 's/^/    | /' "$log" | tail -n 15; FAILS=$((FAILS + 1)); return 0
  fi
  echo "ok   $name (exit $rc)"
}

# Every MR forks from main~1: that is the MR diff base GitLab would report.
DIFF_BASE=$(git -C "$T/seed" rev-parse main~1)

# BLIND: the job 867844 condition.
clone "$T/c-stale" mr-find stale-target
expect "blind: bare semgrep ci on a stale target cut exits 0 without scanning (reproduction)" 0 "$T/l1" \
  "Failed to run 'git merge-base --all" run "$T/c-stale" mr "$DIFF_BASE" semgrep ci
if grep -q "Scanning" "$T/l1"; then echo "FAIL blind reproduction: bare semgrep ci did scan"; FAILS=$((FAILS + 1)); fi
expect "blind condition: the gate scans the diff and catches the finding" 1 "$T/l2" \
  "planted-shell-exec|1 blocking" run "$T/c-stale" mr "$DIFF_BASE" sh "$GATE"

# RED: baseline not in the clone.
clone "$T/c-d1" mr-find depth1
expect "baseline missing: bare semgrep ci exits 0 without scanning (reproduction)" 0 "$T/l3" \
  "Failed to run 'git merge-base --all" run "$T/c-d1" mr "$DIFF_BASE" semgrep ci
expect "baseline missing: the gate refuses" 1 "$T/l4" \
  "REFUSING .*baseline" run "$T/c-d1" mr "$DIFF_BASE" sh "$GATE"

clone "$T/c-clean" mr-clean
expect "empty CI_MERGE_REQUEST_DIFF_BASE_SHA: refused" 1 "$T/l5" \
  "REFUSING .*DIFF_BASE_SHA is empty" run "$T/c-clean" mr "" sh "$GATE"

STUB=$T/stub; mkdir -p "$STUB"
printf '#!/bin/sh\necho "[ERROR] stub: scan aborted" >&2\nexit 0\n' > "$STUB/semgrep"
chmod +x "$STUB/semgrep"
expect "semgrep exits 0 with no result: refused" 1 "$T/l6" \
  "REFUSING .*wrote no result" run "$T/c-clean" mr "$DIFF_BASE" env PATH="$STUB:$PATH" sh "$GATE"

clone "$T/c-ign" mr-ign
expect "text file changed, 0 files scanned: refused" 1 "$T/l7" \
  "REFUSING .*0 files scanned" run "$T/c-ign" mr "$DIFF_BASE" sh "$GATE"

clone "$T/c-find" mr-find
expect "planted finding: exit 1 passes through" 1 "$T/l8" \
  "planted-shell-exec|1 blocking" run "$T/c-find" mr "$DIFF_BASE" sh "$GATE"

# GREEN
expect "ordinary clean MR: diff scanned, green" 0 "$T/l9" \
  "scanned 1 file" run "$T/c-clean" mr "$DIFF_BASE" sh "$GATE"
grep -q "Scan was limited to files changed since baseline commit" "$T/l9" ||
  { echo "FAIL ordinary clean MR: not a diff scan"; FAILS=$((FAILS + 1)); }

clone "$T/c-del" mr-del
expect "deletions-only MR: nothing to scan, green" 0 "$T/l10" \
  "nothing to scan" run "$T/c-del" mr "$DIFF_BASE" sh "$GATE"
clone "$T/c-bin" mr-bin
expect "binary-only MR: nothing to scan, green" 0 "$T/l11" \
  "nothing to scan" run "$T/c-bin" mr "$DIFF_BASE" sh "$GATE"

clone "$T/c-main" main
expect "main pipeline: full scan, green" 0 "$T/l12" \
  "scanned [1-9][0-9]* file" run "$T/c-main" main "" sh "$GATE"
# shellcheck disable=SC2016  # the stub's own $a, expanded when the stub runs
printf '#!/bin/sh\nfor a; do case $a in --json-output=*) echo "{\\"results\\":[],\\"errors\\":[],\\"paths\\":{\\"scanned\\":[]}}" > "${a#--json-output=}";; esac; done\nexit 0\n' > "$STUB/semgrep"
expect "main pipeline, 0 files scanned: refused" 1 "$T/l13" \
  "REFUSING .*0 files scanned" run "$T/c-main" main "" env PATH="$STUB:$PATH" sh "$GATE"

if [ "$FAILS" -ne 0 ]; then
  echo "$FAILS case(s) failed"
  exit 1
fi
echo "all cases passed"
