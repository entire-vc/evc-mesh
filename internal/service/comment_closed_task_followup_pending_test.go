package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// ---------------------------------------------------------------------------
// Pending delivery (#db1c6c7a, P2 of #9b712414): a read error while searching
// for the finding's root is NOT "dedup clean".
//
// The contract being pinned here, in the order the ACs name it:
//
//	persistent read error → NO root, a pending row, a visible notice on source
//	transient read error  → inline retry delivers, exactly 1 root, no pending
//	enqueue itself fails  → the residual case: logged ERROR + metric, comment stands
//
// The first test of the three is the RED test of the whole card: before P2 a
// read error was a log line and silence — no root (that half held), but also
// no pending row and no notice, so the finding was effectively lost until a
// human happened to reread the closed card. It was written first and run
// against the unfixed code to fail for exactly those two missing halves.
// ---------------------------------------------------------------------------

// collapseFollowUpRetrySleeps makes the retry ladders instant: the tests
// assert WHAT the ladder does (attempt counts, eventual outcomes), never how
// long it pauses between attempts.
func collapseFollowUpRetrySleeps(t *testing.T) {
	t.Helper()
	origRoot := followUpRootRetrySleep
	followUpRootRetrySleep = func(time.Duration) {}
	origDelivery := followUpDeliveryRetrySleep
	followUpDeliveryRetrySleep = func(time.Duration) {}
	t.Cleanup(func() {
		followUpRootRetrySleep = origRoot
		followUpDeliveryRetrySleep = origDelivery
	})
}

// closedFollowUpErrorCount reads the closed_followup_error counter at one
// exact (stage, op) label pair, so a test can assert the metric MOVED for the
// reason it expects, whatever unrelated stages fired before it.
func closedFollowUpErrorCount(t *testing.T, stage, op string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.ClosedFollowUpErrorsTotal.WithLabelValues(stage, op))
}

// TestClosedFollowUp_ReadErrorGoesPendingNotFailOpen is the card's red test:
// the identity store is unreadable for the whole delivery, and the finding
// must not silently vanish. Before P2 this produced a WARNING log and nothing
// else — the test failed on the pending row and on the notice.
func TestClosedFollowUp_ReadErrorGoesPendingNotFailOpen(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})

	c := env.comment(t, "находка: в отчёте цифры не сходятся с продом")

	// The contract's first half, which already held in P1: a read error is
	// never "clean", so no root card is minted on the strength of nothing.
	assert.Empty(t, env.taskSvc.createdTasks(), "read error must not create a root")

	// …and P2's half: the failure is VISIBLE — parked for reconcile…
	row, ok := env.pendingRepo.items[c.ID]
	require.True(t, ok, "a failed delivery must park a pending row keyed by the comment")
	assert.Equal(t, env.sourceID, row.SourceTaskID)
	assert.Zero(t, row.Attempts, "attempts counts reconcile passes, not the inline ladder")
	assert.NotEmpty(t, row.LastError, "last_error carries the cause for the human who finds the row")
	assert.Equal(t, closedFindingKey(c), row.FindingKey, "the pending row keys the SAME finding identity the delivery used")

	// …and said so on the source card, where the commenter is looking.
	env.oneSystemCommentContaining(t, env.sourceID, "не подтверждена")
	env.oneSystemCommentContaining(t, env.sourceID, "повторю автоматически")
}

// TestClosedFollowUp_TransientReadErrorRetriesInlineAndDelivers: the store
// flaps once — exactly the shape the inline ladder exists for. One failure,
// then the retry lands, and the finding never needs the queue at all.
func TestClosedFollowUp_TransientReadErrorRetriesInlineAndDelivers(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.failClaimFirstN = 1
	})

	c := env.comment(t, "находка: миграция не в той последовательности applied")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "a transient error followed by a healthy retry delivers exactly one root")
	assert.Equal(t, domain.StatusCategoryTodo, env.statusRepo.items[created[0].StatusID].Category)

	_, parked := env.pendingRepo.items[c.ID]
	assert.False(t, parked, "a delivery that landed on retry must not occupy the pending queue")
	env.oneSystemCommentContaining(t, env.sourceID, "заведена")

	// And the ladder actually retried rather than succeeding by accident:
	// the first Claim call failed by script, the root exists, so the second
	// call must have happened.
	env.rootsRepo.mu.Lock()
	claims := env.rootsRepo.claimCalls
	env.rootsRepo.mu.Unlock()
	assert.GreaterOrEqual(t, claims, 2, "the inline ladder must retry a failed claim")
}

// TestClosedFollowUp_PendingEnqueueFailsIsTheResidualCase: the delivery
// failed AND the pending row cannot be written — the database is wholly down.
// Nothing more can be done inline (this is the one residual case the MR
// names), but it must still be loud: the closed_followup_error counter moves
// and the comment itself stands untouched.
func TestClosedFollowUp_PendingEnqueueFailsIsTheResidualCase(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
		e.pendingRepo.errToReturn = fmt.Errorf("pending store unreachable too")
	})

	before := closedFollowUpErrorCount(t, "enqueue", "enqueue_pending")
	env.comment(t, "находка: очередь доставки тоже лежит")
	after := closedFollowUpErrorCount(t, "enqueue", "enqueue_pending")

	assert.Empty(t, env.taskSvc.createdTasks())
	assert.Empty(t, env.pendingRepo.items, "nothing can be parked when the queue itself is down")
	assert.Greater(t, after, before, "the residual case must still move the closed_followup_error metric (stage=enqueue)")
}

