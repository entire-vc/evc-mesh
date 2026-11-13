package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Unknown usage is not zero, against real Postgres: a report can say tokens
// in, tokens out or cost are unknown (each separately), the session keeps that for good, and
// the task summary answers the known sum with a per-measure incomplete flag — null with the
// flag when no part is known, a plain 0 without the flag for a real zero.

func (f *usageFixture) endActive(t *testing.T) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE agent_sessions SET status='ended', ended_at=now() WHERE agent_id=$1 AND status='active'`, f.agentID)
	require.NoError(t, err)
}

func TestUnknownUsage_AllKnown_SumsWithoutFlags_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)

	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 100, TokensOut: 10, Cost: 0.5}, "m")
	require.NoError(t, err)
	f.endActive(t)
	_, err = f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 30, TokensOut: 2, Cost: 0.25}, "m")
	require.NoError(t, err)

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	require.NotNil(t, s.TokensIn)
	require.NotNil(t, s.TokensOut)
	require.NotNil(t, s.TotalCost)
	assert.EqualValues(t, 130, *s.TokensIn)
	assert.EqualValues(t, 12, *s.TokensOut)
	assert.InDelta(t, 0.75, *s.TotalCost, 1e-9)
	assert.False(t, s.TokensInIncomplete || s.TokensOutIncomplete || s.CostIncomplete)
	assert.Equal(t, 2, s.SessionCount)
}

func TestUnknownUsage_AllUnknown_IsNullWithFlag_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)

	sess, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensInUnknown: true, TokensOutUnknown: true, CostUnknown: true}, "m")
	require.NoError(t, err)
	assert.False(t, sess.TokensInKnown || sess.TokensOutKnown || sess.CostKnown)
	assert.True(t, sess.TokensInUnknown && sess.TokensOutUnknown && sess.CostUnknown)

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	assert.Nil(t, s.TokensIn, "no known part is null, not 0")
	assert.Nil(t, s.TokensOut)
	assert.Nil(t, s.TotalCost)
	assert.True(t, s.TokensInIncomplete && s.TokensOutIncomplete && s.CostIncomplete)
}

func TestUnknownUsage_MixedAcrossReportsAndSessions_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)

	// Session 1: tokens_in known, then unknown, then known again; cost always known.
	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 100, TokensOut: 5, Cost: 1}, "m")
	require.NoError(t, err)
	_, err = f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensOut: 5, Cost: 1, TokensInUnknown: true}, "m")
	require.NoError(t, err)
	last, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 50, TokensOut: 5, Cost: 1}, "m")
	require.NoError(t, err)
	assert.EqualValues(t, 150, last.TokensIn, "the unknown report adds nothing, known ones add up")
	assert.True(t, last.TokensInKnown && last.TokensInUnknown, "known + unknown stays flagged: a later known report does not clear it")

	// Session 2: cost unknown, tokens known.
	f.endActive(t)
	_, err = f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 7, TokensOut: 7, CostUnknown: true}, "m")
	require.NoError(t, err)

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	require.NotNil(t, s.TokensIn)
	require.NotNil(t, s.TokensOut)
	require.NotNil(t, s.TotalCost)
	assert.EqualValues(t, 157, *s.TokensIn)
	assert.EqualValues(t, 22, *s.TokensOut)
	assert.InDelta(t, 3.0, *s.TotalCost, 1e-9, "cost: the known sum of session 1")
	assert.True(t, s.TokensInIncomplete, "tokens_in was unknown once")
	assert.False(t, s.TokensOutIncomplete, "tokens_out was always known")
	assert.True(t, s.CostIncomplete, "session 2 has no cost")
}

func TestUnknownUsage_RealZeroStaysZeroWithoutFlag_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)

	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{TokensIn: 0, TokensOut: 0, Cost: 0}, "m")
	require.NoError(t, err)

	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	require.NotNil(t, s.TokensIn)
	require.NotNil(t, s.TokensOut)
	require.NotNil(t, s.TotalCost)
	assert.EqualValues(t, 0, *s.TokensIn)
	assert.EqualValues(t, 0, *s.TokensOut)
	assert.EqualValues(t, 0, *s.TotalCost)
	assert.False(t, s.TokensInIncomplete || s.TokensOutIncomplete || s.CostIncomplete)
}

func TestUnknownUsage_PlainNumbersReadAsToday_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)

	// A session first opened by tool-call tracking has no figure yet...
	require.NoError(t, f.repo.IncrementToolBreakdown(ctx, f.agentID, f.wsID, &task, map[string]int64{"get_task": 1}))
	s, err := f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	assert.Nil(t, s.TotalCost, "nothing was ever reported: not a 0")
	assert.True(t, s.CostIncomplete)

	// ...and the old-style report (AddUsage, no unknown field) makes every measure known.
	sess, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, &task, 40, 4, 0.2, "m")
	require.NoError(t, err)
	assert.True(t, sess.TokensInKnown && sess.TokensOutKnown && sess.CostKnown)
	assert.False(t, sess.TokensInUnknown || sess.TokensOutUnknown || sess.CostUnknown)

	s, err = f.repo.GetTaskCostSummary(ctx, task)
	require.NoError(t, err)
	require.NotNil(t, s.TotalCost)
	assert.EqualValues(t, 40, *s.TokensIn)
	assert.InDelta(t, 0.2, *s.TotalCost, 1e-9)
	assert.False(t, s.TokensInIncomplete || s.TokensOutIncomplete || s.CostIncomplete)
}

func TestUnknownUsage_TaskWithoutSessionsIsPlainZero_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	task := f.seedTask(t)
	s, err := f.repo.GetTaskCostSummary(context.Background(), task)
	require.NoError(t, err)
	require.NotNil(t, s.TotalCost)
	assert.EqualValues(t, 0, *s.TotalCost)
	assert.False(t, s.CostIncomplete)
	assert.Equal(t, 0, s.SessionCount)
}
