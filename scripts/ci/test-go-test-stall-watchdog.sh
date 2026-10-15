#!/bin/sh
#
# Red/green control for go-test-stall-watchdog.sh.
#
# The watchdog's whole job is to fire on a case we cannot reproduce on demand in
# CI. That makes it exactly the kind of guard that rots unnoticed: it looks alive
# because it is never contradicted. So this script contradicts it on purpose.
#
# Five cases, and the first two are the load-bearing ones:
#
#   RED    a deadlocked test          -> must dump goroutine stacks AND exit non-zero
#   GREEN  an ordinary passing test   -> must exit 0 and stream the result through
#   RED    an ordinary failing test   -> must preserve go test's non-zero exit
#   GREEN  a slow-but-talking test    -> must NOT fire while output keeps arriving
#   GREEN  a silent CPU-busy test     -> must NOT fire while CPU keeps being used
#                                        (a package `go test` reports only at its end,
#                                        on a starved runner: Mesh #362289cb)
#
# The last one matters as much as the first: a watchdog that kills healthy long
# runs is worse than none, because it teaches everyone to raise the limit until
# it stops mattering.
#
# Exit 0 = the watchdog discriminates. Non-zero = do not trust it.
#
# ## Proving the controls themselves: --prove-controls
#
# A control that has never been seen to fail proves nothing. With
# `--prove-controls` the script runs the same cases against three deliberately
# broken copies of the watchdog and requires each to fail for its own reason:
#
#   no-sigquit      never sends SIGQUIT   -> case 1 fails with "no goroutine dump"
#   ignores-output  output does not count -> case 4 fails with "killed a healthy long run"
#   ignores-cpu     CPU use does not count-> case 5 fails with "killed a silent CPU-bound run"
#
# One line per control: `control <name>: failed as expected`. A control that does
# not fail, fails for another reason (the test never started, say), or whose
# mutation no longer applies to the watchdog source, makes the script exit
# non-zero. CI runs this mode after the normal one (about a minute).
#
# ## Why each case waits for its test to be running
#
# The watchdog's clock starts when `go test` starts, so it also times the build
# of the control package. On a CPU-throttled runner with a cold build cache
# that build alone outlasts the short STALL_SECONDS used below: the watchdog
# then fires before any test exists, SIGQUITs the compiler or linker, and the
# case reports a missing dump (RED) or a killed healthy run (GREEN) although the
# watchdog behaved correctly. Reproduced in an alpine container limited to 0.3
# CPU: a cold build of the control package took 96 s, a warm one about 1 s.
#
# So the control package is built once, up front, under a generous limit of its
# own (also guarded by the watchdog), and every case then measures only the run.
# Each test also drops a marker file when it starts running. A stall that fires
# with no marker is reported as exactly that - the test never started - instead
# of as a watchdog defect. The cases keep their meaning; only the margins and
# the point where the clock matters changed.

set -eu

MODE=run
case "${1:-}" in
    "") ;;
    --prove-controls) MODE=prove ;;
    *) echo "usage: $0 [--prove-controls]" >&2; exit 2 ;;
esac

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WATCHDOG="$HERE/go-test-stall-watchdog.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK" 2>/dev/null || true' EXIT INT TERM

FAILURES=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAILURES=$((FAILURES + 1)); }

mkdir -p "$WORK/mod"
cd "$WORK/mod"
cat > go.mod <<'EOF'
module stallcontrol

go 1.22
EOF

cat > deadlock_test.go <<'EOF'
package stallcontrol

import (
	"testing"
	"time"
)

// Blocks forever on a channel nobody sends to - the shape of the CI wedge this
// watchdog exists for. Named so it is unmistakable in a goroutine dump.
//
// The ticker is not decoration. Without it every goroutine is blocked on a sync
// primitive, Go's own runtime deadlock detector fires ("all goroutines are
// asleep"), and the process dies on its own - which would make this control pass
// while proving nothing about the watchdog. A live timer is also what the real
// case looks like: a wedged process almost never has a completely quiet runtime.
func TestDeliberateDeadlockForWatchdogControl(t *testing.T) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	go func() {
		for range tick.C {
		}
	}()
	markRunning("deadlock")
	ch := make(chan struct{})
	<-ch
}
EOF

# Each test marks "I am running now" so a result can be attributed to the test and
# not to the build in front of it.
mkdir -p "$WORK/ready"
STALL_READY_DIR="$WORK/ready"
export STALL_READY_DIR
cat > ready_test.go <<'EOF'
package stallcontrol

import (
	"os"
	"path/filepath"
)

func markRunning(name string) {
	if dir := os.Getenv("STALL_READY_DIR"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, name), nil, 0o600)
	}
}
EOF

cat > ok_test.go <<'EOF'
package stallcontrol

import "testing"

func TestOrdinaryPass(t *testing.T) {}
EOF

cat > fail_test.go <<'EOF'
package stallcontrol

import "testing"

