package service

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// A project created before triage existed gets the standard stage on demand,
// exactly once even under concurrent callers, and a gated task moves into it
// without losing its gate or assignee.
func TestEnsureTriageStageAddsOnceAndKeepsGateAndAssignee(t *testing.T) {
	f, repo, svc, id, owner := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	statuses := postgres.NewTaskStatusRepo(f.db)
	count := func() (n int) {
		require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM task_statuses WHERE project_id=$1 AND category='triage'`, f.projectID))
		return n
	}
	require.Zero(t, count(), "fixture project starts without triage")

	ids := make([]uuid.UUID, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			ids[i], err = ensureTriageStatusID(ctx, statuses, f.projectID)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, 1, count(), "concurrent callers add one triage stage")
	for _, got := range ids {
		require.Equal(t, ids[0], got)
	}
	var first, todo int
	require.NoError(t, f.db.Get(&first, `SELECT position FROM task_statuses WHERE id=$1`, ids[0]))
	require.NoError(t, f.db.Get(&todo, `SELECT position FROM task_statuses WHERE id=$1`, f.statusID))
	require.Less(t, first, todo, "triage is placed first on the board")

	_, err := f.db.Exec(`UPDATE tasks SET human_gate=true,assignee_id=$2,assignee_type='agent' WHERE id=$1`, id, owner)
	require.NoError(t, err)
	require.NoError(t, svc.MoveTask(ctx, id, MoveTaskInput{StatusID: &ids[0]}))
	task, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, ids[0], task.StatusID)
	require.True(t, task.HumanGate, "moving to triage keeps the gate")
	require.NotNil(t, task.AssigneeID)
	require.Equal(t, owner, *task.AssigneeID, "moving to triage keeps the assignee")
	again, err := ensureTriageStatusID(ctx, statuses, f.projectID)
	require.NoError(t, err)
	require.Equal(t, ids[0], again)
}
