package postgres

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSessionRepoSQL returns a SessionRepo whose driver accepts any
// statement and records every one it sees, in order, so assertions can name
// exactly which columns/expressions went out rather than sqlmock's opaque
// "query does not match" — same helper shape as
// task_repo_arm_human_gate_sqlmock_test.go's captureTaskRepoSQL, and for the
// same reason: a regexp match on "UPDATE agent_sessions SET" alone would
// stay green even if the jsonb merge this test exists to pin were deleted.
func captureSessionRepoSQL(t *testing.T) (*SessionRepo, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	var captured []string
	rawDB, mock, err := sqlmock.New(
		sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = append(captured, actualSQL)
			return nil
		})),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewSessionRepo(sqlx.NewDb(rawDB, "postgres")), mock, &captured
}

// Green arm: an active session exists (UPDATE affects 1 row) — exactly one
// statement is sent, using the fixed incrementToolBreakdownAgentWide query
// text (not something assembled per-call), and it reads tool_breakdown for
// the OLD value inside the same statement, so a concurrent session_report
// accumulating tokens onto the same row cannot lose this increment (see
// SessionRepo.Update's doc comment: Update no longer touches
// tool_breakdown/tool_calls at all, precisely so this atomic path is the
// only writer of those two columns).
func TestSessionRepo_IncrementToolBreakdown_UpdatesExistingActiveSession(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	agentID, workspaceID := uuid.New(), uuid.New()
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.IncrementToolBreakdown(context.Background(), agentID, workspaceID, nil,
		map[string]int64{"recall": 2, "remember": 1})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Len(t, *captured, 1, "an UPDATE that affects a row must not also INSERT")
	sql := (*captured)[0]

	assert.Equal(t, incrementToolBreakdownAgentWide, sql,
		"the agent-wide call must run the fixed constant verbatim — no per-call query assembly")
	assert.Regexp(t, regexp.MustCompile(`(?i)^\s*UPDATE\s+agent_sessions`), sql)
	assert.Contains(t, sql, "jsonb_each_text($1::jsonb)", "must merge the delta via a single bound jsonb parameter, not build one jsonb_set per tool name")
	assert.Regexp(t, regexp.MustCompile(`tool_breakdown\s*->>\s*d\.key`), sql,
		"must read the OLD tool_breakdown value in the same statement — a fetch-then-write "+
			"in Go here would race with a concurrent flush/session_report touching the same row")
	assert.Contains(t, sql, "tool_calls = tool_calls + $2", "tool_calls must be an atomic add via a bound placeholder, not an overwrite or an inlined literal")
	assert.Regexp(t, regexp.MustCompile(`(?i)agent_id\s*=\s*\$3\s+AND\s+status\s*=\s*'active'`), sql)
	assert.NotContains(t, sql, "task_id =", "agent-wide call (taskID=nil) must not filter by task_id at all")
}

// This is the specific regression a CI security gate (semgrep
// go.lang.security.audit.database.string-formatted-query) caught on an
// earlier version of this method: the query text was assembled per call —
// first via fmt.Sprintf embedding `total` directly as "%d" (a real miss:
// a data value in the text, not a placeholder), then, after a first fix
// still using placeholders throughout, via "+"-concatenation of
// jsonb_set(...) fragments and placeholder tokens built from a loop counter.
// That second version never put a raw VALUE in the text either, but the
// scanner flags any $QUERY built from "+"/fmt.Sprintf reaching ExecContext
// regardless of what produced it — it can't prove a given caller only ever
// concatenates safe fragments, and "it happens to be safe here" is exactly
// the assumption that breaks on the next edit.
//
// The actual fix is structural, not a placeholder audit: there is no longer
// any per-call query assembly AT ALL. incrementToolBreakdownAgentWide and
// incrementToolBreakdownTaskScoped are fixed string constants; the only
// per-call choice is WHICH of the two constants runs, and every value
// (including the delta map and its total) travels through args. This test
// asserts that directly — the executed query text is byte-identical to one
// of the two constants for any input, including an unusually large count
// that would have been visible as a literal under either older version.
func TestSessionRepo_IncrementToolBreakdown_QueryTextIsAlwaysOneOfTheTwoConstants(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	const distinctiveCount = int64(123456789) // large + unusual: would stand out if ever inlined
	err := repo.IncrementToolBreakdown(context.Background(), uuid.New(), uuid.New(), nil,
		map[string]int64{"recall": distinctiveCount})
	require.NoError(t, err)

	require.Len(t, *captured, 1)
	sql := (*captured)[0]
	assert.Equal(t, incrementToolBreakdownAgentWide, sql, "query text must be exactly the fixed constant, unaffected by the count's value")
	assert.NotContains(t, sql, "123456789", "the count value must never appear as a literal in the query text — it must travel only through the bound jsonb parameter")
}

