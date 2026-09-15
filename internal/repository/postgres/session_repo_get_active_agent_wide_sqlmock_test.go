package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pins the exact defect fixed by task ea1b9fb6: GetActiveAgentWide's WHERE
// clause must filter on task_id IS NULL, not just agent_id + status='active'
// (which is what the pre-fix GetActive did, and which meant it returned
// "the agent's latest active session, whatever task it belongs to" instead
// of the untagged one). A regexp match on "agent_sessions WHERE agent_id"
// alone would stay green even if the IS NULL clause were deleted — this
// pins the clause itself, same style as
// TestSessionRepo_IncrementToolBreakdown_TaskScopedFiltersByTaskID pins
// "AND task_id = $4" for the task-scoped sibling.
func TestSessionRepo_GetActiveAgentWide_FiltersByTaskIDIsNull(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	agentID := uuid.New()
	mock.ExpectQuery("").WillReturnError(sql.ErrNoRows)

	_, err := repo.GetActiveAgentWide(context.Background(), agentID)
	require.NoError(t, err, "sql.ErrNoRows must be swallowed into (nil, nil), not propagated")

	require.Len(t, *captured, 1)
	sqlText := (*captured)[0]
	assert.Regexp(t,
		regexp.MustCompile(`(?i)agent_sessions\s+WHERE\s+agent_id\s*=\s*\$1\s+AND\s+status\s*=\s*'active'\s+AND\s+task_id\s+IS\s+NULL`),
		sqlText,
		"must filter on task_id IS NULL — otherwise this returns the agent's latest "+
			"active session for ANY task, exactly the pre-fix bug (task ea1b9fb6)",
	)
}
