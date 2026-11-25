package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// These controls traverse the real HTTP handlers, memory service, search SQL,
// async journal and PostgreSQL writer. No internal service/repository is mocked.
func TestMemoryReadLogDB_ServedRowsAndHeaders(t *testing.T) {
	db := reservedTagTestDB(t)
	ws := newReservedTagTestWorkspace(t, db, false)
	agent := newReservedTagTestAgent(t, db, ws)
	ctx := context.Background()
	repo := postgres.NewMemoryRepo(db)
	project := &domain.Project{ID: uuid.New(), WorkspaceID: ws, Name: "journal", Slug: "journal-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, project))
	var target uuid.UUID
	for _, tc := range []struct {
		key    string
		scope  domain.MemoryScope
		source domain.MemorySourceType
		status domain.MemoryStatus
	}{
		{"journal-workspace", domain.ScopeWorkspace, domain.SourceAgent, domain.MemoryStatusActive},
		{"journal-project", domain.ScopeProject, domain.SourceAgent, domain.MemoryStatusActive},
		{"journal-old", domain.ScopeWorkspace, domain.SourceAgent, domain.MemoryStatusSuperseded},
	} {
		m := &domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: tc.key, Content: "copperjournal answer", Scope: tc.scope, SourceType: tc.source, Relevance: 1, ImportanceScore: 1, Tags: []string{}}
		if m.Scope == domain.ScopeProject {
			m.ProjectID = &project.ID
		}
		require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
		_, err := db.Exec(`UPDATE memories SET status=$1 WHERE id=$2`, tc.status, m.ID)
		require.NoError(t, err)
		if tc.status == domain.MemoryStatusSuperseded {
			target = m.ID
		}
	}
	chunks := postgres.NewDocumentChunkRepo(db)
	doc := uuid.New()
	_, err := db.Exec(`INSERT INTO documents (id,project_id,slug,title,storage_key,created_by,created_by_type) VALUES ($1,$2,$3,'copperjournal','journal-test',$4,'agent')`, doc, project.ID, doc.String(), agent)
	require.NoError(t, err)
	require.NoError(t, chunks.ReplaceChunks(ctx, doc, project.ID, 1, []domain.DocumentChunk{{ChunkIdx: 0, Content: "copperjournal document answer"}}))
	_, err = db.Exec(`INSERT INTO project_members (id,project_id,workspace_id,agent_id) VALUES ($1,$2,$3,$4)`, uuid.New(), project.ID, ws, agent)
	require.NoError(t, err)
	sink := postgres.NewMemoryReadEventRepo(db)
	journal := mw.NewMemoryReadLog(sink, true)
	h := NewMemoryHandler(service.NewMemoryService(repo, postgres.NewMemoryEdgesRepo(db), nil, service.MemoryWithDocIndex(chunks, true)), postgres.NewWorkspaceMemberRepo(db))
	updates := postgres.NewProjectUpdateRepo(db)
	update := &domain.ProjectUpdate{ID: uuid.New(), ProjectID: project.ID, Title: "journal control", Status: domain.UpdateStatusOnTrack, Summary: "project update control", CreatedBy: agent,
		Highlights: json.RawMessage(`[]`), Blockers: json.RawMessage(`[]`), NextSteps: json.RawMessage(`[]`), Metrics: json.RawMessage(`{}`)}
	require.NoError(t, updates.Create(ctx, update))
	updateHandler := NewProjectUpdateHandler(service.NewProjectUpdateService(updates, postgres.NewProjectRepo(db), postgres.NewTaskRepo(db), postgres.NewTaskStatusRepo(db)))
	e := echo.New()
	// mock: external boundary — identity normally supplied by the auth provider.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, agent, ws); return next(c) }
	})
	e.Use(journal.Middleware())
	e.GET("/api/v1/memories/search", h.Search)
	e.GET("/api/v1/memories/recall-final/v1", h.FinalRecall)
	e.GET("/api/v1/memories/recall_graph", h.RecallGraph)
	e.GET("/api/v1/memories/by-key", h.ByKey)
	e.GET("/api/v1/memories/:id", h.GetByID)
	e.GET("/api/v1/projects/:proj_id/knowledge", h.GetProjectKnowledge)
	e.GET("/api/v1/projects/:proj_id/updates", updateHandler.List)
	e.GET("/api/v1/projects/:proj_id/updates/latest", updateHandler.GetLatest)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, ws) })
	session := uuid.NewString()
	task := uuid.NewString()
	for _, tc := range []struct {
		path, op        string
		headers, source bool
	}{
		{"/api/v1/memories/search?q=copperjournal&exclude_superseded=false&limit=20", "recall", false, true},
		{"/api/v1/memories/recall-final/v1?q=copperjournal&exclude_superseded=false&limit=20", "recall-final", true, true},
		{"/api/v1/memories/recall_graph?query=copperjournal&limit=20&project_id=" + project.ID.String(), "recall_graph", true, true},
		{"/api/v1/memories/by-key?key=journal-old", "get_memory", true, false},
		{"/api/v1/memories/" + target.String(), "get_memory", true, false},
		{"/api/v1/projects/" + project.ID.String() + "/knowledge", "get_project_knowledge", true, false},
		{"/api/v1/projects/" + project.ID.String() + "/updates", "get_project_updates", true, false},
		{"/api/v1/projects/" + project.ID.String() + "/updates/latest", "get_project_updates", true, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Cleanup(func() { journal.Flush(ctx) })
			var before int
			require.NoError(t, db.Get(&before, `SELECT count(*) FROM memory_read_events WHERE workspace_id=$1`, ws))
			req := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
			req.Header.Set("User-Agent", "journal-db-control")
			if tc.headers {
				req.Header.Set(mw.HeaderMeshCaller, "agent_tool")
				req.Header.Set(mw.HeaderMeshSessionID, session)
				req.Header.Set(mw.HeaderMeshTaskID, task)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var served struct {
				Items []struct {
					Source string    `json:"source"`
					ID     uuid.UUID `json:"id"`
				} `json:"items"`
				ID        uuid.UUID         `json:"id"`
				Workspace []json.RawMessage `json:"workspace_memories"`
				Project   []json.RawMessage `json:"project_memories"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &served))
			count := len(served.Items)
			if served.ID != uuid.Nil {
				count = 1
			}
			if tc.op == "get_project_knowledge" {
				count = len(served.Workspace) + len(served.Project)
			}
			require.Positive(t, count, "positive control: search must actually return rows")
			if tc.source {
				sources := map[string]bool{}
				for _, m := range served.Items {
					require.NotEmpty(t, m.Source, "served recall rows identify their source")
					require.Contains(t, []string{"memory", "project_knowledge", "doc"}, m.Source)
					sources[m.Source] = true
				}
				if tc.op == "recall" {
					require.Len(t, sources, 3, "positive control includes real memory, project knowledge and document chunk")
				}
			}
			var immediate int
			require.NoError(t, db.Get(&immediate, `SELECT count(*) FROM memory_read_events WHERE workspace_id=$1`, ws))
			require.Equal(t, before, immediate, "hot path never writes synchronously")
			journal.Flush(ctx)
			var row struct {
				Count      int     `db:"result_count"`
				Superseded int     `db:"superseded_count"`
				Caller     string  `db:"caller_kind"`
				Route      string  `db:"route"`
				UA         string  `db:"user_agent"`
				Session    *string `db:"session_id"`
				Task       *string `db:"task_id"`
				Results    []byte  `db:"results"`
				Op         string  `db:"op"`
			}
			require.NoError(t, db.Get(&row, `SELECT result_count,superseded_count,caller_kind,route,user_agent,session_id,task_id::text,results,op FROM memory_read_events WHERE workspace_id=$1 ORDER BY ts DESC LIMIT 1`, ws))
			var after int
			require.NoError(t, db.Get(&after, `SELECT count(*) FROM memory_read_events WHERE workspace_id=$1`, ws))
			require.Equal(t, before+1, after)
			require.Equal(t, count, row.Count)
			require.Equal(t, tc.op, row.Op)
			require.NotEmpty(t, row.Route)
			require.Equal(t, "journal-db-control", row.UA)
			if tc.headers {
				require.Equal(t, "agent_tool", row.Caller)
				require.Equal(t, session, *row.Session)
				require.Equal(t, task, *row.Task)
			} else {
				require.Equal(t, "unknown", row.Caller)
				require.Nil(t, row.Session)
				require.Nil(t, row.Task)
			}
			var results []domain.MemoryReadResult
			require.NoError(t, json.Unmarshal(row.Results, &results))
			require.Len(t, results, count)
			for i, r := range results {
				require.NotEmpty(t, r.Source)
				require.Equal(t, i+1, r.Rank)
				require.NotEqual(t, uuid.Nil, r.ID)
				if tc.source {
					require.Equal(t, served.Items[i].ID, r.ID, "journal contains the served rows in order")
					require.Equal(t, served.Items[i].Source, r.Source)
				}
				if tc.op == "get_project_updates" {
					require.Equal(t, update.ID, r.ID)
					require.Equal(t, domain.ReadSourceProjectUpdate, r.Source)
				}
			}
			if tc.op == "recall-final" || tc.path == "/api/v1/memories/"+target.String() {
				require.Equal(t, 1, row.Superseded)
			}
		})
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/v1/memories/search?q=unfindablejournalquery", http.StatusOK},
		{"/api/v1/memories/" + uuid.NewString(), http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, http.NoBody))
		require.Equal(t, tc.code, rec.Code, rec.Body.String())
		journal.Flush(ctx)
		var row struct {
			Count  int     `db:"result_count"`
			Status int     `db:"status_code"`
			Error  *string `db:"error"`
		}
		require.NoError(t, db.Get(&row, `SELECT result_count,status_code,error FROM memory_read_events WHERE workspace_id=$1 ORDER BY ts DESC LIMIT 1`, ws))
		require.Zero(t, row.Count)
		require.Equal(t, tc.code, row.Status)
		if tc.code != http.StatusOK {
			require.NotEmpty(t, *row.Error)
		}
	}
	// Disabling the flag leaves the served response unchanged and writes nothing.
	off := mw.NewMemoryReadLog(sink, false)
	e2 := echo.New()
	e2.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, agent, ws); return next(c) }
	})
	e2.Use(off.Middleware())
	e2.GET("/api/v1/memories/search", h.Search)
	path := "/api/v1/memories/search?" + url.Values{"q": {"copperjournal"}}.Encode()
	onResponse, offResponse := httptest.NewRecorder(), httptest.NewRecorder()
	e.ServeHTTP(onResponse, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	journal.Flush(ctx)
	var before int
	require.NoError(t, db.Get(&before, `SELECT count(*) FROM memory_read_events WHERE workspace_id=$1`, ws))
	e2.ServeHTTP(offResponse, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	off.Flush(ctx)
	var after int
	require.NoError(t, db.Get(&after, `SELECT count(*) FROM memory_read_events WHERE workspace_id=$1`, ws))
	require.Equal(t, before, after)
	// Recall asynchronously touches access/update timestamps independently of
	// logging. Compare all served fields except those timestamps, which can
	// legitimately change between the two consecutive reads.
	var onBody, offBody map[string]any
	require.Equal(t, http.StatusOK, onResponse.Code)
	require.Equal(t, http.StatusOK, offResponse.Code)
	require.NoError(t, json.Unmarshal(onResponse.Body.Bytes(), &onBody))
	require.NoError(t, json.Unmarshal(offResponse.Body.Bytes(), &offBody))
	for _, body := range []map[string]any{onBody, offBody} {
		for _, row := range body["items"].([]any) {
			delete(row.(map[string]any), "updated_at")
			delete(row.(map[string]any), "last_accessed_at")
		}
	}
	require.Equal(t, onBody, offBody)
}

func TestMemoryReadLogDB_DailyTaskComments(t *testing.T) {
	f := finishSourceSetup(t)
	ctx := context.Background()
	taskID := f.task(t)
	var ws uuid.UUID
	require.NoError(t, f.db.Get(&ws, `SELECT workspace_id FROM projects WHERE id=$1`, f.project))
	sink := postgres.NewMemoryReadEventRepo(f.db)
	tracker := mw.NewToolBreakdownTracker(postgres.NewSessionRepo(f.db))
	tracker.EnableDaily(sink)
	taskRepo := postgres.NewTaskRepo(f.db)
	activity := postgres.NewActivityLogRepo(f.db)
	comments := service.NewCommentService(postgres.NewCommentRepo(f.db), taskRepo, activity)
	contextHandler := NewTaskContextHandler(f.svc, comments,
		service.NewArtifactService(postgres.NewArtifactRepo(f.db), nil, activity),
		service.NewTaskDependencyService(postgres.NewTaskDependencyRepo(f.db), taskRepo, activity, postgres.NewProjectRepo(f.db)),
		service.NewEventBusService(postgres.NewEventBusMessageRepo(f.db), activity), nil)
	e := echo.New()
	// mock: external boundary — identity normally supplied by the auth provider.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, f.agent, ws); return next(c) }
	})
	e.Use(tracker.Middleware())
	e.GET("/api/v1/tasks/:task_id", NewTaskHandler(f.svc).GetByID)
	e.GET("/api/v1/tasks/:task_id/comments", NewCommentHandler(comments, f.svc).List)
	e.GET("/api/v1/tasks/:task_id/context", contextHandler.GetTaskContext)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM agent_tool_calls_daily WHERE agent_id=$1`, f.agent) })
	request := func(suffix, tool string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+taskID.String()+suffix, http.NoBody)
		req.Header.Set(mw.HeaderMeshCaller, "agent_tool")
		req.Header.Set(mw.HeaderMeshTool, tool)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		return rec
	}
	request("", "")         // a plain get_task
	request("/context", "") // no comments -> get_task_context
	// Even a flush between a composite read's two HTTP requests cannot create
	// a negative bucket or double-count the logical tool call.
	request("", "get_task+comments")
	tracker.Flush(ctx)
	request("/comments", "get_task") // empty -> one get_task
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, AuthorID: f.agent, AuthorType: domain.ActorTypeAgent, Body: "daily comment control"}
	require.NoError(t, postgres.NewCommentRepo(f.db).Create(ctx, comment))
	request("", "get_task+comments")
	tracker.Flush(ctx)
	rec := request("/comments", "get_task")
	require.Contains(t, rec.Body.String(), "daily comment control")
	request("/context", "")
	request("/comments", "") // ordinary list_comments stays separate
	tracker.Flush(ctx)
	type bucket struct {
		Tool    string    `db:"tool"`
		Calls   int64     `db:"calls"`
		Project uuid.UUID `db:"project_id"`
		Caller  string    `db:"caller_kind"`
	}
	var rows []bucket
	require.NoError(t, f.db.Select(&rows, `SELECT tool,calls,project_id,caller_kind FROM agent_tool_calls_daily WHERE agent_id=$1`, f.agent))
	got := map[string]int64{}
	for _, r := range rows {
		require.Equal(t, f.project, r.Project)
		require.Equal(t, "agent_tool", r.Caller)
		got[r.Tool] = r.Calls
	}
	require.Equal(t, map[string]int64{"get_task": 2, "get_task+comments": 1, "get_task_context": 1, "get_task_context+comments": 1, "list_comments": 1}, got)
	t.Run("cached context still counts served comments", func(t *testing.T) {
		redisURL := os.Getenv("REDIS_URL")
		if redisURL == "" {
			t.Skip("REDIS_URL required for the real cache control")
		}
		opts, err := redis.ParseURL(redisURL)
		require.NoError(t, err)
		client := redis.NewClient(opts)
		t.Cleanup(func() { _ = client.Close() })
		require.NoError(t, client.Ping(ctx).Err())
		cache := service.NewContextCacheService(client)
		contextHandler.cache = cache
		t.Cleanup(func() { cache.Invalidate(ctx, taskID) })
		miss := request("/context", "")
		cached, ok := cache.Get(ctx, taskID)
		require.True(t, ok, "positive control: handler populated real Redis")
		require.JSONEq(t, miss.Body.String(), string(cached))
		hit := request("/context", "")
		require.JSONEq(t, miss.Body.String(), hit.Body.String())
		tracker.Flush(ctx)
		var calls int64
		require.NoError(t, f.db.Get(&calls, `SELECT sum(calls) FROM agent_tool_calls_daily WHERE agent_id=$1 AND tool='get_task_context+comments'`, f.agent))
		require.Equal(t, int64(3), calls, "miss and cache hit each add one call with comments")
	})
	// MEMORY_READ_LOG=false disables only the new daily sink. The legacy
	// session counter still records the same ordinary get_task request.
	var dailyBefore, legacyBefore int64
	require.NoError(t, f.db.Get(&dailyBefore, `SELECT sum(calls) FROM agent_tool_calls_daily WHERE agent_id=$1`, f.agent))
	require.NoError(t, f.db.Get(&legacyBefore, `SELECT sum(COALESCE((tool_breakdown->>'get_task')::bigint,0)) FROM agent_sessions WHERE agent_id=$1`, f.agent))
	tracker.EnableDaily(nil)
	request("", "")
	tracker.Flush(ctx)
	var dailyAfter, legacyAfter int64
	require.NoError(t, f.db.Get(&dailyAfter, `SELECT sum(calls) FROM agent_tool_calls_daily WHERE agent_id=$1`, f.agent))
	require.NoError(t, f.db.Get(&legacyAfter, `SELECT sum(COALESCE((tool_breakdown->>'get_task')::bigint,0)) FROM agent_sessions WHERE agent_id=$1`, f.agent))
	require.Equal(t, dailyBefore, dailyAfter)
	require.Equal(t, legacyBefore+1, legacyAfter)
}
