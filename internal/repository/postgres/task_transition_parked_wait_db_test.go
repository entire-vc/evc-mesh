package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestAutomaticTransitionLeavesRegisteredWaitToConsumer(t *testing.T) {
	f := newParkedFixture(t)
	f.register(t)
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	before := *task
	task.StatusID = f.todo
	task.UpdatedAt = time.Now()
	alarm := time.Now().Add(time.Hour)
	err = f.repo.UpdateTransition(f.ctx, task, domain.TaskTransition{
		ExpectedVersion: before.Version, DisallowParkedWait: true,
		AlarmDue: &alarm, AlarmLabels: []string{"must-not-write"},
	})
	var conflict *domain.TaskConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, before.Version, conflict.CurrentVersion)
	require.Equal(t, f.backlog, conflict.CurrentStatusID)
	fresh, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, before, *fresh, "a rejected automatic wake has no task/lease/alarm side effects")
	f.assertHeld(t)
	result, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	require.NoError(t, err)
	require.True(t, result.Released, "unchanged registration remains usable by the real consumer")
}

func TestAutomaticTransitionPermitsReleasedWaitAndManualMove(t *testing.T) {
	for _, kind := range []string{"released", "manual", "unregistered"} {
		t.Run(kind, func(t *testing.T) {
			f := newParkedFixture(t)
			if kind != "unregistered" {
				f.register(t)
			}
			if kind == "released" {
				_, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
				require.NoError(t, err)
			}
			task, err := f.repo.GetByID(f.ctx, f.id)
			require.NoError(t, err)
			task.StatusID = f.todo
			task.Position++
			require.NoError(t, f.repo.UpdateTransition(f.ctx, task, domain.TaskTransition{
				ExpectedVersion: task.Version, DisallowParkedWait: kind != "manual",
			}))
		})
	}
}

func TestAutomaticTransitionStaleVersionNeedsNoSecondConnection(t *testing.T) {
	f := newParkedFixture(t)
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	before := *task
	f.repo.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	task.StatusID = f.todo
	err = f.repo.UpdateTransition(ctx, task, domain.TaskTransition{
		ExpectedVersion: task.Version - 1, DisallowParkedWait: true,
	})
	var conflict *domain.TaskConflict
	require.ErrorAs(t, err, &conflict, "a stale automatic move must return conflict without waiting for another pool connection")
	require.Equal(t, before.Version, conflict.CurrentVersion)
	require.Equal(t, before.StatusID, conflict.CurrentStatusID)
	fresh, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, before, *fresh)
}

// Registration does not bump task.version. It may commit after an automatic
// mover starts waiting for the row lock: that mover must read the registration
// with a new statement snapshot after acquiring the lock.
func TestAutomaticTransitionSeesRegistrationCommittedWhileWaiting(t *testing.T) {
	f := newParkedFixture(t)
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	tx, err := f.repo.db.BeginTxx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = lockParkedTask(f.ctx, tx, f.id)
	require.NoError(t, err)
	plan, err := json.Marshal(f.plan)
	require.NoError(t, err)
	_, err = tx.ExecContext(f.ctx, `INSERT INTO parked_waits(id,task_id,wait_comment_id,plan,registered_by,registered_by_type,result) VALUES($1,$2,$3,$4,$5,'agent','{}')`, f.plan.ID, f.id, f.plan.WaitCommentID, plan, f.owner)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	task.StatusID = f.todo
	go func() {
		result <- f.repo.UpdateTransition(ctx, task, domain.TaskTransition{ExpectedVersion: task.Version, DisallowParkedWait: true})
	}()
	// Observe the actual PostgreSQL lock wait, not a timing-based assumption.
	require.Eventually(t, func() bool {
		var waiting bool
		err := f.repo.db.GetContext(ctx, &waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM tasks WHERE id=$1%' AND pid<>pg_backend_pid())`)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit())
	var conflict *domain.TaskConflict
	require.ErrorAs(t, <-result, &conflict)
	f.assertHeld(t)
}