// Task-scoped call must filter by task_id too, matching the precedence
// ReportSession already uses when it has a task_id to scope by
// (GetActiveForTask before GetActive), and must run the OTHER fixed
// constant verbatim.
func TestSessionRepo_IncrementToolBreakdown_TaskScopedFiltersByTaskID(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	agentID, workspaceID, taskID := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.IncrementToolBreakdown(context.Background(), agentID, workspaceID, &taskID,
		map[string]int64{"add_comment": 1})
	require.NoError(t, err)

	require.Len(t, *captured, 1)
	assert.Equal(t, incrementToolBreakdownTaskScoped, (*captured)[0],
		"a task-scoped call must run the fixed task-scoped constant verbatim, not a variant of the agent-wide one")
	assert.Regexp(t, regexp.MustCompile(`(?i)agent_id\s*=\s*\$3\s+AND\s+status\s*=\s*'active'\s+AND\s+task_id\s*=\s*\$4`), (*captured)[0])
}

// Red arm: when the UPDATE matches no row (no active session yet), a second
// statement — the fallback Create/INSERT — must be sent, seeded with these
// counts. Without this fallback, a spawn whose very first tool call arrives
// before session_report has ever run would silently drop the increment on
// the floor instead of creating the session tool_breakdown is meant to
// describe.
func TestSessionRepo_IncrementToolBreakdown_CreatesSessionWhenNoneActive(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	agentID, workspaceID := uuid.New(), uuid.New()
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 0)) // UPDATE matches nothing
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(1, 1)) // fallback INSERT

	err := repo.IncrementToolBreakdown(context.Background(), agentID, workspaceID, nil,
		map[string]int64{"get_task": 3})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Len(t, *captured, 2, "a miss on the UPDATE must be followed by exactly one INSERT")
	assert.Regexp(t, regexp.MustCompile(`(?i)^\s*UPDATE\s+agent_sessions`), (*captured)[0])
	assert.Regexp(t, regexp.MustCompile(`(?i)^\s*INSERT\s+INTO\s+agent_sessions`), (*captured)[1])
}

// Entries with a non-positive count or an empty key are dropped rather than
// written — a buggy or malicious client sending {"": 5} or {"recall": -1}
// must not corrupt tool_breakdown or manufacture a session out of nothing.
func TestSessionRepo_IncrementToolBreakdown_DropsNonPositiveAndEmptyKeys(t *testing.T) {
	repo, mock, captured := captureSessionRepoSQL(t)

	err := repo.IncrementToolBreakdown(context.Background(), uuid.New(), uuid.New(), nil,
		map[string]int64{"": 5, "recall": 0, "remember": -3})
	require.NoError(t, err)

	assert.Empty(t, *captured, "an all-invalid counts map must be a pure no-op — no UPDATE, no INSERT")
	_ = mock // no expectations were set; nothing should have been executed
}

// An empty counts map is a no-op, same as above but via the simpler path
// (nothing to filter — there was never anything to write).
func TestSessionRepo_IncrementToolBreakdown_EmptyCountsIsNoOp(t *testing.T) {
	repo, _, captured := captureSessionRepoSQL(t)

	err := repo.IncrementToolBreakdown(context.Background(), uuid.New(), uuid.New(), nil, map[string]int64{})
	require.NoError(t, err)
	assert.Empty(t, *captured)
}
