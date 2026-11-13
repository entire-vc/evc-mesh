package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A typed card wait owns the blocks edge it declared, the
// receipt names that edge (id, generation, owned), and the runner's
// owner-checked delete removes only an edge its own wait owns.

// edgeOwnerWait declares wait_for on=card f.task -> f.other as agent and
// returns the receipt.
func (f *typedOutcomeFixture) edgeOwnerWait(t *testing.T, agent uuid.UUID) *domain.TaskWait {
	t.Helper()
	f.holdCheckout(t, f.task, agent)
	target := f.other
	res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), &domain.TurnCommand{
		TaskID: f.task, AgentID: agent, Kind: domain.TurnOutcomeWait, CommentBody: "Waiting on a card.",
		Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target}})
	require.NoError(t, err)
	require.NotNil(t, res.Wait)
	return res.Wait
}

type edgeOwnerRow struct {
	ID          uuid.UUID  `db:"id"`
	Generation  int64      `db:"generation"`
	OwnerWaitID *uuid.UUID `db:"owner_wait_id"`
}

func (f *typedOutcomeFixture) edgeRow(t *testing.T) (edgeOwnerRow, bool) {
	t.Helper()
	var rows []edgeOwnerRow
	require.NoError(t, f.db.Select(&rows, `SELECT id, generation, owner_wait_id FROM task_dependencies
		WHERE task_id = $1 AND depends_on_task_id = $2`, f.task, f.other))
	if len(rows) == 0 {
		return edgeOwnerRow{}, false
	}
	return rows[0], true
}

// storedReceipt reads the wait back: the receipt fields are written with it.
func (f *typedOutcomeFixture) storedReceipt(t *testing.T, waitID uuid.UUID) domain.TaskWait {
	t.Helper()
	var w domain.TaskWait
	require.NoError(t, f.db.Get(&w, `SELECT `+taskWaitCols+` FROM task_waits WHERE id = $1`, waitID))
	return w
}

// AC1: no edge before the wait -> the wait creates and owns it; the receipt's
// id/generation are the row's; the owner-checked delete with them removes it.
func TestTypedWaitEdgeOwner_NewEdgeOwnedAndOwnerDeletes_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	_, exists := f.edgeRow(t)
	require.False(t, exists, "fixture starts without an edge")

	w := f.edgeOwnerWait(t, f.agent)
	require.NotNil(t, w.DependencyOwned, "receipt carries dependency_owned")
	assert.True(t, *w.DependencyOwned)
	row, exists := f.edgeRow(t)
	require.True(t, exists)
	require.NotNil(t, w.DependencyID)
	require.NotNil(t, w.DependencyGeneration)
	assert.Equal(t, row.ID, *w.DependencyID)
	assert.Equal(t, row.Generation, *w.DependencyGeneration)
	require.NotNil(t, row.OwnerWaitID)
	assert.Equal(t, w.ID, *row.OwnerWaitID)

	stored := f.storedReceipt(t, w.ID)
	require.NotNil(t, stored.DependencyID)
	assert.Equal(t, row.ID, *stored.DependencyID)
	assert.Equal(t, row.Generation, *stored.DependencyGeneration)
	assert.True(t, *stored.DependencyOwned)

	// The dependency read names the owner and its agent.
	deps, err := NewTaskDependencyRepo(f.db).ListByTask(ctx, f.task)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	require.NotNil(t, deps[0].OwnerWaitID)
	assert.Equal(t, w.ID, *deps[0].OwnerWaitID)
	require.NotNil(t, deps[0].OwnerAgentID)
	assert.Equal(t, f.agent, *deps[0].OwnerAgentID)

	// Another wait id at the right generation does not match.
	err = NewTaskDependencyRepo(f.db).DeleteIfOwned(ctx, f.task, row.ID, row.Generation, uuid.New())
	require.ErrorIs(t, err, domain.ErrStaleDependencyGeneration)
	_, exists = f.edgeRow(t)
	require.True(t, exists, "a foreign owner id deleted the edge")

	require.NoError(t, NewTaskDependencyRepo(f.db).DeleteIfOwned(ctx, f.task, row.ID, *w.DependencyGeneration, *row.OwnerWaitID))
	_, exists = f.edgeRow(t)
	assert.False(t, exists, "the owner-checked delete left the edge")
}

