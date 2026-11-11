package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// declareThroughCommand ends the agent's turn with a card wait, so the card's
// runtime_state is the one the wait wrote.
func (f *typedOutcomeFixture) declareThroughCommand(t *testing.T, agent uuid.UUID) *domain.TaskWait {
	t.Helper()
	f.holdCheckout(t, f.task, agent)
	target := f.other
	res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), &domain.TurnCommand{TaskID: f.task, AgentID: agent,
		Kind: domain.TurnOutcomeWait, CommentBody: "Waiting.", RuntimeReason: "card",
		Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target}})
	require.NoError(t, err)
	return res.Wait
}

func (f *typedOutcomeFixture) runtimeRef(t *testing.T) *string {
	t.Helper()
	var ref *string
	require.NoError(t, f.db.Get(&ref, `SELECT runtime_state->>'ref' FROM tasks WHERE id = $1`, f.task))
	return ref
}

func TestTaskWaitRepo_ResolveWithCause_DB(t *testing.T) {
	ctx := context.Background()
	f := newTypedOutcomeFixture(t)
	w := f.declareThroughCommand(t, f.agent)
	require.NotNil(t, f.runtimeRef(t))

	got, ws, err := f.waits.GetForTask(ctx, f.task, w.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, w.ID, got.ID)
	assert.NotEqual(t, uuid.Nil, ws)
	missing, _, err := f.waits.GetForTask(ctx, f.other, w.ID)
	require.NoError(t, err)
	assert.Nil(t, missing, "a wait is found only on its own card")

	res, resolved, err := f.waits.ResolveWithCause(ctx, f.task, w.ID, domain.WaitResolutionMet, "card closed upstream", f.agent)
	require.NoError(t, err)
	assert.True(t, resolved)
	assert.Equal(t, domain.WaitResolutionMet, *res.Resolution)
	assert.Equal(t, "card closed upstream", *res.ResolveCause)
	assert.Equal(t, f.agent, *res.ResolvedBy)
	require.NotNil(t, res.ResolvedAt)
	assert.Nil(t, f.runtimeRef(t), "the waiting state written from the wait is cleared")

	again, resolved, err := f.waits.ResolveWithCause(ctx, f.task, w.ID, domain.WaitResolutionCancelled, "late", f.agent2)
	require.NoError(t, err)
	assert.False(t, resolved)
	assert.Equal(t, domain.WaitResolutionMet, *again.Resolution, "an ended wait is not rewritten")
	active, err := f.waits.GetActive(ctx, f.task, &f.agent)
	require.NoError(t, err)
	assert.Nil(t, active)

	_, _, err = f.waits.ResolveWithCause(ctx, f.task, uuid.New(), domain.WaitResolutionMet, "x", f.agent)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 404, ae.Code)
}

// A runtime_state some other writer put on the card is not the wait's to clear.
func TestTaskWaitRepo_ResolveKeepsForeignRuntimeState_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	w := f.declareThroughCommand(t, f.agent)
	_, err := f.db.Exec(`UPDATE tasks SET runtime_state = jsonb_set(runtime_state, '{ref}', '"runner:lane"') WHERE id = $1`, f.task)
	require.NoError(t, err)
	_, _, err = f.waits.ResolveWithCause(context.Background(), f.task, w.ID, domain.WaitResolutionMet, "x", f.agent)
	require.NoError(t, err)
	ref := f.runtimeRef(t)
	require.NotNil(t, ref)
	assert.Equal(t, "runner:lane", *ref)
}

