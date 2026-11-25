package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestMemoryReadDecodeShapes(t *testing.T) {
	id := uuid.NewString()
	row := `{"id":"` + id + `","key":"target","source":"doc","score":0.25,"status":"superseded","tags":["kind:pinned"]}`
	for _, tc := range []struct {
		name, op, route, body string
		count                 int
	}{
		{"recall", "recall", "", `{"items":[` + row + `],"search_mode":"hybrid"}`, 1},
		{"final", "recall-final", "", `{"items":[` + row + `]}`, 1},
		{"graph", "recall_graph", "", `{"items":[` + row + `]}`, 1},
		{"key", "get_memory", "GET /api/v1/memories/by-key", `{"items":[` + row + `]}`, 1},
		{"id", "get_memory", "GET /api/v1/memories/:id", row, 1},
		{"knowledge", "get_project_knowledge", "", `{"project_memories":[` + row + `],"workspace_memories":[` + row + `]}`, 2},
		{"updates", "get_project_updates", "", `{"items":[` + row + `]}`, 1},
		{"latest", "get_project_updates", "GET /api/v1/projects/:proj_id/updates/latest", row, 1},
		{"empty", "recall", "", `{"items":[]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := domain.MemoryReadEvent{Op: tc.op, Route: tc.route, StatusCode: 200}
			parseMemoryReadResponse(&ev, []byte(tc.body), false)
			require.Nil(t, ev.Error)
			require.Equal(t, tc.count, ev.ResultCount)
			require.Len(t, ev.Results, tc.count)
			require.Equal(t, tc.count, ev.SupersededCount)
			require.Equal(t, tc.count, ev.PinnedCount)
			for i, r := range ev.Results {
				require.Equal(t, i+1, r.Rank)
				require.Equal(t, "doc", r.Source)
				require.Equal(t, id, r.ID.String())
				require.Equal(t, 0.25, *r.Score)
			}
		})
	}
}

func TestMemoryReadSourcesAndErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"items":[{"scope":"workspace"}]}`, "memory"},
		{`{"items":[{"scope":"project"}]}`, "project_knowledge"},
		{`{"items":[{"scope":"project","source_type":"doc"}]}`, "doc"},
	} {
		ev := domain.MemoryReadEvent{Op: "recall", StatusCode: 200}
		parseMemoryReadResponse(&ev, []byte(tc.body), false)
		require.Equal(t, tc.want, ev.Results[0].Source)
	}
	for _, tc := range []struct {
		status    int
		body      string
		truncated bool
	}{
		{403, `{"error":"forbidden"}`, false},
		{200, `{`, false},
		{200, `{"items":[]}`, true},
	} {
		ev := domain.MemoryReadEvent{Op: "recall", StatusCode: tc.status}
		parseMemoryReadResponse(&ev, []byte(tc.body), tc.truncated)
		require.NotNil(t, ev.Error)
		require.Zero(t, ev.ResultCount)
		require.Empty(t, ev.Results)
	}
}

func TestMemoryReadRequestFacts(t *testing.T) {
	e := echo.New()
	task, project, ws, agent := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=Hello&project_id="+project.String(), http.NoBody)
	req.Header.Set(HeaderMeshCaller, "agent_tool")
	req.Header.Set(HeaderMeshSessionID, "session-1")
	req.Header.Set(HeaderMeshTaskID, task.String())
	req.Header.Set("User-Agent", "memory-control")
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agent)
	c.Set(ContextKeyWorkspaceID, ws)
	ev := memoryReadRequestFacts(c, "recall", "GET /api/v1/memories/search")
	require.Equal(t, "agent_tool", ev.CallerKind)
	require.Equal(t, task, *ev.TaskID)
	require.Equal(t, project, *ev.ProjectID)
	require.Equal(t, ws, *ev.WorkspaceID)
	require.Equal(t, agent, *ev.AgentID)
	require.Equal(t, "session-1", *ev.SessionID)
	require.Equal(t, "memory-control", ev.UserAgent)
	require.Equal(t, "Hello", *ev.QueryText)
	require.Len(t, *ev.QueryHash, 64)
	for _, tc := range []struct{ auth, header, want string }{
		{AuthTypeAgent, "", "unknown"}, {AuthTypeUser, "", "ui"},
		{AuthTypeUser, "script", "script"}, {AuthTypeAgent, "invalid", "unknown"},
		{AuthTypeAgent, "runner", "runner"},
	} {
		c.Set(ContextKeyAuthType, tc.auth)
		req.Header.Set(HeaderMeshCaller, tc.header)
		require.Equal(t, tc.want, CallerKindOf(c))
	}
	req.Header.Set(HeaderMeshTaskID, "bad")
	req.Header.Del(HeaderMeshSessionID)
	ev = memoryReadRequestFacts(c, "recall", "GET /api/v1/memories/search")
	require.Nil(t, ev.TaskID)
	require.Nil(t, ev.SessionID)
	require.NotEmpty(t, ev.Route)
	c = e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/memories/recall_graph?query=Graph+Question", http.NoBody), httptest.NewRecorder())
	ev = memoryReadRequestFacts(c, "recall_graph", "GET /api/v1/memories/recall_graph")
	require.NotNil(t, ev.QueryText)
	require.Equal(t, "Graph Question", *ev.QueryText)
	require.Len(t, *ev.QueryHash, 64)
}