// ---------------------------------------------------------------------------
// Reconcile (#db1c6c7a): the scheduler half. The delivery is the SAME
// function the inline path used, so these pin the queue mechanics — recovery
// after the outage heals, idempotency of a redelivered row, and the budget's
// edge — not the delivery shapes already pinned above.
// ---------------------------------------------------------------------------

// TestClosedFollowUp_ReconcileDeliversAfterRecovery: the outage that parked
// the finding heals; the next pass delivers exactly one root and empties the
// queue. Nothing about the delivery is special to the reconcile path — only
// the retry it performed is new.
func TestClosedFollowUp_ReconcileDeliversAfterRecovery(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: индекс не покрывает запрос из отчёта")
	require.Contains(t, env.pendingRepo.items, c.ID, "sanity: the finding is parked")

	env.rootsRepo.errToReturn = nil // the store heals
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	assert.Zero(t, retried)
	assert.Zero(t, escalated)

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "reconcile delivers exactly one root for one parked finding")
	assert.Equal(t, domain.StatusCategoryTodo, env.statusRepo.items[created[0].StatusID].Category)
	assert.Empty(t, env.pendingRepo.items, "a delivered row leaves the queue")
	env.oneSystemCommentContaining(t, env.sourceID, "заведена")
}

// TestClosedFollowUp_ReconcileRedeliveryIsIdempotent: the crash window
// between "delivery succeeded" and "pending row deleted" — a row that comes
// back must not produce a SECOND root. The redelivered comment re-enters
// through Claim, loses to its own root's identity, and takes the repeat
// branch: root already open, remark acknowledged, row dropped.
func TestClosedFollowUp_ReconcileRedeliveryIsIdempotent(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: гонка в шедулере очереди")

	env.rootsRepo.errToReturn = nil
	_, _, _, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	require.Len(t, env.taskSvc.createdTasks(), 1)

	// The crash: the row survives a pass that already delivered. Re-seed it
	// by hand — the queue's own redelivery shape, notice stamp included (a
	// healthy enqueue landed it long before the delivery ran) — and run
	// another pass.
	noticed := timeNow()
	row := domain.ClosedFollowUpPending{
		CommentID: c.ID, SourceTaskID: env.sourceID,
		FindingKey: closedFindingKey(c), Attempts: 1,
		LastError: "simulated crash before delete", CreatedAt: timeNow(),
		NoticedAt: &noticed,
	}
	env.pendingRepo.items[c.ID] = &row

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered, "the stale row is worked and dropped, not skipped")
	assert.Zero(t, retried)
	assert.Zero(t, escalated)
	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"a redelivered finding must find its existing root, never open a second one")
	assert.Empty(t, env.pendingRepo.items)
}

// TestClosedFollowUp_ReconcileEscalatesAtTheBudgetAndStops: ten failed
// passes end in exactly one «нужен человек» notice with the row KEPT (a human
// finding it sees the last_error), and an eleventh pass lists nothing — the
// budget is a stop, not a forever.
func TestClosedFollowUp_ReconcileEscalatesAtTheBudgetAndStops(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: магазин лежит и не поднимается")

	var lastEscalated int
	for i := 1; i <= ClosedFollowUpReconcileMaxAttempts; i++ {
		_, _, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
		if escalated > 0 {
			lastEscalated = i
		}
	}
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, lastEscalated,
		"the escalation lands exactly on the budget's last pass, not before")

	env.oneSystemCommentContaining(t, env.sourceID, "нужен человек")

	row, ok := env.pendingRepo.items[c.ID]
	require.True(t, ok, "an escalated row stays for the human — deleting it would be the silent loss again")
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts)
	assert.NotEmpty(t, row.LastError)

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered, "pass 11 must list nothing: attempts<10 excludes the escalated row")
	assert.Zero(t, retried)
	assert.Zero(t, escalated)
}

// ---------------------------------------------------------------------------
// Reconcile branch coverage: the loop's error and cleanup exits. Each test
// parks a finding the honest way (a failing inline delivery) and then breaks
// exactly ONE fact, so a regression in any single branch fails its own test
// rather than hiding inside a longer scenario.
// ---------------------------------------------------------------------------

// repark re-seeds a pending row by hand — the queue's own redelivery shape
// after a crash between "delivered" and "row deleted", reused by the repeat
// branch tests below. NoticedAt is stamped because that crash window sits
// AFTER a healthy enqueue: the park notice landed and stamped long before the
// delivery ran, and a re-seed without the stamp is a state the healthy-store
// scenarios below can no longer produce (round 6 made the stamp part of the
// row's retirement contract).
func (env followUpEnv) repark(t *testing.T, c *domain.Comment, attempts int) {
	t.Helper()
	env.pendingRepo.mu.Lock()
	defer env.pendingRepo.mu.Unlock()
	noticed := timeNow()
	env.pendingRepo.items[c.ID] = &domain.ClosedFollowUpPending{
		CommentID: c.ID, SourceTaskID: env.sourceID,
		FindingKey: closedFindingKey(c), Attempts: attempts,
		LastError: "re-seeded for the branch under test", CreatedAt: timeNow(),
		NoticedAt: &noticed,
	}
}

