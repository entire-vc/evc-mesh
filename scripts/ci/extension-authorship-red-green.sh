#!/bin/sh
#
# RED/GREEN control for the extension-actor authorship fix.
#
# An extension token has neither user_id nor agent_id, so comment author_type and
# artifact uploaded_by_type used to reach Postgres empty (22P02 -> 400). The fix
# is commit $FIX (comment_handler.go and artifact_handler.go); the tests are
# internal/handler/extension_write_authorship_test.go.
#
#   RED    the reverse of $FIX's own diff for the two handler files, applied in an
#          isolated detached worktree of HEAD -> both tests must FAIL on a testify
#          assertion, not on a build or setup error
#   GREEN  the exact HEAD tree -> both tests must PASS
#
# RED is built from the fix commit, never from the MR diff: an MR that touches
# these files for an unrelated reason must not be checked against its own diff.
# Anything unexpected (fix commit missing, empty patch, patch does not apply)
# fails closed. The caller's tracked files and index are never modified; the only
# thing written outside the isolated worktree is $OUT (gitignored CI artifacts).
#
# Logs: $OUT/red.log, $OUT/green.log (CI artifacts).
set -eu

FIX=07a98431537cde29abf8cf0389076c75f93f50ad
OUT="${OUT:-extension-authorship-logs}"
FILES="internal/handler/comment_handler.go internal/handler/artifact_handler.go"
TESTS="TestCommentHandler_Create_ExtensionActorIsAuthor TestArtifactHandler_UploadJSON_ExtensionActorIsUploader"
RUN='ExtensionActorIs'
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

git cat-file -e "$FIX^{commit}" 2>/dev/null || { echo "REFUSING: fix commit $FIX not found (shallow clone?)"; exit 1; }
# shellcheck disable=SC2086
git diff "$FIX^" "$FIX" -- $FILES > "$OUT/fix.patch"
[ -s "$OUT/fix.patch" ] || { echo "REFUSING: empty fix diff for $FIX: $FILES"; exit 1; }

WT="$(mktemp -d)"
cleanup() { git worktree remove --force "$WT" >/dev/null 2>&1 || true; rm -rf "$WT"; }
trap cleanup EXIT

# RED: isolated tree, plain apply (no 3-way, so no conflict markers or index changes)
git worktree add -q --detach "$WT" HEAD
git -C "$WT" apply -R --check "$OUT/fix.patch" || { echo "REFUSING: reverse of $FIX does not apply to HEAD"; exit 1; }
git -C "$WT" apply -R "$OUT/fix.patch"
set +e
(cd "$WT" && go test ./internal/handler -run "$RUN" -count=1 -v) > "$OUT/red.log" 2>&1
red=$?
set -e
if [ "$red" -eq 0 ]; then echo "RED FAILED: tests passed without the fix"; tail -20 "$OUT/red.log"; exit 1; fi
if grep -Eq '\[build failed\]|\[setup failed\]|^panic:' "$OUT/red.log"; then
  echo "RED INVALID: build/setup error or panic, not an assertion failure"; tail -30 "$OUT/red.log"; exit 1
fi
for t in $TESTS; do
  grep -q -- "--- FAIL: $t " "$OUT/red.log" || { echo "RED INVALID: $t did not fail"; tail -30 "$OUT/red.log"; exit 1; }
done
grep -q 'Error Trace:' "$OUT/red.log" || { echo "RED INVALID: no testify assertion failure"; tail -30 "$OUT/red.log"; exit 1; }
echo "RED ok: both tests fail on assertions without the fix"

# GREEN: exact HEAD
go test ./internal/handler -run "$RUN" -count=1 -v > "$OUT/green.log" 2>&1 || { echo "GREEN FAILED"; tail -30 "$OUT/green.log"; exit 1; }
for t in $TESTS; do
  grep -q -- "--- PASS: $t " "$OUT/green.log" || { echo "GREEN INVALID: $t did not pass"; tail -30 "$OUT/green.log"; exit 1; }
done
echo "GREEN ok: both tests pass on HEAD $(git rev-parse --short HEAD)"
