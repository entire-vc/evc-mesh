package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestTaskCAS_ConcurrentSoftDelete(t *testing.T) {
	for _, path := range []string{"transition", "full_update"} {
		t.Run(path, func(t *testing.T) {
			r, id, _ := checkoutGenerationFixture(t)
			ctx := context.Background()
			snapshot, err := r.GetByID(ctx, id)
			require.NoError(t, err)
			write := func() error {
				if path == "transition" {
					return r.UpdateTransition(ctx, snapshot, domain.TaskTransition{ExpectedVersion: snapshot.Version})
				}
				return r.Update(ctx, snapshot)
			}
			require.NoError(t, write(), "positive control: existing task can be written")
			snapshot, err = r.GetByID(ctx, id)
			require.NoError(t, err)
			_, err = r.db.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, id)
			require.NoError(t, err)
			require.NotPanics(t, func() { err = write() })
			var missing *apierror.Error
			require.ErrorAs(t, err, &missing)
			require.Equal(t, 404, missing.StatusCode())
		})
	}
}

// A title edit committed after a move's snapshot must never be erased by that move.
func TestTaskCAS_StaleSnapshotPreservesTitle(t *testing.T) {
	repo, id, _ := checkoutGenerationFixture(t)
	ctx := context.Background()
	move, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	edit, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	edit.Title = "concurrent title"
	edit.UpdatedAt = time.Now()
	require.NoError(t, repo.Update(ctx, edit))
	move.Position = 42
	move.UpdatedAt = time.Now()
	require.Error(t, repo.Update(ctx, move), "stale write must conflict")
	fresh, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "concurrent title", fresh.Title)
	require.NotEqual(t, float64(42), fresh.Position)
}