// parkedRow reads the pending row with its current attempt count.
func (env followUpEnv) parkedRow(t *testing.T, commentID uuid.UUID) *domain.ClosedFollowUpPending {
	t.Helper()
	env.pendingRepo.mu.Lock()
	defer env.pendingRepo.mu.Unlock()
	if row, ok := env.pendingRepo.items[commentID]; ok {
		return row
	}
	return nil
}

// A cancelled context stops the pass BEFORE working any row: the scheduler's
// shutdown path must leave the queue exactly as it found it.
func TestClosedFollowUp_ReconcileStopsOnCancelledContext(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: контекст потух между проходами")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(ctx)
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried)
	assert.Zero(t, escalated)

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row, "an unworked row must survive the pass untouched")
	assert.Zero(t, row.Attempts)
}

// The comment read fails: an attempt is counted with op=comment_read, the row
// stays, nothing is delivered — an outage of the comment store is not a
// reason to drop or force-deliver the finding. Run to the budget's edge: the
// escalation must fire through THIS op too, exactly once, on the last pass.
func TestClosedFollowUp_ReconcileCommentReadErrorCountsAttempt(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: комментарий не читается из сторы")

	before := closedFollowUpErrorCount(t, "reconcile", "comment_read")
	env.rootsRepo.errToReturn = nil // identity store healed…
	env.commentRepo.errToReturn = fmt.Errorf("comment store unreachable")

	var escalatedPass, escalatedTotal int
	for i := 1; i <= ClosedFollowUpReconcileMaxAttempts; i++ {
		delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
		assert.Zero(t, delivered)
		assert.Equal(t, 1, retried)
		escalatedTotal += escalated
		if escalated > 0 {
			escalatedPass = i
		}
	}
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, escalatedPass,
		"the comment_read budget escalates on its last pass, like any other op")
	assert.Equal(t, 1, escalatedTotal)
	assert.Greater(t, closedFollowUpErrorCount(t, "reconcile", "comment_read"), before)

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts)
	assert.Contains(t, row.LastError, "comment store unreachable")
}

// The remark itself is gone (deleted by an admin, wiped with the card):
// nothing left to deliver — the row is dropped silently, not counted as a
// failure and not escalated. A deleted remark is not an outage.
func TestClosedFollowUp_ReconcileDropsRowWhenCommentVanished(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: замечание потом удалили вручную")

	env.rootsRepo.errToReturn = nil
	env.commentRepo.mu.Lock()
	delete(env.commentRepo.items, c.ID)
	env.commentRepo.mu.Unlock()

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried, "a vanished remark is a cleanup, not a failed attempt")
	assert.Zero(t, escalated)
	assert.Nil(t, env.parkedRow(t, c.ID), "the row is dropped with the remark it pointed at")
	assert.Empty(t, env.taskSvc.createdTasks())
}

// The source card read fails: op=task_read, attempt counted, row stays, and
// the budget's edge behaves identically to every other op.
func TestClosedFollowUp_ReconcileTaskReadErrorCountsAttempt(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: карточка-источник не читается")

	before := closedFollowUpErrorCount(t, "reconcile", "task_read")
	env.rootsRepo.errToReturn = nil
	env.taskRepo.errToReturn = fmt.Errorf("task store unreachable")

	var escalatedPass, escalatedTotal int
	for i := 1; i <= ClosedFollowUpReconcileMaxAttempts; i++ {
		delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
		assert.Zero(t, delivered)
		assert.Equal(t, 1, retried)
		escalatedTotal += escalated
		if escalated > 0 {
			escalatedPass = i
		}
	}
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, escalatedPass)
	assert.Equal(t, 1, escalatedTotal)
	assert.Greater(t, closedFollowUpErrorCount(t, "reconcile", "task_read"), before)

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts)
}

// The source card itself is gone: same cleanup logic as the vanished remark —
// drop the row, deliver nothing, count no failure.
func TestClosedFollowUp_ReconcileDropsRowWhenSourceTaskVanished(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: карточку-источник потом снесли")

	env.rootsRepo.errToReturn = nil
	env.taskRepo.mu.Lock()
	delete(env.taskRepo.items, env.sourceID)
	env.taskRepo.mu.Unlock()

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried)
	assert.Zero(t, escalated)
	assert.Nil(t, env.parkedRow(t, c.ID))
	assert.Empty(t, env.taskSvc.createdTasks())
}

