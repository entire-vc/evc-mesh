package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDB connects to a real Postgres instance for analytics query tests — these run raw
// SQL directly against agent_sessions/tasks/projects and are not meaningfully testable
// against a mock (see CLAUDE-workflow.md §1o: never mock the DB layer). Skips cleanly
// when no database is reachable (e.g. a sandboxed CI runner without docker).
func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("skipping: cannot connect to database at %s: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("skipping: database unreachable: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedCostFixture creates a workspace + project + task + two agents, then inserts a
// handful of agent_sessions rows with known cost/token values. Returns the fixture IDs
// and registers cleanup. All sessions are started "now" so any From/To window covering
// the current moment picks them up.
func seedCostFixture(t *testing.T, db *sqlx.DB) (workspaceID, projectID, taskID, agentAID, agentBID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	workspaceID = uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		workspaceID, "Cost Test WS", "cost-ws-"+uuid.New().String()[:8], uuid.New(), json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)

	projectID = uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		projectID, workspaceID, "Cost Test Project", "cost-proj-"+uuid.New().String()[:8], "none", json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)

	statusID := uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		statusID, projectID, "Open", "open", "#00FF00", 0, "todo", true, json.RawMessage(`{}`),
	)
	require.NoError(t, err)

	taskID = uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO tasks (id, project_id, status_id, title, description, assignee_type, priority, position, custom_fields, labels, task_number, created_by, created_by_type, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		taskID, projectID, statusID, "Cost Test Task", "", "unassigned", "medium", 1.0, json.RawMessage(`{}`), "{}", 1, uuid.New(), "user", time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)

	agentAID = uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,$3,$4,$5,$6)`,
		agentAID, workspaceID, "Cost Agent A", "cost-agent-a-"+uuid.New().String()[:8], "hash-a", "agk_a",
	)
	require.NoError(t, err)

	agentBID = uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,$3,$4,$5,$6)`,
		agentBID, workspaceID, "Cost Agent B", "cost-agent-b-"+uuid.New().String()[:8], "hash-b", "agk_b",
	)
	require.NoError(t, err)

	// Agent A: two sessions on the task — $1.50 + $0.75 = $2.25, 1000+500 in / 2000+1000 out.
	// None carry model_used, matching prod's unreported tool-breakdown-tracker rows —
	// ReportedSessionCount stays 0 across this fixture by design (see the dedicated
	// coverage test below for the mixed reported/unreported case).
	insertSession(t, db, workspaceID, agentAID, &taskID, 1.50, 1000, 2000, "")
	insertSession(t, db, workspaceID, agentAID, &taskID, 0.75, 500, 1000, "")
	// Agent B: one session, no task — $0.10.
	insertSession(t, db, workspaceID, agentBID, nil, 0.10, 100, 200, "")

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agent_sessions WHERE workspace_id = $1", workspaceID)
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		_, _ = db.ExecContext(ctx, "DELETE FROM agents WHERE workspace_id = $1", workspaceID)
		_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", workspaceID)
	})

	return workspaceID, projectID, taskID, agentAID, agentBID
}