func reaperFixture(t *testing.T, expired bool) (*TaskRepo, *domain.Task, uuid.UUID) {
	t.Helper()
	r, id, owner := checkoutGenerationFixture(t)
	ctx := context.Background()
	task, err := r.GetByID(ctx, id)
	require.NoError(t, err)
	todo := task.StatusID
	ip := uuid.New()
	_, err = r.db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,'Active','active','in_progress')`, ip, task.ProjectID)
	require.NoError(t, err)
	_, err = r.db.Exec(`UPDATE tasks SET status_id=$2,updated_at=now()-interval '3 hours' WHERE id=$1`, id, ip)
	require.NoError(t, err)
	if expired {
		_, err = r.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(-time.Minute), generationScope())
		require.NoError(t, err)
	}
	task, err = r.GetByID(ctx, id)
	require.NoError(t, err)
	return r, task, todo
}

func candidateTransition(task *domain.Task, todo uuid.UUID, mode string) (*domain.Task, domain.TaskTransition) {
	write := *task
	write.StatusID, write.UpdatedAt = todo, time.Now()
	input := domain.TaskTransition{ExpectedVersion: task.Version, ExpectedStatusID: &task.StatusID,
		Reaper: &domain.ReaperExpectation{Mode: mode, Generation: task.CheckoutGeneration, Token: task.CheckoutToken, ExpiresAt: task.CheckoutExpires, QuietGrace: 2 * time.Hour}}
	return &write, input
}

func TestTaskCAS_ParallelTransitions(t *testing.T) {
	r, snapshot, todo := reaperFixture(t, false)
	start, results := make(chan struct{}), make(chan error, 2)
	for i := range 2 {
		go func(position float64) {
			write := *snapshot
			write.StatusID, write.Position, write.UpdatedAt = todo, position, time.Now()
			<-start
			results <- r.UpdateTransition(context.Background(), &write, domain.TaskTransition{ExpectedVersion: snapshot.Version})
		}(float64(i + 10))
	}
	close(start)
	wins := 0
	for range 2 {
		if err := <-results; err == nil {
			wins++
		} else {
			var conflict *domain.TaskConflict
			require.ErrorAs(t, err, &conflict)
			require.Equal(t, snapshot.Version+1, conflict.CurrentVersion)
			require.Equal(t, todo, conflict.CurrentStatusID)
		}
	}
	require.Equal(t, 1, wins)
	fresh, err := r.GetByID(context.Background(), snapshot.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot.Version+1, fresh.Version)
}

func TestTaskCAS_ExpiredCandidateRenewed(t *testing.T) {
	r, snapshot, todo := reaperFixture(t, true)
	ctx := context.Background()
	candidates, err := r.FindExpiredInProgressCheckouts(ctx)
	require.NoError(t, err)
	require.Contains(t, taskIDs(candidates), snapshot.ID)
	// Renewal commits between the candidate SELECT and the guarded write. The
	// scoped API refuses extending an already expired generation; reacquisition
	// is its supported renewal path and must also defeat the stale sweep.
	_, err = r.ExtendScopedCheckout(ctx, snapshot.ID, domain.CheckoutExpectation{Token: snapshot.CheckoutToken}, time.Now().Add(time.Hour))
	require.ErrorIs(t, err, ErrInvalidCheckoutToken)
	lease, err := r.AcquireCheckout(ctx, snapshot.ID, *snapshot.CheckedOutBy, uuid.New(), time.Now().Add(time.Hour), generationScope())
	require.NoError(t, err)
	write, input := candidateTransition(snapshot, todo, "expired")
	var conflict *domain.TaskConflict
	require.ErrorAs(t, r.UpdateTransition(ctx, write, input), &conflict)
	fresh, err := r.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot.StatusID, fresh.StatusID)
	require.Equal(t, lease.Token, fresh.CheckoutToken)
	require.Equal(t, lease.ExpiresAt, fresh.CheckoutExpires)
	// The general cleanup phase must preserve a renewed in-progress lease too.
	_, err = r.ReleaseExpiredCheckouts(ctx)
	require.NoError(t, err)
}

func TestTaskCAS_ExpiredCandidateLegacyHeartbeat(t *testing.T) {
	r, snapshot, todo := reaperFixture(t, true)
	ctx := context.Background()
	// Model a pre-generation lease. The legacy bulk heartbeat still extends
	// these leases, unlike the scoped API which rejects an expired generation.
	_, err := r.db.Exec(`UPDATE tasks SET checkout_session_id=NULL,checkout_request_id=NULL,checkout_generation=0 WHERE id=$1`, snapshot.ID)
	require.NoError(t, err)
	_, err = r.db.Exec(`INSERT INTO project_rules(id,project_id,rule_type,config,enforcement_mode) VALUES($1,$2,'workflow','{"mid_pipeline":{"heartbeat_extends_checkout":true}}','advisory')`, uuid.New(), snapshot.ProjectID)
	require.NoError(t, err)
	snapshot, err = r.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	candidates, err := r.FindExpiredInProgressCheckouts(ctx)
	require.NoError(t, err)
	require.Contains(t, taskIDs(candidates), snapshot.ID)

	n, err := r.ExtendCheckoutsOnHeartbeat(ctx, *snapshot.CheckedOutBy)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "positive control: legacy heartbeat renews the selected lease")
	renewed, err := r.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	require.True(t, renewed.CheckoutExpires.After(time.Now()))
	require.Greater(t, renewed.Version, snapshot.Version)

	write, input := candidateTransition(snapshot, todo, "expired")
	var conflict *domain.TaskConflict
	require.ErrorAs(t, r.UpdateTransition(ctx, write, input), &conflict)
	_, err = r.ReleaseExpiredCheckouts(ctx)
	require.NoError(t, err)
	fresh, err := r.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot.StatusID, fresh.StatusID, "heartbeat must defeat the stale sweep")
	require.Equal(t, renewed.Version, fresh.Version)
	require.Equal(t, renewed.CheckedOutBy, fresh.CheckedOutBy)
	require.Equal(t, renewed.CheckoutToken, fresh.CheckoutToken)
	require.Equal(t, renewed.CheckoutExpires, fresh.CheckoutExpires)
}

func TestTaskCAS_UnleasedCandidateInvalidated(t *testing.T) {
	for _, activity := range []string{"checkout", "comment", "artifact", "human_gate", "shipped", "quiet_refresh"} {
		t.Run(activity, func(t *testing.T) {
			r, snapshot, todo := reaperFixture(t, false)
			ctx := context.Background()
			candidates, err := r.FindStaleUnleasedInProgress(ctx, 2*time.Hour)
			require.NoError(t, err)
			require.Contains(t, taskIDs(candidates), snapshot.ID)
			switch activity {
			case "checkout":
				var holder uuid.UUID
				require.NoError(t, r.db.Get(&holder, `SELECT a.id FROM agents a JOIN projects p ON p.workspace_id=a.workspace_id WHERE p.id=$1 LIMIT 1`, snapshot.ProjectID))
				_, err = r.AcquireCheckout(ctx, snapshot.ID, holder, uuid.New(), time.Now().Add(time.Hour), generationScope())
			case "comment":
				_, err = r.db.Exec(`INSERT INTO comments(id,task_id,author_id,author_type,body,created_at) VALUES($1,$2,$3,'system','activity',now()-interval '4 hours')`, uuid.New(), snapshot.ID, uuid.Nil)
			case "artifact":
				_, err = r.db.Exec(`INSERT INTO artifacts(id,task_id,name,storage_key,uploaded_by,uploaded_by_type,created_at) VALUES($1,$2,'activity','fixture',$3,'agent',now()-interval '4 hours')`, uuid.New(), snapshot.ID, uuid.New())
			case "human_gate":
				_, err = r.db.Exec(`UPDATE tasks SET human_gate=true WHERE id=$1`, snapshot.ID)
			case "shipped":
				_, err = r.db.Exec(`UPDATE tasks SET is_shipped=true WHERE id=$1`, snapshot.ID)
			case "quiet_refresh":
				_, err = r.db.Exec(`UPDATE tasks SET updated_at=now() WHERE id=$1`, snapshot.ID)
			}
			require.NoError(t, err)
			write, input := candidateTransition(snapshot, todo, "unleased")
			var conflict *domain.TaskConflict
			require.ErrorAs(t, r.UpdateTransition(ctx, write, input), &conflict)
			fresh, err := r.GetByID(ctx, snapshot.ID)
			require.NoError(t, err)
			require.Equal(t, snapshot.StatusID, fresh.StatusID)
			require.Greater(t, fresh.Version, snapshot.Version)
		})
	}
}

func TestTaskCAS_DoubleSweepAndCleanup(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "unleased", true: "expired"}[expired], func(t *testing.T) {
			r, snapshot, todo := reaperFixture(t, expired)
			ctx := context.Background()
			_, err := r.ReleaseExpiredCheckouts(ctx)
			require.NoError(t, err)
			fresh, err := r.GetByID(ctx, snapshot.ID)
			require.NoError(t, err)
			require.Equal(t, snapshot.CheckoutToken, fresh.CheckoutToken, "cleanup cannot discard an in-progress candidate's lease")
			mode := map[bool]string{false: "unleased", true: "expired"}[expired]
			write, input := candidateTransition(snapshot, todo, mode)
			require.NoError(t, r.UpdateTransition(ctx, write, input), "positive stale-task return control")
			duplicate, _ := candidateTransition(snapshot, todo, mode)
			var conflict *domain.TaskConflict
			require.ErrorAs(t, r.UpdateTransition(ctx, duplicate, input), &conflict)
			fresh, err = r.GetByID(ctx, snapshot.ID)
			require.NoError(t, err)
			require.Equal(t, snapshot.Version+1, fresh.Version)
			require.Equal(t, todo, fresh.StatusID)
			require.Nil(t, fresh.CheckoutToken)
			require.Nil(t, fresh.CheckedOutBy)
		})
	}
}

func taskIDs(tasks []domain.Task) []uuid.UUID {
	ids := make([]uuid.UUID, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	return ids
}

func TestTaskCAS_AlarmAndStatusRollback(t *testing.T) {
	r, snapshot, todo := reaperFixture(t, false)
	ctx := context.Background()
	write, input := candidateTransition(snapshot, uuid.New(), "unleased")
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	input.AlarmDue, input.AlarmLabels = &due, []string{"kind:monitor"}
	require.Error(t, r.UpdateTransition(ctx, write, input), "invalid status FK rejects the entire mutation")
	fresh, err := r.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot.StatusID, fresh.StatusID)
	require.Equal(t, snapshot.Version, fresh.Version)
	require.Equal(t, snapshot.DueDate, fresh.DueDate)
	require.Equal(t, snapshot.Labels, fresh.Labels)
	write.StatusID = todo
	require.NoError(t, r.UpdateTransition(ctx, write, input), "valid status is the positive control")
	fresh, err = r.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	require.Equal(t, todo, fresh.StatusID)
	require.Equal(t, &due, fresh.DueDate)
	require.Equal(t, input.AlarmLabels, []string(fresh.Labels))
	require.Equal(t, snapshot.Version+1, fresh.Version)
}