// The finding delivered but the pending row could not be deleted (the crash
// window's write side): the delivery still counts, the row stays, and the
// next pass re-delivers IDEMPOTENTLY — same root, repeat branch, no second
// card. The queue may be sticky; the dedup must not be.
func TestClosedFollowUp_ReconcileDeliveredButDeleteFailsStaysIdempotent(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	c := env.comment(t, "находка: строка очереди не удаляется после доставки")
	require.Len(t, env.taskSvc.createdTasks(), 1, "sanity: delivered inline while healthy")

	env.repark(t, c, 1)
	env.pendingRepo.deleteErr = fmt.Errorf("delete failed")

	for pass := 1; pass <= 2; pass++ {
		delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, delivered, "pass %d: the row is worked and reported delivered", pass)
		assert.Zero(t, retried)
		assert.Zero(t, escalated)
	}

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"a sticky undeletable row must re-deliver to the SAME root, never open a second one")
	require.NotNil(t, env.parkedRow(t, c.ID), "the row the queue cannot delete stays visible")
}

// The attempt counter itself is unwritable while the queue still works: the
// pass logs, retries anyway, and the budget NEVER advances — the counter's
// only job is deciding WHEN to escalate, not WHETHER to retry.
func TestClosedFollowUp_ReconcileMarkAttemptUnwritableRetriesWithoutBudget(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: счётчик попыток не пишется")
	env.pendingRepo.markErr = fmt.Errorf("counter unwritable")

	for pass := 0; pass < ClosedFollowUpReconcileMaxAttempts+2; pass++ {
		delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
		assert.Zero(t, delivered)
		assert.Equal(t, 1, retried, "every pass retries: an unwritable counter is not a stop")
		assert.Zero(t, escalated, "the budget cannot advance, so it must never claim to be exhausted")
	}

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Zero(t, row.Attempts, "no attempt was ever counted")
}

// followUpCauseExcerpt: the human-facing comment carries a bounded excerpt,
// the full cause rides in logs and last_error. Runes, not bytes — the cause
// of the day is as likely to be Russian as anything else.
func TestFollowUpCauseExcerptTruncatesLongCauses(t *testing.T) {
	short := "connection refused"
	assert.Equal(t, short, followUpCauseExcerpt(short))

	long := strings.Repeat("а", 300)
	got := followUpCauseExcerpt(long)
	assert.Len(t, []rune(got), 201, "200 runes of cause plus the ellipsis")
	assert.Equal(t, strings.Repeat("а", 200)+"…", got)
}

// The escalation notice's own write fails: the escalation STILL happened —
// counter moved, row kept at the budget — only the comment is missing. The
// alternative (swallowing the escalation because its notice failed) would be
// the silent loss again, one layer deeper.
func TestClosedFollowUp_EscalationNoticeFailureIsLoggedNotSwallowed(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
		e.commentRepo.createFailFor = func(cm *domain.Comment) bool {
			return cm.AuthorType == domain.ActorTypeSystem && strings.Contains(cm.Body, "нужен человек")
		}
	})
	c := env.comment(t, "находка: даже эскалация не пишется")

	var escalatedPass int
	for i := 1; i <= ClosedFollowUpReconcileMaxAttempts; i++ {
		_, _, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
		if escalated > 0 {
			escalatedPass = i
		}
	}
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, escalatedPass,
		"the escalation fires on the budget's edge even when its notice cannot be posted")

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts)

	for _, n := range env.systemNotices() {
		assert.NotContains(t, n.Body, "нужен человек",
			"the notice write failed by script — the row and the counter are the trail it leaves")
	}
}

// The todo-column read fails AFTER the claim was taken: the claim is released
// (a held claim would point at a card that may never exist) and the failure is
// retryable as todo_status_read.
func TestClosedFollowUp_ReconcileTodoStatusReadFailureReleasesClaim(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: статусы проекта не читаются")

	before := closedFollowUpErrorCount(t, "reconcile", "todo_status_read")
	env.rootsRepo.errToReturn = nil
	env.statusRepo.errToReturn = fmt.Errorf("status store unreachable")

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried)
	assert.Zero(t, escalated)
	assert.Greater(t, closedFollowUpErrorCount(t, "reconcile", "todo_status_read"), before)

	env.rootsRepo.mu.Lock()
	claims := len(env.rootsRepo.items)
	env.rootsRepo.mu.Unlock()
	assert.Zero(t, claims, "the taken claim must be released — no phantom pointing at a card that never exists")

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.Attempts)
}

// The repeat branch's identity read flaps while Claim still succeeds: the
// failure is retryable as get_root, the row stays, no second root.
func TestClosedFollowUp_ReconcileRepeatGetRootErrorIsRetryable(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	c := env.comment(t, "находка: чтение root флапнуло на повторе")
	require.Len(t, env.taskSvc.createdTasks(), 1)

	env.repark(t, c, 1)
	env.rootsRepo.getErr = fmt.Errorf("get flapped")

	before := closedFollowUpErrorCount(t, "reconcile", "get_root")
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried)
	assert.Zero(t, escalated)
	assert.Greater(t, closedFollowUpErrorCount(t, "reconcile", "get_root"), before)

	assert.Len(t, env.taskSvc.createdTasks(), 1)
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.Attempts)
}

