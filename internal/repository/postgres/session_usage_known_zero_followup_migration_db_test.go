//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// knownZeroFollowupMigration clears the known $0 that 20261118100 left on old rows
// already flagged cost_unknown and on rows the previous build wrote on the day of
// 20261113300 after it.
const knownZeroFollowupMigration int64 = 20261119100

type knownZeroFollowupRow struct {
	started             time.Time
	model               string
	tokensIn, tokensOut int64
	cost                float64
	costUnknown         bool
	wantCost            bool // cost_known after Up
}

// TestSessionUsageKnownZeroFollowupMigration_UpDown: an old zero cost flagged unknown and
// a zero cost with real tokens written on the cut day by the previous build both read as
// an unknown cost after Up (null total, incomplete); non-zero figures, rows without tokens
// and rows from the next day stay known; Down restores every flag.
func TestSessionUsageKnownZeroFollowupMigration_UpDown(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	db := f.db.DB
	const migrationsDir = "../../../migrations"

	require.GreaterOrEqual(t, gooseVersion(t, db), preKnownZeroMigration, "the suite DB must be migrated past 20261118100")
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.DownTo(db, migrationsDir, preKnownZeroMigration))
	t.Cleanup(func() { require.NoError(t, goose.Up(db, migrationsDir)) })
	require.Equal(t, preKnownZeroMigration, gooseVersion(t, db))

	before := time.Date(2026, 11, 13, 9, 30, 23, 0, time.UTC)
	staleWriter := time.Date(2026, 11, 13, 9, 41, 46, 0, time.UTC)
	lastOfDay := time.Date(2026, 11, 13, 23, 59, 59, 0, time.UTC)
	nextDay := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	const opus = "claude-opus-5-5"
	rows := map[string]knownZeroFollowupRow{
		// Defect 1: unknown cost, yet its known part of 0 is summed as a known $0.
		"old zero cost flagged unknown": {started: before, model: opus, tokensIn: 7, tokensOut: 7, costUnknown: true},
		// Defect 2: the previous build after the cut, real tokens, cost 0 stored as known.
		"stale writer after the cut": {started: staleWriter, model: opus, tokensIn: 8001589, tokensOut: 49584},
		"stale writer, last second":  {started: lastOfDay, model: opus, tokensIn: 1, wantCost: false},
		// Stay known.
		"old real cost flagged unknown": {started: before, model: opus, tokensIn: 7, tokensOut: 7, cost: 0.42, costUnknown: true, wantCost: true},
		"cut day, no tokens":            {started: staleWriter, model: opus, wantCost: true},
		"cut day, no model":             {started: staleWriter, tokensIn: 5, wantCost: true},
		"cut day, real cost":            {started: staleWriter, model: opus, tokensIn: 5, cost: 0.10, wantCost: true},
		"next day zero with tokens":     {started: nextDay, model: opus, tokensIn: 5, tokensOut: 5, wantCost: true},
		"cut day, flagged unknown":      {started: staleWriter, model: opus, tokensIn: 5, costUnknown: true, wantCost: true},
	}
	tasks := map[string]uuid.UUID{}
	for name, r := range rows {
		task := f.seedTask(t)
		tasks[name] = task
		_, err := f.db.ExecContext(ctx, `
			INSERT INTO agent_sessions (workspace_id, agent_id, task_id, started_at, status, model_used,
			                            tokens_in, tokens_out, estimated_cost,
			                            tokens_in_known, tokens_out_known, cost_known, cost_unknown)
			VALUES ($1, $2, $3, $4, 'ended', NULLIF($5, ''), $6, $7, $8, true, true, true, $9)`,
			f.wsID, f.agentID, task, r.started, r.model, r.tokensIn, r.tokensOut, r.cost, r.costUnknown)
		require.NoError(t, err, name)
	}
	costKnown := func(name string) bool {
		var k bool
		require.NoError(t, f.db.Get(&k, `SELECT cost_known FROM agent_sessions WHERE task_id=$1`, tasks[name]))
		return k
	}
	defects := []string{"old zero cost flagged unknown", "stale writer after the cut"}

	for _, name := range defects {
		s, err := f.repo.GetTaskCostSummary(ctx, tasks[name])
		require.NoError(t, err)
		require.NotNil(t, s.TotalCost, name)
		require.Zero(t, *s.TotalCost, name)
	}
	t.Log("before Up: both defect rows sum to a known total_cost=0 (defect reproduced)")

	require.NoError(t, goose.UpTo(db, migrationsDir, knownZeroFollowupMigration))
	require.Equal(t, knownZeroFollowupMigration, gooseVersion(t, db))

	for name, r := range rows {
		assert.Equal(t, r.wantCost, costKnown(name), "cost_known after Up: %s", name)
	}
	for _, name := range defects {
		s, err := f.repo.GetTaskCostSummary(ctx, tasks[name])
		require.NoError(t, err)
		assert.Nil(t, s.TotalCost, "%s: unknown, not $0", name)
		assert.True(t, s.CostIncomplete, name)
		require.NotNil(t, s.TokensIn, name)
		assert.Positive(t, *s.TokensIn, "%s: real tokens stay known", name)
	}
	s, err := f.repo.GetTaskCostSummary(ctx, tasks["next day zero with tokens"])
	require.NoError(t, err)
	require.NotNil(t, s.TotalCost, "a row after the cut day keeps its zero")
	assert.Zero(t, *s.TotalCost)
	assert.False(t, s.CostIncomplete)

	require.NoError(t, goose.DownTo(db, migrationsDir, preKnownZeroMigration))
	require.Equal(t, preKnownZeroMigration, gooseVersion(t, db))
	assert.False(t, tableExists(t, db, "agent_sessions_known_zero_followup_backup"), "down drops the backup table")
	for name := range rows {
		assert.True(t, costKnown(name), "cost_known after Down: %s", name)
	}
	s, err = f.repo.GetTaskCostSummary(ctx, tasks["stale writer after the cut"])
	require.NoError(t, err)
	require.NotNil(t, s.TotalCost, "down restores the previous answer")
	assert.False(t, s.CostIncomplete)
}
