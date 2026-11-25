package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ReportUsage with a journal turn against real Postgres (no build tag, skipped
// without a database, like the other *_db_test.go files): the session addition,
// the agent_usage_turns row and its agent_usage_hourly hour move together, a
// repeated turn ref writes nothing and names the first report, and a journal
// failure rolls the session addition back on both the create and the add path.

func usageTurnInt(v int64) *int64 { return &v }

type usageTurnRow struct {
	ID          uuid.UUID     `db:"id"`
	SessionID   uuid.NullUUID `db:"session_id"`
	ProjectID   uuid.NullUUID `db:"project_id"`
	Provider    string        `db:"provider"`
	Model       string        `db:"model"`
	TokensIn    *int64        `db:"tokens_in"`
	CacheRead   *int64        `db:"cache_read"`
	OccurredAt  time.Time     `db:"occurred_at"`
	BillingMode *string       `db:"billing_mode"`
}

func (f *usageFixture) turns(t *testing.T) []usageTurnRow {
	t.Helper()
	var rows []usageTurnRow
	require.NoError(t, f.db.Select(&rows, `
		SELECT id, session_id, project_id, provider, model, tokens_in, cache_read, occurred_at, billing_mode
		FROM agent_usage_turns WHERE agent_id=$1 ORDER BY occurred_at, id`, f.agentID))
	return rows
}

type usageHourRow struct {
	Hour     time.Time `db:"hour"`
	Turns    int64     `db:"turns"`
	TokensIn int64     `db:"tokens_in"`
}

func (f *usageFixture) hours(t *testing.T) []usageHourRow {
	t.Helper()
	var rows []usageHourRow
	require.NoError(t, f.db.Select(&rows, `
		SELECT hour, turns, tokens_in FROM agent_usage_hourly WHERE agent_id=$1 ORDER BY hour`, f.agentID))
	return rows
}

func TestSessionRepo_ReportUsage_TurnJournalAndHour_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	task := f.seedTask(t)
	ended := time.Date(2026, 11, 18, 10, 42, 0, 0, time.UTC)
	ref := "turn-a-" + uuid.NewString()[:8]

	// No active session: created with the report, the journal row names it.
	// Turn.ID left nil: the repository picks one.
	first, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{
		TokensIn: 100, TokensOut: 10, Cost: 0.5,
		Turn: &domain.UsageTurn{TurnRef: &ref, EndedAt: &ended, TokensIn: usageTurnInt(100), CacheRead: usageTurnInt(40)},
	}, "claude-opus-5-5")
	require.NoError(t, err)

	// Active session: the addition path, a second turn without a ref.
	second, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, &task, domain.SessionUsage{
		TokensIn: 30, Turn: &domain.UsageTurn{ID: uuid.New(), EndedAt: &ended, TokensIn: usageTurnInt(30)},
	}, "claude-opus-5-5")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "the second report lands on the same session")
	assert.EqualValues(t, 130, second.TokensIn)

	turns := f.turns(t)
	require.Len(t, turns, 2)
	for _, r := range turns {
		assert.NotEqual(t, uuid.Nil, r.ID)
		assert.Equal(t, uuid.NullUUID{UUID: first.ID, Valid: true}, r.SessionID)
		require.True(t, r.ProjectID.Valid, "project resolved from the task")
		assert.Equal(t, "anthropic", r.Provider, "provider derived from the model")
		assert.True(t, r.OccurredAt.Equal(ended), "ended_at picks the moment")
	}
	var sawCache bool
	for _, r := range turns {
		if r.CacheRead != nil {
			sawCache = true
			assert.EqualValues(t, 40, *r.CacheRead)
		}
	}
	assert.True(t, sawCache, "cache_read stored on the first turn, NULL on the second")

	hours := f.hours(t)
	require.Len(t, hours, 1)
	assert.True(t, hours[0].Hour.Equal(ended.Truncate(time.Hour)))
	assert.EqualValues(t, 2, hours[0].Turns)
	assert.EqualValues(t, 130, hours[0].TokensIn)
}