// The identity row vanishes between Claim and Get (a release that raced us,
// manual cleanup): there is no root behind the claim, and the honest answer
// is a retryable get_root — a later pass re-claims and mints the root.
func TestClosedFollowUp_ReconcileVanishedRootRowIsRetryable(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	c := env.comment(t, "находка: identity-строка исчезла между claim и get")
	require.Len(t, env.taskSvc.createdTasks(), 1)

	env.repark(t, c, 1)
	key := closedFollowUpRootKey(env.sourceID, closedFindingKey(c))
	env.rootsRepo.onGet = func(call int, m *MockClosedFollowUpRootRepository) {
		if call != 1 {
			return
		}
		m.mu.Lock()
		delete(m.items, key)
		m.mu.Unlock()
	}

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried)
	assert.Zero(t, escalated)

	assert.Len(t, env.taskSvc.createdTasks(), 1, "no second root from a vanished identity")
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.Attempts)
}

// The root card's status read flaps on the repeat path: retryable as
// status_read — a working read may still find the root open and absorb the
// repeat without waking anyone.
func TestClosedFollowUp_ReconcileRepeatStatusReadErrorIsRetryable(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	c := env.comment(t, "находка: статус root не читается на повторе")
	require.Len(t, env.taskSvc.createdTasks(), 1)

	env.repark(t, c, 1)
	env.statusRepo.errToReturn = fmt.Errorf("status store unreachable")

	before := closedFollowUpErrorCount(t, "reconcile", "status_read")
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried)
	assert.Zero(t, escalated)
	assert.Greater(t, closedFollowUpErrorCount(t, "reconcile", "status_read"), before)

	assert.Len(t, env.taskSvc.createdTasks(), 1)
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.Attempts)
}

// The root CARD's table is unreadable while the identity store still works:
// the repeat branch reads the claim, then cannot read the card behind it.
// That is an outage, not a wedged winner — no read of the card ever
// succeeded — so it must reach the pending queue, not a terminal deferral.
// RED against the first cut of this MR: waitForRootCard collapsed
// "unreadable" into "absent", and a task-store outage bypassed the ladder
// entirely (codex-review P1).
func TestClosedFollowUp_RootCardReadErrorIsRetryableNotWedged(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	env.comment(t, "находка: карточку root не прочитать")
	require.Len(t, env.taskSvc.createdTasks(), 1, "sanity: the finding's root exists")

	before := closedFollowUpErrorCount(t, "inline", "root_card_read")
	rootID := env.taskSvc.createdTasks()[0].ID
	// The SOURCE card must stay readable (Create re-reads it at the door);
	// only the ROOT's lookups fail — the outage waitForRootCard must tell
	// apart from absence.
	env.taskRepo.getByIDErrFor = map[uuid.UUID]error{
		rootID: fmt.Errorf("task store unreadable"),
	}
	second := env.comment(t, "находка: карточку root не прочитать")

	assert.Len(t, env.taskSvc.createdTasks(), 1, "no second root for the same finding")
	row := env.parkedRow(t, second.ID)
	require.NotNil(t, row, "an unreadable root card must PARK the repeat, not defer it terminally")
	assert.Contains(t, row.LastError, "task store unreadable")
	assert.Greater(t, closedFollowUpErrorCount(t, "inline", "root_card_read"), before)
	env.oneSystemCommentContaining(t, env.sourceID, "не подтверждена")
}

// The same misclassification on the reconcile path would count the failed
// read as delivered and DELETE the pending row — the finding dropped from
// the very queue that exists to hold it. The row must stay and the attempt
// must count. The source card stays readable (only the ROOT's reads fail) so
// the pass reaches the delivery at all.
func TestClosedFollowUp_ReconcileRootCardReadErrorIsRetryable(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	c := env.comment(t, "находка: на повторе карточку root не прочитать")
	require.Len(t, env.taskSvc.createdTasks(), 1)
	rootID := env.taskSvc.createdTasks()[0].ID

	env.repark(t, c, 1)
	env.taskRepo.getByIDErrFor = map[uuid.UUID]error{
		rootID: fmt.Errorf("task store unreadable"),
	}

	before := closedFollowUpErrorCount(t, "reconcile", "root_card_read")
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered, "a read outage is not a delivery")
	assert.Equal(t, 1, retried)
	assert.Zero(t, escalated)
	assert.Greater(t, closedFollowUpErrorCount(t, "reconcile", "root_card_read"), before)

	assert.Len(t, env.taskSvc.createdTasks(), 1)
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row, "an unreadable root card must keep the row for the next pass")
	assert.Equal(t, 2, row.Attempts)
}

// The root points at a status row that resolves to nothing — broken
// referential state, not an outage. The repeat is delivered as a comment on
// the root (nothing swallowed) and the row leaves the queue: this is a
// terminal outcome, not something ten more passes could improve.
func TestClosedFollowUp_ReconcileUnresolvableRootStatusDeliversAsComment(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)
	c := env.comment(t, "находка: статус root ссылается в пустоту")
	require.Len(t, env.taskSvc.createdTasks(), 1)
	root := env.taskSvc.createdTasks()[0]

	env.repark(t, c, 1)
	env.statusRepo.mu.Lock()
	delete(env.statusRepo.items, root.StatusID)
	env.statusRepo.mu.Unlock()

	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered, "the remark landed on the root — a terminal, delivered outcome")
	assert.Zero(t, retried)
	assert.Zero(t, escalated)

	assert.Nil(t, env.parkedRow(t, c.ID), "a terminal outcome leaves the queue")
	assert.Len(t, env.taskSvc.createdTasks(), 1)
	env.oneSystemCommentContaining(t, root.ID, "повтор того же замечания")
}