func TestOrdinaryFail(t *testing.T) { t.Fatal("deliberate failure for the control") }
EOF

cat > chatty_test.go <<'EOF'
package stallcontrol

import (
	"testing"
	"time"
)

// Runs longer than STALL_SECONDS but keeps printing. The watchdog must let it
// finish: it watches for silence, not for duration.
func TestSlowButTalking(t *testing.T) {
	markRunning("chatty")
	for i := 0; i < 7; i++ {
		t.Logf("still alive %d", i)
		time.Sleep(2 * time.Second)
	}
}
EOF

cat > busy_test.go <<'EOF'
package stallcontrol

import (
	"testing"
	"time"
)

// Silent (go test prints nothing until the package ends) but burning CPU for
// longer than STALL_SECONDS: a slow package, not a wedge.
func TestSilentButBusy(t *testing.T) {
	markRunning("busy")
	end := time.Now().Add(18 * time.Second)
	x := 0
	for time.Now().Before(end) {
		x++
	}
	_ = x
}
EOF

echo "warm-up: build the control package once, outside every measured case"
# The watchdog guards its own warm-up: a wedged build must fail the job with a
# dump, not sit on the runner until the job timeout. 180 s of silence without CPU
# is far beyond any slow build.
STALL_SECONDS=180 POLL_SECONDS=5 sh "$WATCHDOG" . -run '^$' -count=1 >"$WORK/warm.log" 2>&1 && RC=0 || RC=$?
if [ "$RC" -ne 0 ]; then
    echo "  FAIL: could not build the control package (exit $RC), so no case can be judged"
    tail -25 "$WORK/warm.log"
    exit 1
fi
echo "  built"

# started NAME: did that test reach its body? Absent means the stall (or a kill)
# happened during the build or the start, before the test was running.
started() { [ -e "$WORK/ready/$1" ]; }

