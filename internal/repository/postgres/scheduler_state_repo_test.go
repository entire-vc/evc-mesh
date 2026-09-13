package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment in
// user_repo_test.go. This is the core of task #c5b5fb48's defect-1 fix: a
// background job's cadence surviving a process restart is exactly what a real
// Postgres round-trip needs to prove.

func TestSchedulerStateRepoDB_GetLastRun_NeverRunReturnsNil(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewSchedulerStateRepo(db)

	lastRun, err := repo.GetLastRun(context.Background(), "job-"+uuid.New().String())
	require.NoError(t, err)
	assert.Nil(t, lastRun, "a job that has never run has no last_run_at row")
}

func TestSchedulerStateRepoDB_SetThenGetLastRun_RoundTrips(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewSchedulerStateRepo(db)
	jobName := "job-" + uuid.New().String()
	ctx := context.Background()

	runAt := time.Now().Add(-3 * time.Hour).Truncate(time.Microsecond)
	require.NoError(t, repo.SetLastRun(ctx, jobName, runAt))

	got, err := repo.GetLastRun(ctx, jobName)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.WithinDuration(t, runAt, *got, time.Second)
}

// TestSchedulerStateRepoDB_SetLastRun_UpsertsNotDuplicates is the load-bearing case
// for the main.go fix: the SAME job re-persists its watermark on every tick, for
// the lifetime of the process (and across many process restarts) — this must
// update the one row, never accumulate one row per run.
func TestSchedulerStateRepoDB_SetLastRun_UpsertsNotDuplicates(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewSchedulerStateRepo(db)
	jobName := "job-" + uuid.New().String()
	ctx := context.Background()

	first := time.Now().Add(-48 * time.Hour).Truncate(time.Microsecond)
	second := time.Now().Add(-1 * time.Hour).Truncate(time.Microsecond)

	require.NoError(t, repo.SetLastRun(ctx, jobName, first))
	require.NoError(t, repo.SetLastRun(ctx, jobName, second))

	var rowCount int
	require.NoError(t, db.GetContext(ctx, &rowCount,
		`SELECT count(*) FROM scheduler_job_runs WHERE job_name = $1`, jobName))
	assert.Equal(t, 1, rowCount, "SetLastRun must upsert one row per job_name, not insert a new one each call")

	got, err := repo.GetLastRun(ctx, jobName)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.WithinDuration(t, second, *got, time.Second, "the second SetLastRun must win")
}