// The SECOND half of the mixed-read story: the wait's classification is not
// "did ANY read succeed" but "what did the LAST read see". A read that
// succeeded mid-budget and found nothing does not settle absence — the winner
// may still be inserting its card — so a budget that ENDS on a failed read
// must classify as retryable (nil, err), never as the wedged (nil, nil).
// RED against the sticky-readOK cut of this MR: there an early successful
// empty read flipped a permanent "readOK" bit, and the later failures were
// answered with a wedged verdict on evidence that predated them
// (codex-review P1, round 2).
func TestFollowUpWaitForRootCard_MixedReadsClassifyByTheLastAttempt(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t)

	// A root id nobody ever inserted: a "successful" read here is the empty
	// read, exactly the mid-budget evidence that must NOT settle anything.
	rootID := uuid.New()
	env.taskRepo.mu.Lock()
	env.taskRepo.onGetByID = func(call int, _ uuid.UUID) error {
		if call < 3 {
			return nil // reads 1-2 succeed and find nothing
		}
		return fmt.Errorf("task store flapped on read %d", call)
	}
	env.taskRepo.mu.Unlock()

	card, err := env.svc.waitForRootCard(context.Background(), rootID)
	require.Error(t, err,
		"the budget ended on a FAILED read: the card's state is unknown, and unknown is retryable")
	assert.Nil(t, card)
	assert.Contains(t, err.Error(), "task store flapped")
}

// The requester departs mid-ladder (a cancelled request context is one of the
// ways the ladder ends), and the comment is ALREADY saved — the park must
// complete without them. The mock queue refuses a dead context the way a real
// store does, so before the detach the row bounced off the queue and the
// finding rode on the source comment alone (codex-review P2, round 2).
func TestClosedFollowUp_CancelledRequestStillParksTheFinding(t *testing.T) {
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})

	// Collapse the sleeps AND play the departing requester: by the pause
	// before rung 2, the request is gone.
	ctx, cancel := context.WithCancel(context.Background())
	origRoot := followUpRootRetrySleep
	origDelivery := followUpDeliveryRetrySleep
	followUpRootRetrySleep = func(time.Duration) {}
	followUpDeliveryRetrySleep = func(time.Duration) { cancel() }
	t.Cleanup(func() {
		followUpRootRetrySleep = origRoot
		followUpDeliveryRetrySleep = origDelivery
	})

	c := &domain.Comment{
		TaskID:     env.sourceID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "находка: запрос ушёл, а замечание уже сохранено",
	}
	require.NoError(t, env.svc.Create(ctx, c), "the comment itself is unaffected — it routed, never rejected")

	assert.Empty(t, env.taskSvc.createdTasks(), "no root: the outage held for the whole (shortened) ladder")
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row, "a finding whose requester is gone must still PARK — the comment outlives the request")
	assert.Equal(t, env.sourceID, row.SourceTaskID)
	assert.NotEmpty(t, row.LastError)
	env.oneSystemCommentContaining(t, env.sourceID, "не подтверждена")
}

// The escalation is a TRANSITION, not a level: two passes that raced on one
// row (both listed it at 9, MarkAttempt handed one the 10 and the other the
// 11) must produce exactly one «нужен человек», from the pass that drew the
// budget itself. Simulated by handing markPendingAttempt a row already AT the
// budget — its MarkAttempt draws max+1 — which is the losing side of that
// race in one call (codex-review P2, round 2).
func TestFollowUpMarkPendingAttempt_PastBudgetDoesNotEscalateAgain(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
	})
	c := env.comment(t, "находка: две проходки встретились на одной строке")

	env.repark(t, c, ClosedFollowUpReconcileMaxAttempts)
	row := env.parkedRow(t, c.ID)
	escalatedBefore := testutil.ToFloat64(metrics.ClosedFollowUpPendingTotal.WithLabelValues("escalated"))

	escalated := env.svc.markPendingAttempt(context.Background(), row, "claim", fmt.Errorf("raced pass"))

	assert.False(t, escalated, "the pass that drew MORE than the budget must not claim the escalation — the transition was the other pass's")
	assert.Equal(t, testutil.ToFloat64(metrics.ClosedFollowUpPendingTotal.WithLabelValues("escalated")), escalatedBefore,
		"one escalation outcome per budget, not one per raced pass")
	for _, n := range env.systemNotices() {
		assert.NotContains(t, n.Body, "нужен человек",
			"the losing side of the race posts nothing — a second «нужен человек» is the noise this guard removes")
	}
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts+1, row.Attempts,
		"the counter still records the raced pass's attempt; only the notice is refused")
}

