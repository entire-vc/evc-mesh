package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// A failing database surfaces as an error from every call the nightly job
// makes, so the job logs it and retries on the next pass instead of treating
// the day as empty.
func TestMemoryDailyRollupRepo_DatabaseErrors(t *testing.T) {
	db, err := sqlx.Open("postgres", "postgres://closed.invalid/mesh?sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	repo := NewMemoryDailyRollupRepo(db)
	ctx := context.Background()
	day := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)

	since, err := repo.JournalSince(ctx)
	require.ErrorContains(t, err, "memory journal start")
	require.Nil(t, since)

	written, err := repo.StatsWrittenDays(ctx, day, day.AddDate(0, 0, 1))
	require.ErrorContains(t, err, "written stats days")
	require.Nil(t, written)

	n, err := repo.PendingUsageReads(ctx, day)
	require.ErrorContains(t, err, "pending usage reads")
	require.Zero(t, n)

	_, err = repo.WriteStatsDay(ctx, day)
	require.Error(t, err)
	_, wrote, err := repo.WriteBaseDay(ctx, day, nil)
	require.Error(t, err)
	require.False(t, wrote)
}
