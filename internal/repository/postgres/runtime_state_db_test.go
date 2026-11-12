package postgres

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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

func taskVersion(t *testing.T, repo *TaskRepo, taskID uuid.UUID) int64 {
	t.Helper()
	var v int64
	require.NoError(t, repo.db.Get(&v, `SELECT version FROM tasks WHERE id=$1`, taskID))
	return v
}

// The runtime_state beacon is refreshed every few minutes and must not move
// tasks.version: every CAS on the version (PATCH expected_version, acceptance
// intents) would otherwise go stale with no visible change on the card.
func TestRuntimeState_BeaconKeepsVersion(t *testing.T) {
	repo, taskID, _ := checkoutGenerationFixture(t)
	ctx := context.Background()

	// A client reads the card (its expected_version) before the beacon fires.
	before, err := repo.GetByID(ctx, taskID)
	require.NoError(t, err)
	v0 := before.Version

	// Two refreshes with different values, then the same value again.
	at := time.Now().UTC()
	for i, kind := range []domain.RuntimeKind{domain.RuntimeKindWorking, domain.RuntimeKindWaiting, domain.RuntimeKindWaiting} {
		s := &domain.RuntimeState{Kind: kind}
		require.NoError(t, s.Normalize(at.Add(time.Duration(min(i, 1))*time.Minute)))
		require.NoError(t, repo.SetRuntimeState(ctx, taskID, s))
		assert.Equal(t, v0, taskVersion(t, repo, taskID), "refresh %d (%s) moved the version", i, kind)
	}
	require.NoError(t, repo.SetRuntimeState(ctx, taskID, nil))
	assert.Equal(t, v0, taskVersion(t, repo, taskID), "clearing the beacon moved the version")
	w := &domain.RuntimeState{Kind: domain.RuntimeKindWaiting}
	require.NoError(t, w.Normalize(at.Add(2*time.Minute)))
	require.NoError(t, repo.SetRuntimeState(ctx, taskID, w))
	got, err := repo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, got.RuntimeState)
	assert.Equal(t, domain.RuntimeKindWaiting, got.RuntimeState.Kind, "the beacon is still stored")
	assert.Equal(t, v0, got.Version)

	// The PATCH taken before the beacon still applies: no 409.
	before.Title = "edited after beacon"
	require.NoError(t, repo.Update(ctx, before), "PATCH with a pre-beacon version must not conflict")
	v1 := taskVersion(t, repo, taskID)
	assert.Equal(t, v0+1, v1, "an ordinary edit advances the version")

	// Every other kind of write still advances it.
	_, err = repo.db.Exec(`UPDATE tasks SET title='plain update' WHERE id=$1`, taskID)
	require.NoError(t, err)
	assert.Equal(t, v1+1, taskVersion(t, repo, taskID), "title UPDATE")
	_, err = repo.db.Exec(`UPDATE tasks SET runtime_state='{"kind":"queued"}', title='mixed' WHERE id=$1`, taskID)
	require.NoError(t, err)
	assert.Equal(t, v1+2, taskVersion(t, repo, taskID), "runtime_state together with another column")
	// Snapshot invalidation (comment/artifact/vcs triggers) is a no-change UPDATE
	// and relies on the bump.
	_, err = repo.db.Exec(`UPDATE tasks SET version = version WHERE id=$1`, taskID)
	require.NoError(t, err)
	assert.Equal(t, v1+3, taskVersion(t, repo, taskID), "snapshot invalidation")
}

// A soft delete that commits while the beacon waits on the row lock must
// surface as NotFound, not as a silent success.
func TestRuntimeState_ConcurrentSoftDeleteIsNotFound(t *testing.T) {
	repo, taskID, _ := checkoutGenerationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &domain.RuntimeState{Kind: domain.RuntimeKindWaiting}
	require.NoError(t, s.Normalize(time.Now().UTC()))

	tx, err := repo.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM tasks WHERE id=$1 FOR UPDATE`, taskID)
	require.NoError(t, err)

	result := make(chan error, 1)
	go func() { result <- repo.SetRuntimeState(ctx, taskID, s) }()
	// Observe the actual PostgreSQL lock wait, not a timing-based assumption.
	require.Eventually(t, func() bool {
		var waiting bool
		qErr := repo.db.GetContext(ctx, &waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			AND wait_event_type='Lock' AND query LIKE '%SET runtime_state%' AND pid<>pg_backend_pid())`)
		return qErr == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET deleted_at=NOW() WHERE id=$1`, taskID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	err = <-result
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr, "a beacon on a task deleted under it must fail")
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode())
}