// The escalation's OWN delivery is durable (codex-review P1, round 3): the
// attempt counter used to commit before the notice was written, so a failed
// notice write froze the row out of ListDue with the human never told — the
// one silent loss the mechanism still had. The row must stay listed until
// the notice LANDS; past the budget it is listed for the notice alone, and a
// healed database completes the escalation, never the delivery.
func TestClosedFollowUp_EscalationNoticeRetriedUntilItLands(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
		// The notice write fails: the budget's edge is reached with the
		// human still untold — the exact window the fix closes.
		e.commentRepo.createFailFor = func(cm *domain.Comment) bool {
			return cm.AuthorType == domain.ActorTypeSystem && strings.Contains(cm.Body, "нужен человек")
		}
	})
	c := env.comment(t, "находка: эскалация не пишется, но не теряется")

	for i := 1; i <= ClosedFollowUpReconcileMaxAttempts; i++ {
		_, _, _, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
	}
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	require.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts)
	require.Nil(t, row.EscalatedAt, "the notice never landed — the row must not read as escalated")

	// Pass 11: still listed, worked for the notice alone, still failing.
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried, "past the budget the row is retried for its NOTICE, not dropped")
	assert.Zero(t, escalated, "a notice that did not land is not an escalation")
	assert.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts, "the notice retry costs no delivery attempts")

	// Everything heals: the notice lands, the row completes and leaves the
	// due set forever. The finding's DELIVERY stays dead — the budget bought
	// the human, not a resurrection.
	env.rootsRepo.errToReturn = nil
	env.commentRepo.createFailFor = nil

	delivered, retried, escalated, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered, "no delivery past the budget — the notice is all that is left")
	assert.Zero(t, retried)
	assert.Equal(t, 1, escalated, "the notice landing IS the escalation completing")

	row = env.parkedRow(t, c.ID)
	require.NotNil(t, row, "the escalated row stays as the paper trail")
	require.NotNil(t, row.EscalatedAt)

	var notices int
	for _, n := range env.systemNotices() {
		if strings.Contains(n.Body, "нужен человек") {
			notices++
		}
	}
	assert.Equal(t, 1, notices, "exactly one «нужен человек» for one escalation")

	delivered, retried, escalated, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried, "a landed escalation leaves the due set forever")
	assert.Zero(t, escalated)
	assert.Empty(t, env.taskSvc.createdTasks(), "healing does not resurrect delivery past the budget")
}

// The park's OWN notice is durable too (codex-review P1, round 4): the
// «не подтверждена» write used to be best-effort with no retry path, so a
// transient comment-store failure at enqueue time queued the finding
// INVISIBLY — a row in the queue its own commenter could not see. Every
// reconcile pass that works the row must retry the notice until it lands
// (noticed_at), then never repeat it.
func TestClosedFollowUp_PendingNoticeRetriedUntilItLands(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
		// The park notice write fails AT ENQUEUE TIME: the row is queued but
		// its commenter is never told — the exact window the fix closes.
		e.commentRepo.createFailFor = func(cm *domain.Comment) bool {
			return cm.AuthorType == domain.ActorTypeSystem && strings.Contains(cm.Body, "не подтверждена")
		}
	})
	c := env.comment(t, "находка: уведомление о парковке не пишется")

	parkNotices := func() int {
		var n int
		for _, s := range env.systemNotices() {
			if strings.Contains(s.Body, "не подтверждена") {
				n++
			}
		}
		return n
	}

	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row, "the finding itself parked — only its notice failed")
	require.Nil(t, row.NoticedAt, "a notice that did not land must not read as landed")
	assert.Zero(t, parkNotices(), "nothing was told to the commenter")

	// Pass 1: the notice store still fails; the row is worked, delivery fails
	// again (identity still down), the notice retry fails too — visibly
	// counted, never swallowed.
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried)
	assert.Zero(t, escalated)
	row = env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	require.Nil(t, row.NoticedAt, "still failing — and still unnoticed, not silently forgotten")
	assert.Zero(t, parkNotices())

	// The comment store heals. The next pass lands the park notice and stamps
	// noticed_at; the delivery itself keeps failing (identity still down) —
	// the queue is visible now, which is what the contract demands.
	env.commentRepo.createFailFor = nil

	_, _, _, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	row = env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	require.NotNil(t, row.NoticedAt, "the retried notice landed — the park is visible to its commenter")
	assert.Equal(t, 1, parkNotices(), "exactly one park notice once it lands")

	// A later pass works the row again (delivery still failing) and must NOT
	// repeat the landed notice — noticed_at makes the retry once-ever.
	_, retried, _, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, retried, "delivery keeps retrying as before")
	assert.Equal(t, 1, parkNotices(), "a landed park notice is never repeated")
	assert.Empty(t, env.taskSvc.createdTasks(), "the identity outage still holds — no root")
}

