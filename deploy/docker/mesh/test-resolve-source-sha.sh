#!/bin/sh
#
# Behavioural test for resolve-source-sha.sh: which commit a
# Docker build stamps into dist/.build-sha. Every case runs on a throwaway
# repository, so the answer is known exactly; the refusals (unknown, empty,
# malformed, contradicting, enclosing repository) are the point.
#
# Run:  sh deploy/docker/mesh/test-resolve-source-sha.sh [path/to/resolve-source-sha.sh]
# Red control: pass a resolver that always prints the build arg (or always
# HEAD of the enclosing repository); the test must fail.
set -eu

RESOLVE=${1:-$(dirname "$0")/resolve-source-sha.sh}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

commit_repo() {
  mkdir -p "$1"
  git -C "$1" init -q
  echo "$2" > "$1/f"
  git -C "$1" add f
  git -C "$1" -c user.name=t -c user.email=t@t commit -q -m "$2"
  git -C "$1" rev-parse HEAD
}

OTHER=$(printf 'b%.0s' $(seq 40))
SRC_SHA=$(commit_repo "$TMP/src" source)
mkdir -p "$TMP/tarball"
echo source > "$TMP/tarball/f"
# A deploy repository whose working tree holds the product checkout without
# its .git: git must not answer with the deploy repository's HEAD.
DEPLOY_SHA=$(commit_repo "$TMP/deploy" deploy)
mkdir -p "$TMP/deploy/evc-mesh"
echo source > "$TMP/deploy/evc-mesh/f"
# A worktree's .git is a file pointing at a gitdir on the host, absent inside the build.
mkdir -p "$TMP/worktree"
echo "gitdir: /nonexistent/.git/worktrees/x" > "$TMP/worktree/.git"

bad=0
# expect <name> <want-exit> <want-stdout> <args...>
expect() {
  name=$1 want_rc=$2 want_out=$3
  shift 3
  set +e
  out=$(sh "$RESOLVE" "$@" 2>"$TMP/err")
  rc=$?
  set -e
  if [ "$rc" = "$want_rc" ] && [ "$out" = "$want_out" ]; then
    echo "ok   $name: rc=$rc out='$out'"
  else
    echo "FAIL $name: rc=$rc out='$out' (want rc=$want_rc out='$want_out') $(cat "$TMP/err")"
    bad=$((bad + 1))
  fi
}

expect "git checkout -> its HEAD"                    0 "$SRC_SHA" "$TMP/src"
expect "git checkout + same build arg -> HEAD"       0 "$SRC_SHA" "$TMP/src" "$SRC_SHA"
expect "git checkout + other build arg -> refused"   1 ""         "$TMP/src" "$OTHER"
expect "no .git + build arg -> build arg"            0 "$SRC_SHA" "$TMP/tarball" "$SRC_SHA"
expect "no .git + build arg with newline -> trimmed" 0 "$SRC_SHA" "$TMP/tarball" "$SRC_SHA
"
expect "no .git, no build arg -> unknown, no stamp"  0 ""         "$TMP/tarball"
expect "no .git + empty build arg -> no stamp"       0 ""         "$TMP/tarball" ""
expect "no .git + short sha -> refused"              1 ""         "$TMP/tarball" "${SRC_SHA%????????}"
expect "no .git + 'dev' -> refused"                  1 ""         "$TMP/tarball" "dev"
expect "no .git + uppercase sha -> refused"          1 ""         "$TMP/tarball" "$(printf '%s' "$SRC_SHA" | tr a-f A-F)"
expect "inside a deploy repo -> not the deploy HEAD" 0 ""         "$TMP/deploy/evc-mesh"
expect "worktree .git file, gitdir absent -> no stamp" 0 ""       "$TMP/worktree"
expect "worktree .git file + build arg -> build arg" 0 "$SRC_SHA" "$TMP/worktree" "$SRC_SHA"

# Positive control for the deploy case: the deploy repo itself does resolve,
# so the empty answer above is the guard working, not git being absent.
expect "deploy repo root -> its own HEAD"            0 "$DEPLOY_SHA" "$TMP/deploy"

[ "$bad" = 0 ] || { echo "$bad case(s) failed" >&2; exit 1; }
echo "all cases passed"
