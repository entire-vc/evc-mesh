package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// An entry handed to Create without a time is stored at the moment of the
// write, never as the zero time; an explicit time is kept as given.
func TestActivityLogRepo_CreateStoresWriteTimeWhenUnset(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewActivityLogRepo(db)
	taskID := vcsLinkTestTask(t, db)
	var wsID uuid.UUID
	require.NoError(t, db.GetContext(ctx, &wsID, `SELECT p.workspace_id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID))

	before := time.Now().Add(-time.Second)
	unset := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: wsID, EntityType: "agent", EntityID: uuid.New(),
		Action: "key_issued", ActorID: uuid.New(), ActorType: domain.ActorTypeSystem,
	}
	require.NoError(t, repo.Create(ctx, unset))
	after := time.Now().Add(time.Second)
	assert.False(t, unset.CreatedAt.IsZero(), "Create reports the time it stored")

	var stored time.Time
	require.NoError(t, db.GetContext(ctx, &stored, `SELECT created_at FROM activity_log WHERE id = $1`, unset.ID))
	assert.True(t, stored.After(before) && stored.Before(after), "stored %s outside [%s, %s]", stored, before, after)

	explicit := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	given := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: wsID, EntityType: "agent", EntityID: uuid.New(),
		Action: "key_revoked", ActorID: uuid.New(), ActorType: domain.ActorTypeSystem, CreatedAt: explicit,
	}
	require.NoError(t, repo.Create(ctx, given))
	require.NoError(t, db.GetContext(ctx, &stored, `SELECT created_at FROM activity_log WHERE id = $1`, given.ID))
	assert.True(t, stored.Equal(explicit), "explicit time kept: got %s", stored)
}