// The two durability stamps must BOTH land before a row retires
// (codex-review P1, round 5): the escalation branch used to run before any
// park-notice check, so a landed escalation (escalated_at) took the row out
// of ListDue while noticed_at had never been set — the escalation stranded
// the very notice that says the finding is queued. The row is listed until
// both stamps are set, the escalation is never re-posted, and a healed park
// store completes the last notice.
func TestClosedFollowUp_ParkNoticeNotStrandedByEscalation(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
		// BOTH notice writes fail: the park notice at enqueue, and every
		// escalation attempt on top of it.
		e.commentRepo.createFailFor = func(cm *domain.Comment) bool {
			return cm.AuthorType == domain.ActorTypeSystem &&
				(strings.Contains(cm.Body, "не подтверждена") || strings.Contains(cm.Body, "нужен человек"))
		}
	})
	c := env.comment(t, "находка: ни один нотис не пишется")

	notices := func() (park, escalated int) {
		for _, s := range env.systemNotices() {
			switch {
			case strings.Contains(s.Body, "не подтверждена"):
				park++
			case strings.Contains(s.Body, "нужен человек"):
				escalated++
			}
		}
		return park, escalated
	}

	for i := 0; i < ClosedFollowUpReconcileMaxAttempts; i++ {
		_, _, _, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
		require.NoError(t, err)
	}
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	require.Equal(t, ClosedFollowUpReconcileMaxAttempts, row.Attempts)
	require.Nil(t, row.EscalatedAt, "the escalation never landed")
	require.Nil(t, row.NoticedAt, "the park notice never landed either")

	// The escalation store heals ALONE: the escalation lands, the park
	// notice still cannot. The row must not retire on the escalation —
	// that is the exact strand round 5 closes.
	env.commentRepo.createFailFor = func(cm *domain.Comment) bool {
		return cm.AuthorType == domain.ActorTypeSystem && strings.Contains(cm.Body, "не подтверждена")
	}
	delivered, retried, escalatedN, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried)
	assert.Equal(t, 1, escalatedN, "the escalation notice landed on its own schedule")
	row = env.parkedRow(t, c.ID)
	require.NotNil(t, row)
	require.NotNil(t, row.EscalatedAt)
	require.Nil(t, row.NoticedAt, "the park notice is still missing — the row is NOT done")

	// The park store heals too: the next pass completes the missing notice
	// without re-posting the landed escalation, and only then does the row
	// leave the due set.
	env.commentRepo.createFailFor = nil
	delivered, retried, escalatedN, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Equal(t, 1, retried, "the pass worked — for the park notice alone")
	assert.Zero(t, escalatedN, "a landed escalation is never re-posted")
	row = env.parkedRow(t, c.ID)
	require.NotNil(t, row, "the completed row stays as the paper trail")
	require.NotNil(t, row.NoticedAt, "the stranded park notice is completed after all")
	park, esc := notices()
	assert.Equal(t, 1, park, "exactly one park notice")
	assert.Equal(t, 1, esc, "exactly one escalation notice")

	delivered, retried, escalatedN, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried, "both stamps set — the row has left the due set forever")
	assert.Zero(t, escalatedN)
	assert.Empty(t, env.taskSvc.createdTasks(), "no delivery resurrection past the budget")
}

// Delivery must not discard an unlanded park notice (codex-review P1,
// round 6): the delivery's own outcome notice is best-effort inside
// claimAndCreateRoot, so a successful reconcile delivery used to delete the
// row while noticed_at had never been set — the promised source-card notice
// lost its only retry path. The row stays due until the notice lands; the
// redelivery on the completing pass is idempotent.
func TestClosedFollowUp_DeliveryDoesNotDiscardUnlandedParkNotice(t *testing.T) {
	collapseFollowUpRetrySleeps(t)
	env := setupFollowUpEnv(t, func(e *followUpEnv) {
		e.rootsRepo.errToReturn = fmt.Errorf("identity store unreachable")
		// The park notice fails at enqueue AND keeps failing: the finding
		// queues invisibly while the identity store is down.
		e.commentRepo.createFailFor = func(cm *domain.Comment) bool {
			return cm.AuthorType == domain.ActorTypeSystem && strings.Contains(cm.Body, "не подтверждена")
		}
	})
	c := env.comment(t, "находка: доставится быстрее, чем уведомление")

	// The identity store heals ALONE: reconcile delivers the finding, but
	// the park notice still cannot land — the delete must not run.
	env.rootsRepo.errToReturn = nil
	delivered, retried, escalated, err := env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered, "the delivery itself succeeded")
	assert.Zero(t, retried, "a delivered-and-held row counts as delivered, not retried")
	assert.Zero(t, escalated)
	row := env.parkedRow(t, c.ID)
	require.NotNil(t, row,
		"a delivered finding whose park notice never landed stays queued — the delete would discard the notice's only retry path")
	require.Nil(t, row.NoticedAt)

	// The notice store heals too: the next pass lands the park notice,
	// redelivers idempotently (one root), and only then deletes the row.
	env.commentRepo.createFailFor = nil
	delivered, retried, escalated, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered, "the redelivery is idempotent — still exactly one root")
	assert.Zero(t, retried)
	assert.Zero(t, escalated)
	assert.Nil(t, env.parkedRow(t, c.ID), "notice landed AND delivered — the row retires")

	assert.Len(t, env.taskSvc.createdTasks(), 1, "one finding, one root, across both passes")
	var park int
	for _, s := range env.systemNotices() {
		if strings.Contains(s.Body, "не подтверждена") {
			park++
		}
	}
	assert.Equal(t, 1, park, "exactly one park notice — landed on the first pass that could write it")

	delivered, retried, escalated, err = env.svc.ReconcileClosedFollowUpPending(context.Background())
	require.NoError(t, err)
	assert.Zero(t, delivered)
	assert.Zero(t, retried, "the row is gone — nothing left to work")
	assert.Zero(t, escalated)
}
