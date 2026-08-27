#!/bin/sh
#
# Red/green control for go-test-stall-watchdog.sh.
#
# The watchdog's whole job is to fire on a case we cannot reproduce on demand in
# CI. That makes it exactly the kind of guard that rots unnoticed: it looks alive
# because it is never contradicted. So this script contradicts it on purpose.
#
# Four cases, and the first two are the load-bearing ones:
#
#   RED    a deadlocked test          -> must dump goroutine stacks AND exit non-zero
#   GREEN  an ordinary passing test   -> must exit 0 and stream the result through
#   RED    an ordinary failing test   -> must preserve go test's non-zero exit
#   GREEN  a slow-but-talking test    -> must NOT fire while output keeps arriving
#
# The last one matters as much as the first: a watchdog that kills healthy long
# runs is worse than none, because it teaches everyone to raise the limit until
# it stops mattering.
#
# Exit 0 = the watchdog discriminates. Non-zero = do not trust it.

set -eu

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
	ch := make(chan struct{})
	<-ch
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
	for i := 0; i < 6; i++ {
		t.Logf("still alive %d", i)
		time.Sleep(2 * time.Second)
	}
}
EOF

echo "case 1 (RED): deadlocked test must produce a stack dump and fail"
STALL_SECONDS=10 POLL_SECONDS=2 sh "$WATCHDOG" . \
    -run TestDeliberateDeadlockForWatchdogControl -count=1 -timeout 0 \
    >"$WORK/deadlock.log" 2>&1 && RC=0 || RC=$?

if [ "$RC" -eq 0 ]; then
    fail "watchdog exited 0 on a deadlocked test"
else
    pass "non-zero exit ($RC)"
fi
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

echo "case 4 (GREEN): slow but talking test must NOT be killed"
STALL_SECONDS=6 POLL_SECONDS=2 sh "$WATCHDOG" . \
    -run TestSlowButTalking -count=1 -v >"$WORK/chatty.log" 2>&1 && RC=0 || RC=$?
if [ "$RC" -eq 0 ]; then
    pass "survived a run longer than STALL_SECONDS while producing output"
else
    fail "killed a healthy long run (exit $RC) - watchdog is measuring duration, not silence"
    tail -15 "$WORK/chatty.log"
fi

echo ""
if [ "$FAILURES" -eq 0 ]; then
    echo "go-test-stall-watchdog: all controls passed"
    exit 0
fi
echo "go-test-stall-watchdog: $FAILURES control(s) FAILED - the watchdog is not trustworthy"
exit 1