# case1, case4 and case5 take the watchdog to test as $1, so --prove-controls can
# run them against broken copies. Markers left by an earlier run must not vouch
# for this one, hence the rm.
case1() {
    WD=$1
    rm -f "$WORK"/ready/*
    echo "case 1 (RED): deadlocked test must produce a stack dump and fail"
    STALL_SECONDS=15 POLL_SECONDS=2 sh "$WD" . \
        -run TestDeliberateDeadlockForWatchdogControl -count=1 -timeout 0 \
        >"$WORK/deadlock.log" 2>&1 && RC=0 || RC=$?

    if [ "$RC" -eq 0 ]; then
        fail "watchdog exited 0 on a deadlocked test"
    else
        pass "non-zero exit ($RC)"
    fi
    if ! started deadlock; then
        # Nothing below can be judged: no test was running, so a missing banner
        # or dump says nothing about the watchdog. Report only this, and fail.
        fail "the deadlocked test never started: the stall fired during the build or start, so this run says nothing about the dump"
        tail -15 "$WORK/deadlock.log"
        return 0
    fi
    pass "the deadlocked test was running when the stall fired"
    if grep -q "STALLED: no output" "$WORK/deadlock.log"; then
        pass "stall was announced"
    else
        fail "no stall banner in output"
    fi
    # The point of the whole exercise: a NAME, not silence.
    # The goroutine-header pattern tolerates the extra `gp=0x... m=... mp=...`
    # metadata newer Go runtimes insert between the number and the state bracket
    # (go1.26 here) as well as the older, plainer `goroutine N [state]:` form.
    if grep -q "TestDeliberateDeadlockForWatchdogControl" "$WORK/deadlock.log" &&
       grep -qE "goroutine [0-9]+ .*\[" "$WORK/deadlock.log"; then
        pass "goroutine dump names the blocked test"
    else
        fail "no goroutine dump naming the blocked test - the watchdog fired but produced no evidence"
        echo "--- captured output (tail) ---"
        tail -25 "$WORK/deadlock.log"
        echo "------------------------------"
    fi
}

case4() {
    WD=$1
    rm -f "$WORK"/ready/*
    echo "case 4 (GREEN): slow but talking test must NOT be killed"
    STALL_SECONDS=10 POLL_SECONDS=2 sh "$WD" . \
        -run TestSlowButTalking -count=1 -v >"$WORK/chatty.log" 2>&1 && RC=0 || RC=$?
    if [ "$RC" -eq 0 ]; then
        pass "survived a run longer than STALL_SECONDS while producing output"
    elif ! started chatty; then
        fail "killed before the test started (exit $RC): the build or start outlasted STALL_SECONDS, which says nothing about duration vs silence"
        tail -15 "$WORK/chatty.log"
    else
        fail "killed a healthy long run (exit $RC) - watchdog is measuring duration, not silence"
        tail -15 "$WORK/chatty.log"
    fi
}

case5() {
    WD=$1
    rm -f "$WORK"/ready/*
    echo "case 5 (GREEN): silent but CPU-busy test must NOT be killed"
    STALL_SECONDS=12 POLL_SECONDS=2 sh "$WD" . \
        -run TestSilentButBusy -count=1 >"$WORK/busy.log" 2>&1 && RC=0 || RC=$?
    if [ "$RC" -eq 0 ]; then
        pass "survived silence longer than STALL_SECONDS while using CPU"
    elif ! started busy; then
        fail "killed before the test started (exit $RC): the build or start outlasted STALL_SECONDS, which says nothing about CPU-bound runs"
        tail -8 "$WORK/busy.log"
    else
        fail "killed a silent CPU-bound run (exit $RC) - watchdog is treating a slow package as a wedge"
        tail -8 "$WORK/busy.log"
    fi
}

# mutate NAME FROM TO: a copy of the watchdog with one whole line replaced. The
# line must exist exactly once, or the control would silently test the real
# watchdog.
mutate() {
    _name=$1
    _copy="$WORK/watchdog-$_name.sh"
    _count=$(grep -cxF -- "$2" "$WATCHDOG" || true)
    if [ "$_count" -ne 1 ]; then
        echo "control $_name: mutation target found $_count times in the watchdog (want 1) - update this script" >&2
        return 1
    fi
    FROM=$2 TO=$3 awk '$0 == ENVIRON["FROM"] { print ENVIRON["TO"]; next } { print }' "$WATCHDOG" >"$_copy"
    printf '%s\n' "$_copy"
}

# expect_red NAME CASE WATCHDOG REASON: run one case against a broken watchdog. It
# must fail, and with the case's own verdict (REASON), not some other breakage.
expect_red() {
    _name=$1 _case=$2 _wd=$3 _reason=$4
    FAILURES=0
    "$_case" "$_wd" >"$WORK/control-$_name.log" 2>&1 || true
    if [ "$FAILURES" -gt 0 ] && grep -q -- "$_reason" "$WORK/control-$_name.log"; then
        echo "control $_name: failed as expected"
    else
        echo "control $_name: DID NOT FAIL AS EXPECTED (failures=$FAILURES, wanted: $_reason)"
        tail -20 "$WORK/control-$_name.log"
        CONTROL_BAD=$((CONTROL_BAD + 1))
    fi
}

if [ "$MODE" = prove ]; then
    CONTROL_BAD=0
    ACTIVITY='    if [ "$SIZE" = "$LAST_SIZE" ] && [ "$CPU" = "$LAST_CPU" ]; then'
    WD_QUIT=$(mutate no-sigquit '        signal_tree QUIT "$GO_PID"' '        :') || exit 1
    WD_OUT=$(mutate ignores-output "$ACTIVITY" '    if [ "$CPU" = "$LAST_CPU" ]; then') || exit 1
    WD_CPU=$(mutate ignores-cpu "$ACTIVITY" '    if [ "$SIZE" = "$LAST_SIZE" ]; then') || exit 1
    expect_red no-sigquit case1 "$WD_QUIT" "no goroutine dump naming the blocked test"
    expect_red ignores-output case4 "$WD_OUT" "killed a healthy long run"
    expect_red ignores-cpu case5 "$WD_CPU" "killed a silent CPU-bound run"
    echo ""
    if [ "$CONTROL_BAD" -eq 0 ]; then
        echo "go-test-stall-watchdog: every broken watchdog was caught"
        exit 0
    fi
    echo "go-test-stall-watchdog: $CONTROL_BAD control(s) did not discriminate - the self-check cannot be trusted"
    exit 1
fi

case1 "$WATCHDOG"

echo "case 2 (GREEN): ordinary passing test must exit 0 and stream through"
STALL_SECONDS=60 POLL_SECONDS=2 sh "$WATCHDOG" . \
    -run TestOrdinaryPass -count=1 >"$WORK/ok.log" 2>&1 && RC=0 || RC=$?
if [ "$RC" -eq 0 ]; then pass "exit 0"; else fail "passing test exited $RC"; fi
if grep -q "^ok" "$WORK/ok.log"; then pass "go test output reached the log"; else fail "go test output was swallowed"; fi

echo "case 3 (RED): ordinary failing test must keep its non-zero exit"
STALL_SECONDS=60 POLL_SECONDS=2 sh "$WATCHDOG" . \
    -run TestOrdinaryFail -count=1 >"$WORK/fail.log" 2>&1 && RC=0 || RC=$?
if [ "$RC" -ne 0 ] && [ "$RC" -ne 124 ]; then
    pass "propagated go test's failure exit ($RC), not the stall code"
else
    fail "expected a plain test failure exit, got $RC"
fi
if grep -q "deliberate failure for the control" "$WORK/fail.log"; then
    pass "failure text preserved"
else
    fail "failure text lost"
fi

case4 "$WATCHDOG"
case5 "$WATCHDOG"

echo ""
if [ "$FAILURES" -eq 0 ]; then
    echo "go-test-stall-watchdog: all controls passed"
    exit 0
fi
echo "go-test-stall-watchdog: $FAILURES control(s) FAILED - the watchdog is not trustworthy"
exit 1
