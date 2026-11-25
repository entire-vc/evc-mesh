package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// DB-backed, no build tag (same convention as the other *_db_test.go files):
// skipped when Postgres is not reachable. The journal and the daily counters
// write through the real PostgreSQL repository into the migrated tables; the
// handlers behind the middleware are plain stand-ins because the unit under
// test is the middleware and its writer, not the memory handlers (those are
// covered end to end in internal/handler/memory_read_log_db_test.go).

func memoryReadLogTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable" // pragma: allowlist secret
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// closedSinkRepo is a real repository over a closed pool: every write fails
// the way it does when the database goes away under the writer.
func closedSinkRepo(t *testing.T) *postgres.MemoryReadEventRepo {
	t.Helper()
	db, err := sqlx.Open("postgres", "postgres://127.0.0.1:1/mesh?sslmode=disable")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return postgres.NewMemoryReadEventRepo(db)
}

func TestMemoryReadLogDB_RunFlushAndRows(t *testing.T) {
	db := memoryReadLogTestDB(t)
	ctx := context.Background()
	ws, user, project := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, ws) })

	l := NewMemoryReadLog(postgres.NewMemoryReadEventRepo(db), true)
	require.True(t, l.Enabled())
	e := echo.New()
	// mock: external boundary — identity normally supplied by the auth provider.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(ContextKeyAuthType, AuthTypeUser)
			c.Set(ContextKeyUserID, user)
			return next(c)
		}
	})
	e.Use(l.Middleware())
	e.GET("/api/v1/memories/search", func(c echo.Context) error {
		return c.JSON(200, map[string]any{"items": []map[string]any{{"id": uuid.NewString(), "key": "k1", "scope": "project", "score": 0.5}}, "search_mode": "hybrid"})
	})
	e.GET("/api/v1/memories/:id", func(c echo.Context) error { return echo.NewHTTPError(http.StatusNotFound, "memory not found") })
	e.GET("/api/v1/memories/by-key", func(c echo.Context) error { return errors.New("store unavailable") })
	e.GET("/api/v1/projects/:proj_id/updates", func(c echo.Context) error {
		return c.JSON(200, map[string]any{"items": []map[string]any{{"id": uuid.NewString(), "composite_score": 0.9}}})
	})
	e.GET("/api/v1/tasks", func(c echo.Context) error { return c.String(200, "not a memory read") })

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { l.Run(runCtx); close(done) }()

	missing := uuid.New()
	for _, path := range []string{
		"/api/v1/memories/search?q=Copper&workspace_id=" + ws.String() + "&project_id=" + project.String(),
		"/api/v1/memories/" + missing.String() + "?workspace_id=" + ws.String(),
		"/api/v1/memories/by-key?key=target-key&workspace_id=" + ws.String(),
		"/api/v1/projects/" + project.String() + "/updates?workspace_id=" + ws.String(),
		"/api/v1/tasks?workspace_id=" + ws.String(),
	} {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, http.NoBody))
	}
	// Run takes the queued reads off the request path into the pending batch.
	require.Eventually(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.queue) == 0 && len(l.pending) == 4
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	l.Flush(ctx)

	var rows []struct {
		Op      string     `db:"op"`
		Route   string     `db:"route"`
		Status  int        `db:"status_code"`
		Error   *string    `db:"error"`
		User    *uuid.UUID `db:"user_id"`
		Project *uuid.UUID `db:"project_id"`
		Caller  string     `db:"caller_kind"`
		Query   *string    `db:"query_text"`
		Results string     `db:"results"`
		Count   int        `db:"result_count"`
	}
	require.NoError(t, db.Select(&rows, `SELECT op,route,status_code,error,user_id,project_id,caller_kind,query_text,results::text,result_count
		FROM memory_read_events WHERE workspace_id=$1 ORDER BY route`, ws))
	require.Len(t, rows, 4, "the unjournaled route writes no row")
	byRoute := map[string]int{}
	for i, r := range rows {
		byRoute[r.Route] = i
		require.Equal(t, user, *r.User)
		require.Equal(t, domain.CallerKindUI, r.Caller)
	}
	search := rows[byRoute["GET /api/v1/memories/search"]]
	require.Equal(t, 200, search.Status)
	require.Equal(t, project, *search.Project)
	require.Equal(t, "Copper", *search.Query)
	require.Equal(t, 1, search.Count)
	require.Contains(t, search.Results, `"source": "project_knowledge"`)

	byID := rows[byRoute["GET /api/v1/memories/:id"]]
	require.Equal(t, http.StatusNotFound, byID.Status)
	require.NotNil(t, byID.Error)
	require.Contains(t, *byID.Error, "memory not found")
	require.Equal(t, missing.String(), *byID.Query, "get_memory by id journals the id it asked for")

	byKey := rows[byRoute["GET /api/v1/memories/by-key"]]
	require.Equal(t, http.StatusInternalServerError, byKey.Status)
	require.Equal(t, "store unavailable", *byKey.Error)
	require.Equal(t, "target-key", *byKey.Query)

	updates := rows[byRoute["GET /api/v1/projects/:proj_id/updates"]]
	require.Equal(t, project, *updates.Project)
	require.Equal(t, 1, updates.Count)
	require.Contains(t, updates.Results, `"source": "project_update"`)
	require.Contains(t, updates.Results, `"score": 0.9`)
}