// insertSession inserts one agent_sessions row. modelUsed == "" reproduces the prod
// tool-breakdown-tracker rows that never got a session_report (NULL model_used) — pass a
// real model name (e.g. "claude-sonnet-5") to simulate a reported session.
func insertSession(t *testing.T, db *sqlx.DB, workspaceID, agentID uuid.UUID, taskID *uuid.UUID, cost float64, tokensIn, tokensOut int64, modelUsed string) {
	t.Helper()
	var modelArg interface{}
	if modelUsed != "" {
		modelArg = modelUsed
	}
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_sessions (id, workspace_id, agent_id, task_id, started_at, status, tokens_in, tokens_out, estimated_cost, model_used)
		 VALUES ($1,$2,$3,$4,$5,'ended',$6,$7,$8,$9)`,
		uuid.New(), workspaceID, agentID, taskID, time.Now().UTC(), tokensIn, tokensOut, cost, modelArg,
	)
	require.NoError(t, err)
}

func TestAnalyticsService_GetMetrics_CostMetrics(t *testing.T) {
	db := testDB(t)
	workspaceID, _, taskID, agentAID, agentBID := seedCostFixture(t, db)

	svc := NewAnalyticsService(db)
	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)

	metrics, err := svc.GetMetrics(context.Background(), AnalyticsFilter{
		WorkspaceID: workspaceID,
		From:        from,
		To:          to,
	})
	require.NoError(t, err)
	require.NotNil(t, metrics)

	cost := metrics.CostMetrics
	assert.InDelta(t, 2.35, cost.TotalCost, 0.001)
	assert.Equal(t, int64(1600), cost.TotalTokensIn)
	assert.Equal(t, int64(3200), cost.TotalTokensOut)
	assert.Equal(t, 3, cost.SessionCount)
	// Fixture rows carry no model_used (see seedCostFixture) — none are "reported".
	assert.Equal(t, 0, cost.ReportedSessionCount)

	// By-agent breakdown: A=2.25, B=0.10, ordered by cost DESC.
	require.Len(t, cost.ByAgent, 2)
	assert.Equal(t, agentAID, cost.ByAgent[0].AgentID)
	assert.InDelta(t, 2.25, cost.ByAgent[0].Cost, 0.001)
	assert.Equal(t, agentBID, cost.ByAgent[1].AgentID)
	assert.InDelta(t, 0.10, cost.ByAgent[1].Cost, 0.001)

	// By-project breakdown: only agent A's task-linked sessions count ($2.25).
	require.Len(t, cost.ByProject, 1)
	assert.InDelta(t, 2.25, cost.ByProject[0].Cost, 0.001)

	// Top tasks: the one task both agent-A sessions touched, summed to $2.25.
	require.Len(t, cost.TopTasks, 1)
	assert.Equal(t, taskID, cost.TopTasks[0].TaskID)
	assert.InDelta(t, 2.25, cost.TopTasks[0].Cost, 0.001)
	assert.Equal(t, 2, cost.TopTasks[0].SessionCount)

	// Daily breakdown: all three sessions land on the same day.
	require.Len(t, cost.ByDay, 1)
	assert.InDelta(t, 2.35, cost.ByDay[0].Cost, 0.001)
}

func TestAnalyticsService_GetMetrics_CostMetrics_ProjectFilter(t *testing.T) {
	db := testDB(t)
	workspaceID, projectID, _, agentAID, _ := seedCostFixture(t, db)

	svc := NewAnalyticsService(db)
	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)

	metrics, err := svc.GetMetrics(context.Background(), AnalyticsFilter{
		WorkspaceID: workspaceID,
		ProjectID:   &projectID,
		From:        from,
		To:          to,
	})
	require.NoError(t, err)
	require.NotNil(t, metrics)

	// project_id filter excludes agent B's task-less session ($0.10) — only A's $2.25 remain.
	cost := metrics.CostMetrics
	assert.InDelta(t, 2.25, cost.TotalCost, 0.001)
	assert.Equal(t, 2, cost.SessionCount)
	assert.Equal(t, 0, cost.ReportedSessionCount)
	require.Len(t, cost.ByAgent, 1)
	assert.Equal(t, agentAID, cost.ByAgent[0].AgentID)
}

// TestAnalyticsService_GetMetrics_CostMetrics_ReportedCoverage is the regression test for
// #7fe93754: session_count (every agent_sessions row touched, including unreported
// tool-breakdown-tracker stubs) must stay distinct from reported_session_count (rows that
// actually carry a model_used from a real session_report) — the two must NOT be conflated
// into a single "Total spend" / "Avg per session" figure.
func TestAnalyticsService_GetMetrics_CostMetrics_ReportedCoverage(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	workspaceID := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		workspaceID, "Coverage Test WS", "coverage-ws-"+uuid.New().String()[:8], uuid.New(), json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agent_sessions WHERE workspace_id = $1", workspaceID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", workspaceID)
	})

	agentID := uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,$3,$4,$5,$6)`,
		agentID, workspaceID, "Coverage Agent", "coverage-agent-"+uuid.New().String()[:8], "hash-c", "agk_c",
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agents WHERE workspace_id = $1", workspaceID)
	})

	// 3 reported sessions (real model_used, real cost) + 5 unreported stub rows
	// (model_used NULL, estimated_cost 0 — the tool_breakdown_tracker shape).
	insertSession(t, db, workspaceID, agentID, nil, 1.00, 100, 200, "claude-sonnet-5")
	insertSession(t, db, workspaceID, agentID, nil, 2.00, 200, 400, "claude-sonnet-5")
	insertSession(t, db, workspaceID, agentID, nil, 0.50, 50, 100, "claude-opus-5")
	for i := 0; i < 5; i++ {
		insertSession(t, db, workspaceID, agentID, nil, 0, 0, 0, "")
	}

	svc := NewAnalyticsService(db)
	metrics, err := svc.GetMetrics(context.Background(), AnalyticsFilter{
		WorkspaceID: workspaceID,
		From:        time.Now().Add(-time.Hour),
		To:          time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NotNil(t, metrics)

	cost := metrics.CostMetrics
	assert.Equal(t, 8, cost.SessionCount, "session_count must include the 5 unreported stub rows")
	assert.Equal(t, 3, cost.ReportedSessionCount, "reported_session_count must count only rows with a real model_used")
	assert.InDelta(t, 3.50, cost.TotalCost, 0.001)
}

func TestAnalyticsService_GetMetrics_CostMetrics_EmptyWorkspace(t *testing.T) {
	db := testDB(t)

	svc := NewAnalyticsService(db)
	metrics, err := svc.GetMetrics(context.Background(), AnalyticsFilter{
		WorkspaceID: uuid.New(),
		From:        time.Now().Add(-time.Hour),
		To:          time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NotNil(t, metrics)

	cost := metrics.CostMetrics
	assert.Equal(t, 0.0, cost.TotalCost)
	assert.Equal(t, 0, cost.SessionCount)
	assert.Equal(t, 0, cost.ReportedSessionCount)
	assert.Empty(t, cost.ByAgent)
	assert.Empty(t, cost.ByProject)
	assert.Empty(t, cost.TopTasks)
}
