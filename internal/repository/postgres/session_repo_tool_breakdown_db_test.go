//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionRepo_IncrementToolBreakdown_RealPostgres is the sqlmock tests'
// missing other half: sqlmock proves the SQL text has the shape claimed
// (jsonb_set, reads-before-writes tool_breakdown, no INSERT on a hit); it
// cannot prove that shape actually accumulates correctly, or that Postgres's
// row lock genuinely serializes a concurrent Update() call the way
// SessionRepo.Update's doc comment claims. Only a real database can show
// that — a mock that returns whatever WillReturnResult says would pass
// identically whether the real UPDATE adds or overwrites.
func TestSessionRepo_IncrementToolBreakdown_RealPostgres(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()

	wsID := uuid.New()
	agentID := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Tool Breakdown Test', $2, $3)`,
		wsID, "tbd-ws-"+wsID.String()[:8], uuid.New())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, 'Tool Breakdown Agent', $3, 'not-a-real-hash', 'test')`,
		agentID, wsID, "tbd-agent-"+agentID.String()[:8])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agent_sessions WHERE agent_id = $1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agents WHERE id = $1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id = $1`, wsID)
	})

	// 1. No active session exists yet — the very first tool call of a spawn
	//    (this is the audit's core complaint: previously nothing created a
	//    session row until session_report ran, so a spawn that crashed before
	//    reporting left no trace at all).
	require.NoError(t, repo.IncrementToolBreakdown(ctx, agentID, wsID, nil,
		map[string]int64{"recall": 2, "get_task": 1}))

	active, err := repo.GetActive(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, active, "the first tool call must create the session row, not wait for session_report")
	assert.Equal(t, 3, active.ToolCalls)
	var breakdown map[string]int64
	require.NoError(t, json.Unmarshal(active.ToolBreakdown, &breakdown))
	assert.Equal(t, int64(2), breakdown["recall"])
	assert.Equal(t, int64(1), breakdown["get_task"])

	// 2. A second flush cycle's worth of counts, including a repeat of an
	//    existing key — must ADD, not overwrite, and must not disturb keys
	//    from the first flush it doesn't mention.
	require.NoError(t, repo.IncrementToolBreakdown(ctx, agentID, wsID, nil,
		map[string]int64{"recall": 3, "remember": 1}))

	active, err = repo.GetActive(ctx, agentID)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(active.ToolBreakdown, &breakdown))
	assert.Equal(t, int64(5), breakdown["recall"], "5 = 2 + 3, an atomic add across two flush cycles")
	assert.Equal(t, int64(1), breakdown["get_task"], "a key untouched by the second flush must survive it")
	assert.Equal(t, int64(1), breakdown["remember"])
	assert.Equal(t, 7, active.ToolCalls, "7 = (2+1) + (3+1) across both flushes")

	// 3. The concurrency claim in SessionRepo.Update's doc comment: a
	//    ReportSession-shaped Update() call that fetched the session BEFORE
	//    this increment must not revert it when it writes back afterward.
	//    This is the exact race Update() used to be able to lose before
	//    tool_breakdown/tool_calls were dropped from its SET list.
	staleCopy := *active // simulates ReportSession's GetActive() fetch, taken before the next increment
	require.NoError(t, repo.IncrementToolBreakdown(ctx, agentID, wsID, nil, map[string]int64{"recall": 100}))

	staleCopy.TokensIn = 999 // ReportSession would also bump tokens/cost on its copy
	require.NoError(t, repo.Update(ctx, &staleCopy))

	final, err := repo.GetActive(ctx, agentID)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(final.ToolBreakdown, &breakdown))
	assert.Equal(t, int64(105), breakdown["recall"],
		"Update() writing back a stale in-memory copy must NOT clobber an increment that "+
			"landed on the row after that copy was fetched — this is what dropping "+
			"tool_breakdown/tool_calls from Update()'s SET list buys")
	assert.Equal(t, int64(999), final.TokensIn, "Update() must still do its own job (tokens) correctly")
}

// TestSessionRepo_IncrementToolBreakdown_RealPostgres_TaskScoped exercises the
// task-scoped branch (a :task_id route) against a real active session created
// with a task_id, proving the WHERE task_id = $n clause actually selects the
// right row rather than falling through to a different active session for
// the same agent.
func TestSessionRepo_IncrementToolBreakdown_RealPostgres_TaskScoped(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()

	// agent_sessions.task_id carries a real FK to tasks(id) (migration
	// 20260610066) — a random uuid.New() for taskA is rejected outright, so
	// this needs the full workspace→project→status→task chain, not just a
	// workspace + agent like the untasked test above.
	ws, proj, status := createTestProject(t, db)
	agentID := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, 'Tool Breakdown Task Agent', $3, 'not-a-real-hash', 'test')`,
		agentID, ws.ID, "tbdt-agent-"+agentID.String()[:8])
	require.NoError(t, err)
	taskA := uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, position)
		 VALUES ($1, $2, $3, 'tool breakdown task-scoped test', $4, $5, 1)`,
		taskA, proj.ID, status.ID, int(time.Now().UnixNano()%1_000_000_000), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agent_sessions WHERE agent_id = $1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id = $1`, taskA)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agents WHERE id = $1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id = $1`, ws.ID)
	})

	// Agent-wide bucket (no task) and task-scoped bucket must land on two
	// distinct rows, same separation ReportSession's own task-scoping relies on.
	require.NoError(t, repo.IncrementToolBreakdown(ctx, agentID, ws.ID, nil, map[string]int64{"heartbeat": 1}))
	require.NoError(t, repo.IncrementToolBreakdown(ctx, agentID, ws.ID, &taskA, map[string]int64{"add_comment": 1}))
	require.NoError(t, repo.IncrementToolBreakdown(ctx, agentID, ws.ID, &taskA, map[string]int64{"add_comment": 2}))

	taskSession, err := repo.GetActiveForTask(ctx, agentID, taskA)
	require.NoError(t, err)
	require.NotNil(t, taskSession)
	var breakdown map[string]int64
	require.NoError(t, json.Unmarshal(taskSession.ToolBreakdown, &breakdown))
	assert.Equal(t, int64(3), breakdown["add_comment"], "3 = 1 + 2 on the task-scoped row")
	_, hasHeartbeat := breakdown["heartbeat"]
	assert.False(t, hasHeartbeat, "the agent-wide heartbeat count must not leak onto the task-scoped row")

	rows, err := db.QueryContext(ctx, `SELECT count(*) FROM agent_sessions WHERE agent_id = $1 AND status = 'active'`, agentID)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var n int
	require.NoError(t, rows.Scan(&n))
	assert.Equal(t, 2, n, "agent-wide and task-scoped calls must create/target two separate session rows, not merge into one")
}