func TestMemoryReadLogDB_Retention(t *testing.T) {
	db := memoryReadLogTestDB(t)
	ctx := context.Background()
	ws := uuid.New()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, ws) })
	sink := postgres.NewMemoryReadEventRepo(db)
	now := time.Now().UTC()
	text := "retention control"
	var events []domain.MemoryReadEvent
	for _, days := range []int{91, 31, 1} {
		events = append(events, domain.MemoryReadEvent{TS: now.Add(-time.Duration(days) * 24 * time.Hour), WorkspaceID: &ws,
			Op: "recall", Route: "GET /api/v1/memories/search", CallerKind: domain.CallerKindScript, QueryText: &text})
	}
	require.NoError(t, sink.InsertMemoryReadEvents(ctx, events))

	l := NewMemoryReadLog(sink, true)
	l.now = func() time.Time { return now }
	out := captureLog(t, func() { l.Purge(ctx) })
	require.Contains(t, out, "[memory-read-log] retention:")

	var rows []struct {
		TS    time.Time `db:"ts"`
		Query *string   `db:"query_text"`
	}
	require.NoError(t, db.Select(&rows, `SELECT ts,query_text FROM memory_read_events WHERE workspace_id=$1 ORDER BY ts`, ws))
	require.Len(t, rows, 2, "the 91-day row is deleted")
	require.Nil(t, rows[0].Query, "query_text of the 31-day row is cleared")
	require.NotNil(t, rows[1].Query, "a fresh row keeps its query_text")
	require.Equal(t, text, *rows[1].Query)

	// A second purge has nothing to do and says nothing.
	out = captureLog(t, func() { l.Purge(ctx) })
	require.NotContains(t, out, "retention:")
}

func TestMemoryReadLog_DisabledAndFailingSink(t *testing.T) {
	ctx := context.Background()
	var nilLog *MemoryReadLog
	require.False(t, nilLog.Enabled())
	require.False(t, NewMemoryReadLog(nil, true).Enabled(), "no sink is disabled")

	off := NewMemoryReadLog(closedSinkRepo(t), false)
	require.False(t, off.Enabled())
	done := make(chan struct{})
	go func() { off.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run of a disabled journal must return at once")
	}
	out := captureLog(t, func() {
		off.Flush(ctx)
		off.Purge(ctx)
	})
	require.Empty(t, out, "a disabled journal touches no sink")

	broken := NewMemoryReadLog(closedSinkRepo(t), true)
	broken.enqueue(rawMemoryRead{event: domain.MemoryReadEvent{Op: "recall", Route: "GET /api/v1/memories/search", StatusCode: 200}, body: []byte(`{"items":[]}`)})
	broken.dropped.Add(2)
	out = captureLog(t, func() {
		broken.Flush(ctx)
		broken.Purge(ctx)
	})
	require.Contains(t, out, "queue full: 2 reads not journaled")
	require.Contains(t, out, "flush of 1 reads failed")
	require.Contains(t, out, "retention purge failed")
	require.Empty(t, broken.pending, "a failed batch is not retried")
	require.Zero(t, broken.dropped.Load())

	// An empty flush is silent.
	out = captureLog(t, func() { broken.Flush(ctx) })
	require.Empty(t, out)
}