func TestSessionRepo_ReportUsage_RepeatTurnRefWritesNothing_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	ref := "turn-r-" + uuid.NewString()[:8]
	turn := func() *domain.UsageTurn {
		return &domain.UsageTurn{ID: uuid.New(), TurnRef: &ref, TokensIn: usageTurnInt(50)}
	}

	first, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{TokensIn: 50, Turn: turn()}, "gpt-5")
	require.NoError(t, err)
	firstTurn := f.turns(t)
	require.Len(t, firstTurn, 1)
	assert.Equal(t, "openai", firstTurn[0].Provider)

	sess, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{TokensIn: 50, Turn: turn()}, "gpt-5")
	assert.Nil(t, sess)
	var rep *domain.UsageTurnRepeat
	require.True(t, errors.As(err, &rep), "a repeated turn ref answers UsageTurnRepeat, got %v", err)
	assert.Equal(t, firstTurn[0].ID, rep.TurnID)
	require.NotNil(t, rep.Session)
	assert.Equal(t, first.ID, rep.Session.ID)
	assert.EqualValues(t, 50, rep.Session.TokensIn, "the repeat added nothing to the session")
	assert.Contains(t, rep.Error(), firstTurn[0].ID.String())
	assert.Len(t, f.turns(t), 1)
	assert.EqualValues(t, 1, f.hours(t)[0].Turns)

	// The session the first report landed on is gone: the repeat still names
	// the journal row, without a session.
	_, err = f.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE id=$1`, first.ID)
	require.NoError(t, err)
	_, err = f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{TokensIn: 50, Turn: turn()}, "gpt-5")
	require.True(t, errors.As(err, &rep), "got %v", err)
	assert.Equal(t, firstTurn[0].ID, rep.TurnID)
	assert.Nil(t, rep.Session)
	assert.Zero(t, f.sessions(t), "the repeat created no new session")
}

func TestSessionRepo_ReportUsage_JournalFailureRollsBack_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	bad := "prepaid" // violates the billing_mode CHECK the handler normally guards
	badTurn := func() *domain.UsageTurn { return &domain.UsageTurn{ID: uuid.New(), BillingMode: &bad} }

	// Create path: no session yet, the failed journal row leaves none behind.
	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{TokensIn: 10, Turn: badTurn()}, "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage turn")
	assert.Zero(t, f.sessions(t), "the session insert rolled back with the journal row")

	// Addition path: an existing session keeps its figures.
	sess, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{TokensIn: 10}, "m")
	require.NoError(t, err)
	_, err = f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{TokensIn: 99, Turn: badTurn()}, "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage turn")
	var tokensIn int64
	require.NoError(t, f.db.Get(&tokensIn, `SELECT tokens_in FROM agent_sessions WHERE id=$1`, sess.ID))
	assert.EqualValues(t, 10, tokensIn, "the addition rolled back with the journal row")
	assert.Empty(t, f.turns(t))
	assert.Empty(t, f.hours(t))
}

// Positive control for the failure test above: the same report with a valid
// billing mode is written, so the refusal is the CHECK, not the fixture.
func TestSessionRepo_ReportUsage_ValidBillingModeWritten_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	mode := "subscription"
	_, err := f.repo.ReportUsage(ctx, f.agentID, f.wsID, nil, domain.SessionUsage{
		TokensIn: 10, Turn: &domain.UsageTurn{ID: uuid.New(), BillingMode: &mode},
	}, "m")
	require.NoError(t, err)
	turns := f.turns(t)
	require.Len(t, turns, 1)
	require.NotNil(t, turns[0].BillingMode)
	assert.Equal(t, mode, *turns[0].BillingMode)
	assert.False(t, turns[0].ProjectID.Valid, "no task, no project")
	assert.Equal(t, "unknown", turns[0].Provider)
}
