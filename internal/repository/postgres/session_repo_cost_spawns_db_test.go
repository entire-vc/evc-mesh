package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A spawn is a session row that carries a usage report. Another agent reading the card
// (GET /tasks/:task_id/..., counted by ToolBreakdownTracker) opens a task-scoped row with
// tool calls only; that row is not a run and must not move the task's spawn count, its
// unknown-usage flags or its quality flag.

func (f *usageFixture) otherAgent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		VALUES ($1,$2,'Reader Agent',$3,'not-a-real-hash','test')`, id, f.wsID, "reader-agent-"+id.String()[:8])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM agent_sessions WHERE agent_id=$1`, id)
		_, _ = f.db.Exec(`DELETE FROM agents WHERE id=$1`, id)
	})
	return id
}

func TestCostSummary_ToolCallsOnlyRowIsNotASpawn_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)
	reader := f.otherAgent(t)

	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 1000, TokensOut: 200, Cost: 1.5}, "m")
	require.NoError(t, err)
	require.NoError(t, f.repo.IncrementToolBreakdown(ctx, reader, f.wsID, &task,
		map[string]int64{"GET /api/v1/tasks/:task_id/dependencies": 120}))

	var rows int
	require.NoError(t, f.db.Get(&rows, `SELECT count(*) FROM agent_sessions WHERE task_id=$1`, task))
	require.Equal(t, 2, rows, "positive control: the reader's row exists")

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, 1, s.SessionCount, "only the reported run is a spawn")
	assert.False(t, s.CostIncomplete, "a row without a report is not unknown cost")
	assert.False(t, s.TokensInIncomplete)
	assert.False(t, s.TokensOutIncomplete)
	assert.Equal(t, "golden", s.QualityFlag)
	require.NotNil(t, s.TotalCost)
	require.NotNil(t, s.TokensIn)
	assert.InDelta(t, 1.5, *s.TotalCost, 1e-9)
	assert.EqualValues(t, 1000, *s.TokensIn)
}

func TestCostSummary_OnlyToolCallsRowsIsPlainZero_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)

	require.NoError(t, f.repo.IncrementToolBreakdown(ctx, f.agentID, f.wsID, &task, map[string]int64{"get_task": 3}))

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, 0, s.SessionCount)
	assert.Equal(t, "unknown", s.QualityFlag)
	require.NotNil(t, s.TotalCost)
	assert.EqualValues(t, 0, *s.TotalCost)
	assert.False(t, s.CostIncomplete || s.TokensInIncomplete || s.TokensOutIncomplete)
}

func TestCostSummary_UnknownReportStillCountsAndFlags_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)
	reader := f.otherAgent(t)

	// A run that reported "cost unknown" is a spawn and keeps the incomplete flag.
	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 10, TokensOut: 1, CostUnknown: true}, "m")
	require.NoError(t, err)
	require.NoError(t, f.repo.IncrementToolBreakdown(ctx, reader, f.wsID, &task, map[string]int64{"get_task": 1}))

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, 1, s.SessionCount)
	assert.True(t, s.CostIncomplete)
	assert.Nil(t, s.TotalCost)
	assert.False(t, s.TokensInIncomplete)
}
