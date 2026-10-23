#!/usr/bin/env bash
# Red/green control for perf-web-unchanged.sh on a throwaway repo.
set -eu
script="$(cd "$(dirname "$0")" && pwd)/perf-web-unchanged.sh"
d="$(mktemp -d)"; trap 'find "$d" -delete' EXIT
cd "$d"
git init -q -b main . && git config user.email t@t && git config user.name t
mkdir -p web docs/perf internal
echo a > web/a.ts; echo h > docs/perf/h.mjs; echo g > internal/x.go
git add -A && git commit -qm base
git checkout -qb branch
echo b >> internal/x.go && git commit -qam backend
bash "$script" main || { echo "FAIL: backend-only diff must be unchanged (0)"; exit 1; }
# main moves web/ while the branch is backend-only: still nothing to measure
git checkout -q main && echo m >> web/a.ts && git commit -qam main-web && git checkout -q branch
bash "$script" main || { echo "FAIL: branch behind a web-moving main must be unchanged (0)"; exit 1; }
echo b >> web/b.ts && git add -A && git commit -qm web
rc=0; bash "$script" main || rc=$?
[ "$rc" -eq 1 ] || { echo "FAIL: web diff must exit 1, got $rc"; exit 1; }
rc=0; bash "$script" nosuchref 2>/dev/null || rc=$?
[ "$rc" -eq 2 ] || { echo "FAIL: bad ref must exit 2, got $rc"; exit 1; }
echo "ok"
