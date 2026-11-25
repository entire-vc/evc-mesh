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

// preKnownZeroMigration turns the zeros of sessions started before 20261113300
// (on prod 2026-11-13 09:30:24 UTC) back into unknown figures; preKnownZeroPrev is
// the migration right before it on main when it was written.
const (
	preKnownZeroMigration int64 = 20261118100
	preKnownZeroPrev      int64 = 20261115100
)

type preKnownZeroRow struct {
	started                   time.Time
	model                     *string
	tokensIn, tokensOut       int64
	cost                      float64
	costUnknown               bool
	wantIn, wantOut, wantCost bool // *_known after Up
}

type knownFlags struct {
	In   bool `db:"tokens_in_known"`
	Out  bool `db:"tokens_out_known"`
	Cost bool `db:"cost_known"`
}

// TestSessionUsagePreKnownZeroMigration_UpDown: an old session with a zero cost and
// real tokens is answered as an unknown cost after Up (null total, incomplete), an
// old session with nothing reported loses every known flag, real figures and rows
// started after the cut stay known; Down puts every flag back.
func TestSessionUsagePreKnownZeroMigration_UpDown(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	db := f.db.DB
	const migrationsDir = "../../../migrations"

	require.GreaterOrEqual(t, gooseVersion(t, db), preKnownZeroPrev, "the suite DB must be migrated past the previous migration")
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.DownTo(db, migrationsDir, preKnownZeroPrev))
	t.Cleanup(func() { require.NoError(t, goose.Up(db, migrationsDir)) })
	require.Equal(t, preKnownZeroPrev, gooseVersion(t, db))

	before := time.Date(2026, 11, 13, 9, 30, 23, 0, time.UTC)
	after := time.Date(2026, 11, 13, 9, 30, 24, 0, time.UTC)
	opus, gpt := "claude-opus-5-5", "gpt-5"
	rows := map[string]preKnownZeroRow{
		// The defect: an old zero cost with real tokens reads as a known $0.
		"old zero cost, real tokens": {started: before, model: &opus, tokensIn: 1200, tokensOut: 300, wantIn: true, wantOut: true},
		// No model and no tokens: only tool_breakdown was ever written.
		"old, nothing reported": {started: before},
		// A model distinguishes this from a tool-breakdown-only row.
		"old, model without tokens": {started: before, model: &gpt, wantIn: true, wantOut: true},
		"old, empty model":          {started: before, model: new(string)},
		"old, only input reported":  {started: before, tokensIn: 1, wantIn: true, wantOut: true},
		"old, only output reported": {started: before, tokensOut: 1, wantIn: true, wantOut: true},
		"no report at the cut":      {started: after, wantIn: true, wantOut: true, wantCost: true},
		// Real figures stay known.
		"old real cost": {started: before, model: &opus, tokensIn: 100, tokensOut: 10, cost: 0.42, wantIn: true, wantOut: true, wantCost: true},
		// Started at the cut: 20261118100 keeps this zero. 20261119100 later clears the known
		// zeros with real tokens written on the cut day by the previous build.
		"zero at the cut": {started: after, model: &opus, tokensIn: 50, tokensOut: 5, wantIn: true, wantOut: true, wantCost: true},
		// Already flagged unknown: cost_known is not the flag that says so, left as it was.
		"old, cost already unknown": {started: before, model: &opus, tokensIn: 7, tokensOut: 7, costUnknown: true, wantIn: true, wantOut: true, wantCost: true},
	}
	tasks := map[string]uuid.UUID{}
	for name, r := range rows {
		task := f.seedTask(t)
		tasks[name] = task
		_, err := f.db.ExecContext(ctx, `
			INSERT INTO agent_sessions (workspace_id, agent_id, task_id, started_at, status, model_used,
			                            tokens_in, tokens_out, estimated_cost, tool_breakdown,
			                            tokens_in_known, tokens_out_known, cost_known, cost_unknown)
			VALUES ($1, $2, $3, $4, 'ended', $5, $6, $7, $8, '{"get_task": 3}', true, true, true, $9)`,
			f.wsID, f.agentID, task, r.started, r.model, r.tokensIn, r.tokensOut, r.cost, r.costUnknown)
		require.NoError(t, err, name)
	}
	flags := func(name string) knownFlags {
		var k knownFlags
		require.NoError(t, f.db.Get(&k, `SELECT tokens_in_known, tokens_out_known, cost_known FROM agent_sessions WHERE task_id=$1`, tasks[name]))
		return k
	}

	baseline, err := f.repo.GetTaskCostSummary(ctx, tasks["old zero cost, real tokens"])
	require.NoError(t, err)
	require.NotNil(t, baseline.TotalCost)
	require.Zero(t, *baseline.TotalCost)
	require.False(t, baseline.CostIncomplete)
	t.Log("before Up: total_cost=0, cost_incomplete=false (defect reproduced)")

	require.NoError(t, goose.UpTo(db, migrationsDir, preKnownZeroMigration))
	require.Equal(t, preKnownZeroMigration, gooseVersion(t, db))

	for name, r := range rows {
		assert.Equal(t, knownFlags{In: r.wantIn, Out: r.wantOut, Cost: r.wantCost}, flags(name), "known flags after Up: %s", name)
	}

	s, err := f.repo.GetTaskCostSummary(ctx, tasks["old zero cost, real tokens"])
	require.NoError(t, err)
	assert.Nil(t, s.TotalCost, "an old zero cost is unknown, not $0")
	assert.True(t, s.CostIncomplete)
	require.NotNil(t, s.TokensIn)
	assert.EqualValues(t, 1200, *s.TokensIn, "real tokens stay known")
	assert.False(t, s.TokensInIncomplete)

	s, err = f.repo.GetTaskCostSummary(ctx, tasks["old, nothing reported"])
	require.NoError(t, err)
	// Once the migration clears the inherited flags, this tool-only row is
	// excluded from the spawn aggregate, just like a newly tracked tool call.
	assert.Zero(t, s.SessionCount)
	require.NotNil(t, s.TotalCost)
	require.NotNil(t, s.TokensIn)
	require.NotNil(t, s.TokensOut)
	assert.Zero(t, *s.TotalCost)
	assert.Zero(t, *s.TokensIn)
	assert.Zero(t, *s.TokensOut)
	assert.False(t, s.CostIncomplete || s.TokensInIncomplete || s.TokensOutIncomplete)

	s, err = f.repo.GetTaskCostSummary(ctx, tasks["zero at the cut"])
	require.NoError(t, err)
	require.NotNil(t, s.TotalCost, "a zero after the cut is a real zero")
	assert.Zero(t, *s.TotalCost)
	assert.False(t, s.CostIncomplete)

	require.NoError(t, goose.DownTo(db, migrationsDir, preKnownZeroPrev))
	require.Equal(t, preKnownZeroPrev, gooseVersion(t, db))
	assert.False(t, tableExists(t, db, "agent_sessions_pre_known_zero_backup"), "down drops the backup table")
	for name := range rows {
		assert.Equal(t, knownFlags{In: true, Out: true, Cost: true}, flags(name), "known flags after Down: %s", name)
	}
	s, err = f.repo.GetTaskCostSummary(ctx, tasks["old zero cost, real tokens"])
	require.NoError(t, err)
	require.NotNil(t, s.TotalCost, "down restores the previous answer")
	assert.False(t, s.CostIncomplete)
}
