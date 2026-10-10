package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func runtimeKindIDs(t *testing.T, repo *TaskRepo, projectID uuid.UUID, kind domain.RuntimeKind) []uuid.UUID {
	t.Helper()
	page, err := repo.List(context.Background(), projectID, repository.TaskFilter{RuntimeKind: &kind}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	var ids []uuid.UUID
	for _, task := range page.Items {
		ids = append(ids, task.ID)
	}
	return ids
}

func TestRuntimeState_RoundTripAndFilter(t *testing.T) {
	repo, taskID, owner := checkoutGenerationFixture(t)
	ctx := context.Background()
	var projectID uuid.UUID
	require.NoError(t, repo.db.Get(&projectID, `SELECT project_id FROM tasks WHERE id=$1`, taskID))

	// Nothing stored, nothing checked out: no state, matches no filter.
	task, err := repo.GetByID(ctx, taskID)
	require.NoError(t, err)
	assert.Nil(t, task.RuntimeState)
	assert.Empty(t, runtimeKindIDs(t, repo, projectID, domain.RuntimeKindWorking))

	// Every kind round-trips through the column and is found by exactly its own filter.
	for _, k := range []domain.RuntimeKind{domain.RuntimeKindWorking, domain.RuntimeKindWaiting, domain.RuntimeKindQueued, domain.RuntimeKindStopped} {
		ref := "pipeline:g/p#1"
		s := &domain.RuntimeState{Kind: k, Ref: &ref}
		require.NoError(t, s.Normalize(time.Now().UTC()))
		require.NoError(t, repo.SetRuntimeState(ctx, taskID, s))
		task, err = repo.GetByID(ctx, taskID)
		require.NoError(t, err)
		require.NotNil(t, task.RuntimeState, k)
		assert.Equal(t, k, task.RuntimeState.Kind)
		assert.Equal(t, domain.RuntimeStateSourceExplicit, task.RuntimeState.Source)
		require.NotNil(t, task.RuntimeState.Ref)
		assert.Equal(t, ref, *task.RuntimeState.Ref)
		for _, other := range []domain.RuntimeKind{domain.RuntimeKindWorking, domain.RuntimeKindWaiting, domain.RuntimeKindQueued, domain.RuntimeKindStopped} {
			got := runtimeKindIDs(t, repo, projectID, other)
			if other == k {
				assert.Equal(t, []uuid.UUID{taskID}, got, "filter %s", other)
			} else {
				assert.Empty(t, got, "kind %s stored, filter %s", k, other)
			}
		}
	}

	// The column rejects an unknown kind even if a writer bypasses the service.
	_, err = repo.db.Exec(`UPDATE tasks SET runtime_state='{"kind":"dozing"}' WHERE id=$1`, taskID)
	require.Error(t, err)

	// Clear, then derive from a live checkout (card is in a todo status).
	require.NoError(t, repo.SetRuntimeState(ctx, taskID, nil))
	_, err = repo.AcquireCheckout(ctx, taskID, owner, uuid.New(), time.Now().Add(time.Hour), generationScope())
	require.NoError(t, err)
	task, err = repo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task.RuntimeState)
	assert.Equal(t, domain.RuntimeKindWorking, task.RuntimeState.Kind)
	assert.Equal(t, domain.RuntimeStateSourceCheckout, task.RuntimeState.Source)
	assert.Equal(t, []uuid.UUID{taskID}, runtimeKindIDs(t, repo, projectID, domain.RuntimeKindWorking), "todo + checkout -> working")
	assert.Empty(t, runtimeKindIDs(t, repo, projectID, domain.RuntimeKindWaiting))

	// An explicit `waiting` beats the checkout, in the value and in the filter.
	w := &domain.RuntimeState{Kind: domain.RuntimeKindWaiting}
	require.NoError(t, w.Normalize(time.Now().UTC()))
	require.NoError(t, repo.SetRuntimeState(ctx, taskID, w))
	assert.Empty(t, runtimeKindIDs(t, repo, projectID, domain.RuntimeKindWorking))
	assert.Equal(t, []uuid.UUID{taskID}, runtimeKindIDs(t, repo, projectID, domain.RuntimeKindWaiting))

	// JSON without a kind is "nothing usable stored" to the resolver; the filter
	// must agree and still derive `working` from the live checkout.
	_, err = repo.db.Exec(`UPDATE tasks SET runtime_state='{}' WHERE id=$1`, taskID)
	require.NoError(t, err)
	task, err = repo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task.RuntimeState)
	assert.Equal(t, domain.RuntimeStateSourceCheckout, task.RuntimeState.Source)
	assert.Equal(t, []uuid.UUID{taskID}, runtimeKindIDs(t, repo, projectID, domain.RuntimeKindWorking), "kindless JSON + checkout -> working in the filter too")

	// Unknown / deleted task -> NotFound, not a silent no-op.
	assert.Error(t, repo.SetRuntimeState(ctx, uuid.New(), w))
}