// Stale waits do not pile up: continue_next ends the caller's own wait, and
// a card closed by any path ends every active wait on it.
func TestTaskWaits_StaleWaitsEnd_DB(t *testing.T) {
	ctx := context.Background()

	t.Run("continue_next supersedes the caller's wait", func(t *testing.T) {
		f := newTypedOutcomeFixture(t)
		w := f.declareThroughCommand(t, f.agent)
		other := f.declareThroughCommand(t, f.agent2)
		f.holdCheckout(t, f.task, f.agent)
		_, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
			Kind: domain.TurnOutcomeNext, CommentBody: "Continuing."})
		require.NoError(t, err)
		got, _, err := f.waits.GetForTask(ctx, f.task, w.ID)
		require.NoError(t, err)
		require.NotNil(t, got.Resolution)
		assert.Equal(t, domain.WaitResolutionSuperseded, *got.Resolution)
		assert.Equal(t, "continue_next", *got.ResolveCause)
		still, err := f.waits.GetActive(ctx, f.task, &f.agent2)
		require.NoError(t, err)
		require.NotNil(t, still, "another agent's wait is not the caller's to end")
		assert.Equal(t, other.ID, still.ID)
	})

	for _, category := range []domain.StatusCategory{domain.StatusCategoryDone, domain.StatusCategoryCancelled} {
		t.Run("closing the card ends its waits: "+string(category), func(t *testing.T) {
			f := newTypedOutcomeFixture(t)
			f.declareThroughCommand(t, f.agent)
			f.declareThroughCommand(t, f.agent2)
			var project uuid.UUID
			require.NoError(t, f.db.Get(&project, `SELECT project_id FROM tasks WHERE id = $1`, f.task))
			closed := &domain.TaskStatus{ID: uuid.New(), ProjectID: project, Name: string(category), Slug: string(category),
				Color: "#000000", Category: category, Position: 9}
			require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, closed))
			_, err := f.db.Exec(`UPDATE tasks SET status_id = $2 WHERE id = $1`, f.task, closed.ID)
			require.NoError(t, err)
			var open, cancelled int
			require.NoError(t, f.db.Get(&open, `SELECT count(*) FROM task_waits WHERE task_id = $1 AND resolved_at IS NULL`, f.task))
			require.NoError(t, f.db.Get(&cancelled, `SELECT count(*) FROM task_waits WHERE task_id = $1
				AND resolution = 'cancelled' AND resolve_cause = 'card closed'`, f.task))
			assert.Zero(t, open)
			assert.Equal(t, 2, cancelled)
		})
	}

	t.Run("a move between open statuses keeps the waits", func(t *testing.T) {
		f := newTypedOutcomeFixture(t)
		f.declareThroughCommand(t, f.agent)
		var project uuid.UUID
		require.NoError(t, f.db.Get(&project, `SELECT project_id FROM tasks WHERE id = $1`, f.task))
		doing := &domain.TaskStatus{ID: uuid.New(), ProjectID: project, Name: "Doing", Slug: "doing",
			Color: "#000000", Category: domain.StatusCategoryInProgress, Position: 2}
		require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, doing))
		_, err := f.db.Exec(`UPDATE tasks SET status_id = $2 WHERE id = $1`, f.task, doing.ID)
		require.NoError(t, err)
		active, err := f.waits.GetActive(ctx, f.task, &f.agent)
		require.NoError(t, err)
		assert.NotNil(t, active)
	})

	t.Run("deleting the card ends its waits", func(t *testing.T) {
		f := newTypedOutcomeFixture(t)
		f.declareThroughCommand(t, f.agent)
		_, err := f.db.Exec(`UPDATE tasks SET deleted_at = now() WHERE id = $1`, f.task)
		require.NoError(t, err)
		var open int
		require.NoError(t, f.db.Get(&open, `SELECT count(*) FROM task_waits WHERE task_id = $1 AND resolved_at IS NULL`, f.task))
		assert.Zero(t, open)
	})
}

// The controller is the agent holding a controller's reporter grant in this
// workspace, presented with a key for this workspace; nobody else is.
func TestRuntimeRepo_IsController_DB(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	is := func(ws uuid.UUID, actor RuntimeActor) bool {
		ok, err := f.repo.IsController(ctx, ws, actor)
		require.NoError(t, err)
		return ok
	}
	assert.False(t, is(f.owner, f.reporter), "no catalog saved yet: no controller")
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	assert.True(t, is(f.owner, f.reporter))
	assert.False(t, is(f.owner, f.worker), "an agent without the reporter grant")
	assert.False(t, is(f.receiver, f.reporter), "the reporter of another workspace")
	connector := f.reporter
	connector.Connector = true
	assert.False(t, is(f.owner, connector), "an OAuth connector")
	otherKey := f.reporter
	otherKey.AuthWorkspaceID = f.receiver
	assert.False(t, is(f.owner, otherKey), "a key presented for another workspace")
	assert.False(t, is(f.owner, f.ownerActor), "a person")
	assert.False(t, is(uuid.New(), RuntimeActor{AgentID: f.reporter.AgentID}), "a workspace without a runtime connection")
	assert.False(t, is(f.receiver, f.worker), "a workspace whose catalog was never saved")
	sameWorkspaceKey := RuntimeActor{AgentID: f.worker.AgentID, AuthWorkspaceID: f.owner}
	assert.False(t, is(f.owner, sameWorkspaceKey), "a key for this workspace without the reporter grant")

	// A controller without a reporter grant, or an unreadable catalog, names
	// no controller.
	_, err = f.db.Exec(`UPDATE integration_configs SET config = jsonb_set(config, '{controllers,runner-a,reporter_grant_id}', '"00000000-0000-0000-0000-000000000000"')
		WHERE workspace_id = $1 AND provider IN ('runner','agent_runtime')`, f.owner)
	require.NoError(t, err)
	assert.False(t, is(f.owner, f.reporter), "a controller without a reporter grant (the catalog parser refuses it)")
	_, err = f.db.Exec(`UPDATE integration_configs SET config = '[]'::jsonb WHERE workspace_id = $1 AND provider IN ('runner','agent_runtime')`, f.owner)
	require.NoError(t, err)
	assert.False(t, is(f.owner, f.reporter), "an unreadable catalog")
}
