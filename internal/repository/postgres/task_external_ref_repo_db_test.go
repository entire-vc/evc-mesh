package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestTaskExternalRefRepo_HashWriteOnce(t *testing.T) {
	taskRepo, taskID, _ := checkoutGenerationFixture(t)
	repo := NewTaskExternalRefRepo(taskRepo.db)
	ctx := context.Background()

	none, err := repo.Get(ctx, taskID)
	require.NoError(t, err)
	require.Nil(t, none)

	by := uuid.New()
	got, changed, err := repo.Put(ctx, &domain.TaskExternalRef{TaskID: taskID, Ref: "r1", Hash: "h1", SetBy: &by})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "h1", got.Hash)

	got, changed, err = repo.Put(ctx, &domain.TaskExternalRef{TaskID: taskID, Ref: "r2", Hash: "h1"})
	require.NoError(t, err)
	require.True(t, changed, "same hash refreshes ref")
	require.Equal(t, "r2", got.Ref)

	got, changed, err = repo.Put(ctx, &domain.TaskExternalRef{TaskID: taskID, Ref: "r3", Hash: "h2"})
	require.NoError(t, err)
	require.False(t, changed, "a different hash must be refused")
	require.Equal(t, "h1", got.Hash)
	require.Equal(t, "r2", got.Ref)
}
