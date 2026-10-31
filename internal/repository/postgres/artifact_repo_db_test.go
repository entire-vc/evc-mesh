//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Concurrent uploads to one task must not jointly pass the per-task artifact
// limit: eight 10-byte uploads race for a 25-byte task limit and
// exactly two may hold room. Then the artifact takes over its reservation, a
// released reservation frees its room, and an expired one stops counting.
func TestArtifactRepo_ReserveTaskBytes_Concurrent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)
	task := makeTestTask(proj.ID, status.ID, "artifact limit host task")
	require.NoError(t, NewTaskRepo(db).Create(ctx, task, nil))
	repo := NewArtifactRepo(db)

	const racers, size, limit = 8, 10, 25
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, racers)
	results := make([]bool, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		ids[i] = uuid.New()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], _, errs[i] = repo.ReserveTaskBytes(ctx, ids[i], task.ID, size, limit, time.Minute)
		}(i)
	}
	close(start)
	wg.Wait()

	var won []uuid.UUID
	for i := range results {
		require.NoError(t, errs[i])
		if results[i] {
			won = append(won, ids[i])
		}
	}
	require.Len(t, won, 2)
	held := func() (artifacts, reservations int64) {
		require.NoError(t, db.GetContext(ctx, &artifacts, `SELECT COALESCE(SUM(size_bytes), 0) FROM artifacts WHERE task_id = $1`, task.ID))
		require.NoError(t, db.GetContext(ctx, &reservations, `SELECT COALESCE(SUM(size_bytes), 0) FROM artifact_reservations WHERE task_id = $1`, task.ID))
		return artifacts, reservations
	}
	artifacts, reservations := held()
	assert.Zero(t, artifacts, "nothing is visible as an artifact while uploads are in flight")
	assert.EqualValues(t, 2*size, reservations)

	// The first upload lands: its room passes to the artifact.
	require.NoError(t, repo.CreateFromReservation(ctx, won[0], &domain.Artifact{
		ID: won[0], TaskID: task.ID, Name: "bundle.tar.gz",
		ArtifactType: domain.ArtifactTypeFile, MimeType: "application/gzip",
		StorageKey: "test/" + won[0].String(), SizeBytes: size,
		Metadata: json.RawMessage(`{}`), UploadedBy: uuid.New(),
		UploadedByType: domain.UploaderTypeAgent, CreatedAt: time.Now().UTC(),
	}))
	artifacts, reservations = held()
	assert.EqualValues(t, size, artifacts)
	assert.EqualValues(t, size, reservations)

	// The second fails and is released: its room is free again.
	require.NoError(t, repo.ReleaseReservation(ctx, won[1]))
	ok, _, err := repo.ReserveTaskBytes(ctx, uuid.New(), task.ID, size, limit, -time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	// A second born-expired one fits too: the first stopped counting.

	// That one was born expired (a crashed or too slow upload): it can no
	// longer be recorded, it no longer counts, and the next reservation on the
	// task purges it.
	expired := uuid.New()
	ok, _, err = repo.ReserveTaskBytes(ctx, expired, task.ID, size, limit, -time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	err = repo.CreateFromReservation(ctx, expired, &domain.Artifact{
		ID: expired, TaskID: task.ID, Name: "late.tar.gz",
		ArtifactType: domain.ArtifactTypeFile, MimeType: "application/gzip",
		StorageKey: "test/" + expired.String(), SizeBytes: size,
		Metadata: json.RawMessage(`{}`), UploadedBy: uuid.New(),
		UploadedByType: domain.UploaderTypeAgent, CreatedAt: time.Now().UTC(),
	})
	require.ErrorIs(t, err, repository.ErrArtifactReservationExpired)
	artifacts, _ = held()
	assert.EqualValues(t, size, artifacts, "an expired reservation records nothing")

	ok, used, err := repo.ReserveTaskBytes(ctx, uuid.New(), task.ID, size, limit, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.EqualValues(t, size, used)
	_, reservations = held()
	assert.EqualValues(t, size, reservations)
}
