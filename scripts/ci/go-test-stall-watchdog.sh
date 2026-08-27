#!/bin/sh
#
# Run `go test` so that a hang produces a stack dump instead of silence.
#
# ## The failure this exists for
#
# 2026-08-26, jobs 3242 and 3266 (Mesh #405872aa). `go test ./... -race` printed
# four package results, then emitted nothing for 28 minutes and was killed by the
# job timeout, holding the fleet's only runner the whole time. The trace was
# byte-identical when read twice 45s apart: 32009 bytes, not growing.
#
# What made it undiagnosable was not the hang, it was the silence. Two facts from
# that trace:
#
#   * `go test`'s own `-timeout` (10m by default) NEVER FIRED. A test binary that
#     blows its timeout panics and prints every goroutine's stack; no such dump
#     appeared in 28 minutes. So whatever was wedged was not a running test with a
#     live watchdog goroutine — it was the go tool, a compile/link child, or a
#     process stalled hard enough that the in-binary timer could not run.
#   * `internal/auth`, the package it stopped before, is byte-identical between
#     that commit and the one that passed it in 20.5s on the same host at the same
#     time. So the wedge is not a defect in the code being tested.
#
# `-timeout` alone therefore cannot close this. It only covers the one case that
# was already excluded. This wrapper covers the rest.
#
# ## What it does
#
# Runs `go test` in its own process group and watches its output. If nothing new
# is written for $STALL_SECONDS, it sends SIGQUIT to the WHOLE PROCESS GROUP.
# Every process involved is a Go program — the `go` tool, `compile`, `link`, the
# test binary — and the Go runtime answers SIGQUIT by dumping all goroutine stacks
# and dying. That is what turns "28 minutes of nothing" into "here is exactly
# which process was blocked, and on what".
#
# Then it SIGKILLs and exits non-zero. A wedged run must fail the job, not be
# retried into another half hour of a shared runner.
#
# ## Usage
#
#   scripts/ci/go-test-stall-watchdog.sh ./... -race -coverprofile=coverage.out
#
# Every argument is passed to `go test` untouched. Env:
#   STALL_SECONDS   quiet period before the dump (default 180)
#   POLL_SECONDS    how often to check for new output (default 10)
#
# ## Proving it works
#
# `scripts/ci/test-go-test-stall-watchdog.sh` is the red/green control: it runs
# this wrapper against a deliberately deadlocked test and requires a stack dump
# plus a non-zero exit, and against a passing and a failing package to prove it
# does not change ordinary outcomes. A watchdog nobody has seen fire is
# indistinguishable from one that is broken.

set -eu

STALL_SECONDS=${STALL_SECONDS:-180}
POLL_SECONDS=${POLL_SECONDS:-10}

# SIGQUIT dumps every goroutine regardless, but say so explicitly rather than
# relying on a default that a future GOTRACEBACK in the job env could weaken.
GOTRACEBACK=${GOTRACEBACK:-all}
export GOTRACEBACK

OUT=$(mktemp)
trap 'rm -f "$OUT" 2>/dev/null || true' EXIT INT TERM

# Every process in the tree is a Go program - the `go` tool, `compile`, `link`,
# the test binary - and the Go runtime answers SIGQUIT by dumping all goroutine
# stacks. Signalling only `go` would dump the one process least likely to be the
# wedged one, so walk the tree and signal each. `ps -o pid=,ppid=` is the one
# format both busybox (alpine, the CI image) and macOS agree on; `setsid` plus a
# process-group kill is neater but busybox and util-linux `setsid` differ on
# whether they fork, which makes $! mean different things on the two platforms.
#
# The SELECTION flag is where the two diverge, not the format: bare `ps -o ...`
# with no selector defaults to "processes on my controlling terminal" on BSD/macOS
# ps, which a backgrounded script very often does not have - the walk then finds
# zero children even though they exist (reproduced live: `go test`'s own compiled
# test binary, a direct child, was invisible to the bare form and only appeared
# under `-A`). BusyBox ps has no such tty filter - it lists everything already -
# and some builds reject an unrecognized `-A`. Probe once for the flag this host's
# ps accepts and reuse it, rather than assuming either behavior.
if ps -A -o pid= >/dev/null 2>&1; then
    _PS_ALL_FLAG=-A
else
    _PS_ALL_FLAG=
fi

signal_tree() {
    _sig=$1
    _root=$2

    # Collect the whole tree BEFORE signalling anything. `go test` relays its
    # compiled test binary's output through itself (not a bare inherited fd) -
    # confirmed live: signalling the root first stops that relay before the
    # child's own dump can reach it, and the dump is silently lost even though
    # the child received SIGQUIT and did write it. Deepest processes must be
    # signalled - and given a moment to write - before their ancestors.
    _all=$_root
    _gen=$_root
    _i=0
    while [ -n "$_gen" ] && [ "$_i" -lt 12 ]; do
        _next=""
        for _p in $_gen; do
            _kids=$(ps $_PS_ALL_FLAG -o pid=,ppid= 2>/dev/null | awk -v pp="$_p" '$2 == pp { print $1 }')
            _next="$_next $_kids"
        done
        _all="$_all $_next"
        _gen=$_next
        _i=$((_i + 1))
    done

    _reversed=""
    for _p in $_all; do
        _reversed="$_p $_reversed"
    done
    for _p in $_reversed; do
        kill -"$_sig" "$_p" 2>/dev/null || true
        # Give a leaf a moment to write its dump through the still-alive parent
        # before that parent is signalled too - only matters between generations,
        # but a fixed short pause per pid is simpler than tracking depth here and
        # the tree is small (a handful of pids at most).
        sleep 0.2 2>/dev/null || sleep 1
    done
}

go test "$@" >"$OUT" 2>&1 &
GO_PID=$!

# Stream to the job log so a healthy run looks exactly as it did before.
tail -f "$OUT" &
TAIL_PID=$!

stop_tail() {
    sleep 1
    kill "$TAIL_PID" 2>/dev/null || true
    wait "$TAIL_PID" 2>/dev/null || true
}

LAST_SIZE=-1
QUIET_FOR=0

while kill -0 "$GO_PID" 2>/dev/null; do
    sleep "$POLL_SECONDS"
    SIZE=$(wc -c <"$OUT" | tr -d ' ')
    if [ "$SIZE" = "$LAST_SIZE" ]; then
        QUIET_FOR=$((QUIET_FOR + POLL_SECONDS))
    else
        QUIET_FOR=0
        LAST_SIZE=$SIZE
    fi

    if [ "$QUIET_FOR" -ge "$STALL_SECONDS" ]; then
        echo ""
        echo "=============================================================================="
        echo "STALLED: no output from 'go test' for ${QUIET_FOR}s (limit ${STALL_SECONDS}s)."
        echo "Sending SIGQUIT to the go test process tree - every Go process in it dumps"
        echo "all goroutine stacks below. The wedged one is the point of this job now."
        echo "=============================================================================="
        echo ""
        signal_tree QUIT "$GO_PID"

        # Let the dumps land in $OUT and reach the log before tearing anything down.
        sleep 15
        signal_tree KILL "$GO_PID"
        stop_tail

        echo ""
        echo "go test was killed after stalling. Failing the job: a wedged run must not"
        echo "sit on a shared runner until the job timeout."
        exit 124
    fi
done

GO_STATUS=0
wait "$GO_PID" || GO_STATUS=$?
stop_tail
exit "$GO_STATUS"