// AC2: an edge made by hand before the wait stays ownerless; the receipt says
// owned=false and no owner-checked delete removes it.
func TestTypedWaitEdgeOwner_ManualEdgeNotOwned_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	deps := NewTaskDependencyRepo(f.db)
	manual := &domain.TaskDependency{ID: uuid.New(), TaskID: f.task, DependsOnTaskID: f.other,
		DependencyType: domain.DependencyTypeBlocks}
	require.NoError(t, deps.Create(ctx, manual))

	w := f.edgeOwnerWait(t, f.agent)
	require.NotNil(t, w.DependencyOwned)
	assert.False(t, *w.DependencyOwned)
	assert.Equal(t, manual.ID, *w.DependencyID)
	row, exists := f.edgeRow(t)
	require.True(t, exists)
	assert.Nil(t, row.OwnerWaitID, "a typed wait took over a person's edge")
	assert.False(t, *f.storedReceipt(t, w.ID).DependencyOwned)

	for _, owner := range []uuid.UUID{w.ID, uuid.New()} {
		err := deps.DeleteIfOwned(ctx, f.task, row.ID, row.Generation, owner)
		require.ErrorIs(t, err, domain.ErrStaleDependencyGeneration)
	}
	_, exists = f.edgeRow(t)
	assert.True(t, exists, "an owner-checked delete removed an ownerless edge")

	// Positive control: the plain generation delete still removes it.
	require.NoError(t, deps.DeleteIfGeneration(ctx, f.task, row.ID, row.Generation))
	_, exists = f.edgeRow(t)
	assert.False(t, exists)
}

// AC3: an operator's edit of an owned edge takes it away from the wait.
func TestTypedWaitEdgeOwner_OperatorEditClearsOwner_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	deps := NewTaskDependencyRepo(f.db)
	w := f.edgeOwnerWait(t, f.agent)
	row, _ := f.edgeRow(t)
	require.NotNil(t, row.OwnerWaitID)

	// Control: an unchanged re-send is no edit and keeps the owner.
	gen := row.Generation
	_, err := deps.UpdateCondition(ctx, row.ID, domain.DependencyConditionUpdate{
		Condition: domain.DependencyConditionTaskDone, ExpectedGeneration: &gen})
	require.NoError(t, err)
	row, _ = f.edgeRow(t)
	require.NotNil(t, row.OwnerWaitID, "an unchanged re-send dropped the owner")

	updated, err := deps.UpdateCondition(ctx, row.ID, domain.DependencyConditionUpdate{
		Condition: domain.DependencyConditionShipped, ExpectedGeneration: &gen})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Nil(t, updated.OwnerWaitID)
	row, _ = f.edgeRow(t)
	assert.Nil(t, row.OwnerWaitID, "a condition change kept the wait as owner")

	err = deps.DeleteIfOwned(ctx, f.task, row.ID, row.Generation, w.ID)
	require.ErrorIs(t, err, domain.ErrStaleDependencyGeneration)
	_, exists := f.edgeRow(t)
	assert.True(t, exists)
}

// AC4: the same agent re-declaring from the same card takes the edge over to
// its new wait; another agent's wait does not.
func TestTypedWaitEdgeOwner_RedeclareTransfersOnlyWithinAgent_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	deps := NewTaskDependencyRepo(f.db)

	w1 := f.edgeOwnerWait(t, f.agent)
	w2 := f.edgeOwnerWait(t, f.agent)
	require.NotEqual(t, w1.ID, w2.ID)
	assert.True(t, *w2.DependencyOwned)
	assert.Equal(t, *w1.DependencyID, *w2.DependencyID, "the re-declare reuses the edge")
	row, _ := f.edgeRow(t)
	require.NotNil(t, row.OwnerWaitID)
	assert.Equal(t, w2.ID, *row.OwnerWaitID, "ownership did not move to the new wait")
	require.ErrorIs(t, deps.DeleteIfOwned(ctx, f.task, row.ID, row.Generation, w1.ID), domain.ErrStaleDependencyGeneration,
		"the superseded wait still deletes the edge")

	w3 := f.edgeOwnerWait(t, f.agent2)
	assert.False(t, *w3.DependencyOwned)
	row, _ = f.edgeRow(t)
	require.NotNil(t, row.OwnerWaitID)
	assert.Equal(t, w2.ID, *row.OwnerWaitID, "another agent's wait took the edge")
	require.ErrorIs(t, deps.DeleteIfOwned(ctx, f.task, row.ID, row.Generation, w3.ID), domain.ErrStaleDependencyGeneration)

	require.NoError(t, deps.DeleteIfOwned(ctx, f.task, row.ID, row.Generation, w2.ID))
	_, exists := f.edgeRow(t)
	assert.False(t, exists)
}