func TestToolBreakdownDailyDB_BookingAndFlush(t *testing.T) {
	db := memoryReadLogTestDB(t)
	ctx := context.Background()
	agent, ws, task, proj, recallProj := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM agent_tool_calls_daily WHERE agent_id=$1`, agent) })

	sessions := &fakeToolBreakdownSessionRepo{}
	tracker := NewToolBreakdownTracker(sessions)
	day := time.Date(2026, 11, 18, 23, 30, 0, 0, time.FixedZone("MSK", 3*3600))
	tracker.now = func() time.Time { return day }
	tracker.EnableDaily(postgres.NewMemoryReadEventRepo(db))

	e := echo.New()
	// mock: external boundary — identity normally supplied by the auth provider.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(ContextKeyAuthType, AuthTypeAgent)
			c.Set(ContextKeyAgentID, agent)
			c.Set(ContextKeyWorkspaceID, ws)
			return next(c)
		}
	})
	e.Use(tracker.Middleware())
	withComments := func(c echo.Context) error {
		if c.QueryParam("comments") == "1" {
			SetResponseCommentCount(c, 1)
		}
		return c.NoContent(200)
	}
	e.GET("/api/v1/tasks/:task_id", withComments)
	e.GET("/api/v1/tasks/:task_id/context", withComments)
	e.GET("/api/v1/tasks/:task_id/comments", withComments)
	e.GET("/api/v1/projects/:proj_id/knowledge", withComments)
	e.GET("/api/v1/memories/search", withComments)
	e.GET("/api/v1/not-a-tool", withComments)

	taskPath := "/api/v1/tasks/" + task.String()
	for _, tc := range []struct{ path, tool, caller string }{
		{taskPath, "", ""},                                  // get_task
		{taskPath + "?comments=1", "", ""},                  // get_task+comments
		{taskPath + "?comments=1", "get_task+comments", ""}, // primary request of a composite read: not booked
		{taskPath + "/comments", "get_task", ""},            // its comments request, empty: get_task
		{taskPath + "/comments?comments=1", "get_task", ""}, // its comments request with rows: get_task+comments
		{taskPath + "/context", "", ""},                     // get_task_context
		{taskPath + "/context?comments=1", "", ""},          // get_task_context+comments
		{taskPath + "/comments?comments=1", "", ""},         // a plain list_comments
		{"/api/v1/projects/" + proj.String() + "/knowledge", "", "agent_tool"},
		{"/api/v1/memories/search?project_id=" + recallProj.String(), "", "runner"},
		{"/api/v1/not-a-tool", "", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
		if tc.tool != "" {
			req.Header.Set(HeaderMeshTool, tc.tool)
		}
		if tc.caller != "" {
			req.Header.Set(HeaderMeshCaller, tc.caller)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, tc.path)
	}
	tracker.Flush(ctx)

	var rows []struct {
		Day     time.Time  `db:"day"`
		Tool    string     `db:"tool"`
		Calls   int64      `db:"calls"`
		Project *uuid.UUID `db:"project_id"`
		Caller  string     `db:"caller_kind"`
	}
	require.NoError(t, db.Select(&rows, `SELECT day,tool,calls,project_id,caller_kind FROM agent_tool_calls_daily WHERE agent_id=$1 AND workspace_id=$2`, agent, ws))
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Tool] += r.Calls
		require.Equal(t, "2026-11-18", r.Day.Format("2006-01-02"), "the day is the UTC day of the call")
		switch r.Tool {
		case "get_project_knowledge":
			require.Equal(t, proj, *r.Project)
			require.Equal(t, domain.CallerKindAgentTool, r.Caller)
		case "recall":
			require.Equal(t, recallProj, *r.Project)
			require.Equal(t, domain.CallerKindRunner, r.Caller)
		default:
			require.Nil(t, r.Project, "a task with no row resolves no project")
			require.Equal(t, domain.CallerKindUnknown, r.Caller)
		}
	}
	require.Equal(t, map[string]int64{
		"get_task":                  2,
		"get_task+comments":         2,
		"get_task_context":          1,
		"get_task_context+comments": 1,
		"list_comments":             1,
		"get_project_knowledge":     1,
		"recall":                    1,
		// An unmapped route keeps the route fallback of tool_breakdown.
		"route:GET /api/v1/not-a-tool": 1,
	}, got)
	// The agent-wide tool_breakdown keeps counting every request by route.
	require.NotEmpty(t, sessions.calls)
}

func TestToolBreakdownDaily_DisabledAndFailingSink(t *testing.T) {
	ctx := context.Background()
	agent, ws := uuid.New(), uuid.New()
	serve := func(tracker *ToolBreakdownTracker) {
		e := echo.New()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				c.Set(ContextKeyAuthType, AuthTypeAgent)
				c.Set(ContextKeyAgentID, agent)
				c.Set(ContextKeyWorkspaceID, ws)
				return next(c)
			}
		})
		e.Use(tracker.Middleware())
		e.GET("/api/v1/memories/search", func(c echo.Context) error { return c.NoContent(200) })
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/memories/search", http.NoBody))
	}

	off := NewToolBreakdownTracker(&fakeToolBreakdownSessionRepo{})
	serve(off)
	require.Empty(t, off.dailyDirty, "daily counters are off without a sink")

	broken := NewToolBreakdownTracker(&fakeToolBreakdownSessionRepo{})
	broken.EnableDaily(closedSinkRepo(t))
	serve(broken)
	require.Len(t, broken.dailyDirty, 1)
	out := captureLog(t, func() { broken.Flush(ctx) })
	require.True(t, strings.Contains(out, "daily counters flush failed (1 buckets)"), out)
	require.Empty(t, broken.dailyDirty, "a failed batch is dropped, not re-booked")

	broken.EnableDaily(nil)
	serve(broken)
	require.Empty(t, broken.dailyDirty, "EnableDaily(nil) switches the counters off again")
}
