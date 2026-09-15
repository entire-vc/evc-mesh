package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #872f82a2: EndStale used to filter on started_at, so a session open
// longer than the timeout was force-ended regardless of ongoing activity —
// exactly the case for a fiddler lane working one task for many hours. This
// pins the fix: the WHERE clause names last_activity_at, not started_at.
func TestSessionRepo_EndStale_FiltersOnLastActivityAt_NotStartedAt(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 2))

	n, err := repo.EndStale(context.Background(), 6*time.Hour)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 2, n)

	require.Len(t, *captured, 1)
	sql := (*captured)[0]
	assert.Regexp(t, regexp.MustCompile(`(?i)status\s*=\s*'active'`), sql)
	assert.Regexp(t, regexp.MustCompile(`(?i)last_activity_at\s*<\s*\$1`), sql,
		"EndStale must age sessions out by inactivity (last_activity_at), not by how long ago they started")
	assert.NotRegexp(t, regexp.MustCompile(`(?i)started_at\s*<`), sql,
		"a session must not be closed purely for being old while still receiving tool calls")
}

// Both IncrementToolBreakdown query shapes must bump last_activity_at in the
// same atomic UPDATE that adds the tool-call counts — a separate follow-up
// write would race with EndStale's sweep (which could fire in the gap and
// close a session the increment was about to revive).
func TestSessionRepo_IncrementToolBreakdown_BumpsLastActivityAt_AgentWide(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.IncrementToolBreakdown(context.Background(), uuid.New(), uuid.New(), nil,
		map[string]int64{"recall": 1})
	require.NoError(t, err)
	require.Len(t, *captured, 1)
	assert.Contains(t, (*captured)[0], "last_activity_at = now()")
}

func TestSessionRepo_IncrementToolBreakdown_BumpsLastActivityAt_TaskScoped(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	taskID := uuid.New()
	err := repo.IncrementToolBreakdown(context.Background(), uuid.New(), uuid.New(), &taskID,
		map[string]int64{"add_comment": 1})
	require.NoError(t, err)
	require.Len(t, *captured, 1)
	assert.Contains(t, (*captured)[0], "last_activity_at = now()")
}

// A session-report (ReportSession -> Update) is exactly as much "activity"
// as a tool call and must also postpone EndStale — otherwise a long task
// that only ever calls the report endpoint (no other tool traffic in the
// window) would still age out between reports.
func TestSessionRepo_Update_BumpsLastActivityAt(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	s := &domain.AgentSession{
		ID:            uuid.New(),
		Status:        domain.AgentSessionStatusActive,
		ModelUsed:     "claude-opus-4-7",
		TokensIn:      100,
		TokensOut:     50,
		EstimatedCost: 0.01,
	}
	err := repo.Update(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, *captured, 1)
	assert.Contains(t, (*captured)[0], "last_activity_at")
	assert.Regexp(t, regexp.MustCompile(`(?i)GREATEST\(\s*last_activity_at\s*,\s*now\(\)\s*\)`), (*captured)[0],
		"must never move last_activity_at backwards — a concurrent tool-call bump must win over a stale report")
}