func TestMemoryReadMiddlewareNonBlockingBoundedAndDisabled(t *testing.T) {
	// Exercise the queue directly; no persistence boundary is replaced.
	l := &MemoryReadLog{enabled: true, queue: make(chan rawMemoryRead, 1), now: time.Now}
	e := echo.New()
	e.Use(l.Middleware())
	e.GET("/api/v1/memories/search", func(c echo.Context) error { return c.JSON(200, map[string]any{"items": []any{}}) })
	for i := 0; i < 3; i++ {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/memories/search", http.NoBody))
		require.Equal(t, 200, r.Code)
		require.JSONEq(t, `{"items":[]}`, r.Body.String())
	}
	require.Len(t, l.queue, 1)
	require.Equal(t, int64(2), l.dropped.Load())
	raw := <-l.queue
	require.NotEmpty(t, raw.event.Route)
	require.Equal(t, "unknown", raw.event.CallerKind)
	require.Equal(t, len(raw.body), raw.event.ResponseChars)
	l.enabled = false
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/memories/search", http.NoBody))
	require.Empty(t, l.queue)
	l.enabled = true
	l.pending = make([]domain.MemoryReadEvent, memoryReadQueueSize)
	l.accept(raw)
	require.Len(t, l.pending, memoryReadQueueSize)
	require.Equal(t, int64(3), l.dropped.Load())
}

func TestMemoryReadTeeAndUnicode(t *testing.T) {
	out := httptest.NewRecorder()
	tee := &teeResponseWriter{ResponseWriter: out, limit: 5}
	_, err := tee.Write([]byte("hello"))
	require.NoError(t, err)
	_, err = tee.Write([]byte(" world"))
	require.NoError(t, err)
	require.True(t, tee.truncated)
	require.Equal(t, 11, tee.total)
	require.Equal(t, "hello world", out.Body.String())
	require.Equal(t, "hello", tee.buf.String())
	require.True(t, utf8.ValidString(truncateText(strings.Repeat("я", 20), 7)))
}

func TestMemoryReadTeeFullBufferFlushUnwrap(t *testing.T) {
	out := httptest.NewRecorder()
	tee := &teeResponseWriter{ResponseWriter: out, limit: 3}
	_, err := tee.Write([]byte("abc"))
	require.NoError(t, err)
	require.False(t, tee.truncated, "exactly the limit is not truncated")
	_, err = tee.Write([]byte("d"))
	require.NoError(t, err)
	require.True(t, tee.truncated, "a write after a full buffer marks the capture truncated")
	require.Equal(t, "abc", tee.buf.String())
	require.Equal(t, 4, tee.total)
	tee.Flush()
	require.True(t, out.Flushed, "Flush reaches the underlying writer")
	require.Same(t, out, tee.Unwrap())
	// A writer without http.Flusher is left alone.
	(&teeResponseWriter{ResponseWriter: struct{ http.ResponseWriter }{out}}).Flush()
}

func TestMemoryReadDecodeErrorsBySingleAndKnowledgeShape(t *testing.T) {
	for _, tc := range []struct{ op, route string }{
		{"get_memory", "GET /api/v1/memories/:id"},
		{"get_project_updates", "GET /api/v1/projects/:proj_id/updates/latest"},
		{"get_project_knowledge", "GET /api/v1/projects/:proj_id/knowledge"},
	} {
		ev := domain.MemoryReadEvent{Op: tc.op, Route: tc.route, StatusCode: 200}
		parseMemoryReadResponse(&ev, []byte(`[`), false)
		require.NotNil(t, ev.Error, tc.route)
		require.Contains(t, *ev.Error, "unreadable response")
		require.Zero(t, ev.ResultCount)
	}
}
