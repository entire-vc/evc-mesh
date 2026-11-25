package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func memoryJournalIsolatedDB(t *testing.T) (database *sqlx.DB, downSQL string) {
	t.Helper()
	admin := userRepoTestDB(t)
	schema := "memoryjournal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := sqlx.Connect("postgres", dsnWithSearchPath(t, userRepoTestDSN(), schema))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	raw, err := os.ReadFile("../../../migrations/20261119600_memory_read_events.sql")
	require.NoError(t, err)
	halves := strings.Split(string(raw), "-- +goose Down")
	require.Len(t, halves, 2)
	_, err = db.Exec(halves[0])
	require.NoError(t, err)
	return db, halves[1]
}

func TestMemoryReadEventsDB_RetentionAndDown(t *testing.T) {
	db, down := memoryJournalIsolatedDB(t)
	repo := NewMemoryReadEventRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	text := "old query"
	var events []domain.MemoryReadEvent
	for _, age := range []int{91, 31, 29, 0} {
		events = append(events, domain.MemoryReadEvent{TS: now.Add(-time.Duration(age) * 24 * time.Hour), Op: "recall", Route: "GET /api/v1/memories/search", QueryText: &text, CallerKind: "invalid"})
	}
	require.NoError(t, repo.InsertMemoryReadEvents(ctx, events))
	deleted, cleared, err := repo.PurgeMemoryReadEvents(ctx, now.Add(-90*24*time.Hour), now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	require.Equal(t, int64(1), cleared)
	var rows []struct {
		Text    *string `db:"query_text"`
		Caller  string  `db:"caller_kind"`
		Results string  `db:"results"`
	}
	require.NoError(t, db.Select(&rows, `SELECT query_text,caller_kind,results::text FROM memory_read_events ORDER BY ts`))
	require.Len(t, rows, 3)
	require.Nil(t, rows[0].Text)
	require.Equal(t, text, *rows[1].Text)
	require.Equal(t, text, *rows[2].Text)
	for _, r := range rows {
		require.Equal(t, "unknown", r.Caller)
		require.Equal(t, "[]", r.Results)
	}
	deleted, cleared, err = repo.PurgeMemoryReadEvents(ctx, now.Add(-90*24*time.Hour), now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.Zero(t, cleared)
	_, err = db.Exec(down)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.Get(&count, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('memory_read_events','agent_tool_calls_daily')`))
	require.Zero(t, count, "down removes both new tables")
}

func TestMemoryReadEventsDB_DailyNullableProjectUpsert(t *testing.T) {
	db, _ := memoryJournalIsolatedDB(t)
	_, err := db.Exec(`CREATE TABLE tasks(id uuid PRIMARY KEY, project_id uuid NOT NULL)`)
	require.NoError(t, err)
	repo := NewMemoryReadEventRepo(db)
	ctx := context.Background()
	ws, agent, project, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO tasks(id,project_id) VALUES($1,$2)`, task, project)
	require.NoError(t, err)
	base := domain.ToolCallsDailyDelta{Day: time.Now().UTC(), WorkspaceID: ws, AgentID: agent, Tool: "recall", CallerKind: "agent_tool", Calls: 1}
	require.NoError(t, repo.IncrementToolCallsDaily(ctx, []domain.ToolCallsDailyDelta{base, base}))
	base.TaskID = &task
	require.NoError(t, repo.IncrementToolCallsDaily(ctx, []domain.ToolCallsDailyDelta{base}))
	base.TaskID = nil
	base.ProjectID = &project
	require.NoError(t, repo.IncrementToolCallsDaily(ctx, []domain.ToolCallsDailyDelta{base}))
	var rows []struct {
		Project *uuid.UUID `db:"project_id"`
		Calls   int64      `db:"calls"`
	}
	require.NoError(t, db.Select(&rows, `SELECT project_id,calls FROM agent_tool_calls_daily ORDER BY project_id NULLS FIRST`))
	require.Len(t, rows, 2)
	require.Nil(t, rows[0].Project)
	require.Equal(t, int64(2), rows[0].Calls)
	require.Equal(t, project, *rows[1].Project)
	require.Equal(t, int64(2), rows[1].Calls)
}

func TestMemoryReadEventsDB_EdgeCasesAndFailures(t *testing.T) {
	db, down := memoryJournalIsolatedDB(t)
	_, err := db.Exec(`CREATE TABLE tasks(id uuid PRIMARY KEY, project_id uuid NOT NULL)`)
	require.NoError(t, err)
	repo := NewMemoryReadEventRepo(db)
	ctx := context.Background()
	require.NoError(t, repo.InsertMemoryReadEvents(ctx, nil), "an empty batch is a no-op")
	require.NoError(t, repo.IncrementToolCallsDaily(ctx, nil), "an empty batch is a no-op")

	// A zero timestamp is stamped at write time; a zero delta and an unknown
	// caller kind are normalised instead of failing the batch.
	before := time.Now().Add(-time.Minute)
	require.NoError(t, repo.InsertMemoryReadEvents(ctx, []domain.MemoryReadEvent{{Op: "recall", Route: "GET /api/v1/memories/search"}}))
	var ts time.Time
	require.NoError(t, db.Get(&ts, `SELECT ts FROM memory_read_events`))
	require.True(t, ts.After(before), "zero TS is written as now, got %s", ts)
	ws, agent := uuid.New(), uuid.New()
	base := domain.ToolCallsDailyDelta{Day: time.Now().UTC(), WorkspaceID: ws, AgentID: agent, Tool: "recall", CallerKind: "browser", Calls: 3}
	zero := base
	zero.Tool, zero.Calls = "skipped", 0
	require.NoError(t, repo.IncrementToolCallsDaily(ctx, []domain.ToolCallsDailyDelta{base, zero}))
	var rows []struct {
		Tool   string `db:"tool"`
		Caller string `db:"caller_kind"`
		Calls  int64  `db:"calls"`
	}
	require.NoError(t, db.Select(&rows, `SELECT tool,caller_kind,calls FROM agent_tool_calls_daily`))
	require.Len(t, rows, 1, "a zero delta writes no bucket")
	require.Equal(t, "unknown", rows[0].Caller)
	require.Equal(t, int64(3), rows[0].Calls)

	// Without the tables every write reports its error.
	_, err = db.Exec(down)
	require.NoError(t, err)
	require.Error(t, repo.InsertMemoryReadEvents(ctx, []domain.MemoryReadEvent{{Op: "recall", Route: "r"}}))
	require.Error(t, repo.IncrementToolCallsDaily(ctx, []domain.ToolCallsDailyDelta{base}))
	_, _, err = repo.PurgeMemoryReadEvents(ctx, time.Now(), time.Now())
	require.Error(t, err)

	// A pool that is gone fails at the transaction start.
	require.NoError(t, db.Close())
	require.Error(t, repo.InsertMemoryReadEvents(ctx, []domain.MemoryReadEvent{{Op: "recall", Route: "r"}}))
	require.Error(t, repo.IncrementToolCallsDaily(ctx, []domain.ToolCallsDailyDelta{base}))
}
