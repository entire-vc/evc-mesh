package sdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAgent returns the JSON body the server returns for GET /api/v1/agents/me.
func fakeAgent(id, wsID string) map[string]any {
	return map[string]any{
		"id":           id,
		"workspace_id": wsID,
		"name":         "test-agent",
		"slug":         "test-agent",
		"agent_type":   "custom",
		"status":       "online",
		"created_at":   "2026-01-01T00:00:00Z",
		"updated_at":   "2026-01-01T00:00:00Z",
	}
}

// testServer spins up an httptest.Server that delegates to a simple router map.
// routes maps "METHOD /path" -> handler func. All unmatched requests return 404.
type testServer struct {
	*httptest.Server
	routes map[string]http.HandlerFunc
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{
		routes: make(map[string]http.HandlerFunc),
	}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if h, ok := ts.routes[key]; ok {
			h(w, r)
			return
		}
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(ts.Server.Close)
	return ts
}

// respond writes a JSON body with the given status code.
func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// -------------------------------------------------------------------
// New() / Me()
// -------------------------------------------------------------------

func TestNew_DiscoverAgentInfo(t *testing.T) {
	const agentID = "aaaa-bbbb-cccc-dddd"
	const wsID = "ws-0000-1111-2222"

	srv := newTestServer(t)
	srv.routes["GET /api/v1/agents/me"] = func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "agk_test_key", r.Header.Get("X-Agent-Key"))
		respond(w, http.StatusOK, fakeAgent(agentID, wsID))
	}

	client, err := New(srv.URL, "agk_test_key")
	require.NoError(t, err)
	assert.Equal(t, agentID, client.AgentID())
	assert.Equal(t, wsID, client.WorkspaceID())
}

func TestNew_AuthFailure(t *testing.T) {
	srv := newTestServer(t)
	srv.routes["GET /api/v1/agents/me"] = func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusUnauthorized, map[string]string{"message": "invalid agent key"})
	}

	_, err := New(srv.URL, "agk_bad_key")
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

// -------------------------------------------------------------------
// Tasks
// -------------------------------------------------------------------

func newAuthedClient(t *testing.T, srv *testServer, agentID, wsID string) *Client {
	t.Helper()
	srv.routes["GET /api/v1/agents/me"] = func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, fakeAgent(agentID, wsID))
	}
	client, err := New(srv.URL, "agk_test")
	require.NoError(t, err)
	return client
}

func TestCreateTask(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const projID = "proj-abc"
	srv.routes["POST /api/v1/projects/"+projID+"/tasks"] = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "Fix the bug", body["title"])

		respond(w, http.StatusCreated, map[string]any{
			"id":         "task-123",
			"project_id": projID,
			"title":      "Fix the bug",
			"priority":   "high",
			"status_id":  "status-1",
			"created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-01T00:00:00Z",
		})
	}

	task, err := client.CreateTask(t.Context(), projID, CreateTaskInput{
		Title:    "Fix the bug",
		Priority: "high",
	})
	require.NoError(t, err)
	assert.Equal(t, "task-123", task.ID)
	assert.Equal(t, "Fix the bug", task.Title)
	assert.Equal(t, "high", task.Priority)
}

func TestGetTask(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const taskID = "task-xyz"
	srv.routes["GET /api/v1/tasks/"+taskID] = func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{
			"id":         taskID,
			"project_id": "proj-1",
			"title":      "My task",
			"priority":   "medium",
			"status_id":  "status-1",
			"created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-01T00:00:00Z",
		})
	}

	task, err := client.GetTask(t.Context(), taskID)
	require.NoError(t, err)
	assert.Equal(t, taskID, task.ID)
	assert.Equal(t, "My task", task.Title)
}

func TestGetTask_NotFound(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	// No route registered for this task ID — test server returns 404.
	_, err := client.GetTask(t.Context(), "does-not-exist")
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
}

func TestMoveTask(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const taskID = "task-move"
	const newStatusID = "status-done"

	moveCalled := false
	srv.routes["POST /api/v1/tasks/"+taskID+"/move"] = func(w http.ResponseWriter, r *http.Request) {
		moveCalled = true
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, newStatusID, body["status_id"])
		// Server returns 200 {"status":"ok"} for move
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	}

	// MoveTask internally fetches the task after moving it.
	srv.routes["GET /api/v1/tasks/"+taskID] = func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{
			"id":         taskID,
			"project_id": "proj-1",
			"title":      "Moved task",
			"priority":   "medium",
			"status_id":  newStatusID,
			"created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-01T00:00:00Z",
		})
	}

	task, err := client.MoveTask(t.Context(), taskID, newStatusID)
	require.NoError(t, err)
	assert.True(t, moveCalled, "expected POST /move to be called")
	assert.Equal(t, newStatusID, task.StatusID)
}

func TestListTasks(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const projID = "proj-list"
	srv.routes["GET /api/v1/projects/"+projID+"/tasks"] = func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "high", r.URL.Query().Get("priority"))
		respond(w, http.StatusOK, map[string]any{
			"items": []map[string]any{
				{
					"id":         "task-1",
					"project_id": projID,
					"title":      "Task one",
					"priority":   "high",
					"status_id":  "status-1",
					"created_at": "2026-01-01T00:00:00Z",
					"updated_at": "2026-01-01T00:00:00Z",
				},
			},
			"total_count": 1,
			"page":        1,
			"page_size":   50,
			"total_pages": 1,
			"has_more":    false,
		})
	}

	tasks, err := client.ListTasks(t.Context(), projID, WithPriority("high"))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "task-1", tasks[0].ID)
}

// -------------------------------------------------------------------
// Comments
// -------------------------------------------------------------------

func TestAddComment(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const taskID = "task-comment"
	srv.routes["POST /api/v1/tasks/"+taskID+"/comments"] = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "Progress update", body["body"])
		assert.Equal(t, true, body["is_internal"])
		respond(w, http.StatusCreated, map[string]any{
			"id":          "comment-1",
			"task_id":     taskID,
			"author_id":   "agent-1",
			"author_type": "agent",
			"body":        "Progress update",
			"is_internal": true,
			"created_at":  "2026-01-01T00:00:00Z",
			"updated_at":  "2026-01-01T00:00:00Z",
		})
	}

	comment, err := client.AddComment(t.Context(), taskID, "Progress update", true)
	require.NoError(t, err)
	assert.Equal(t, "comment-1", comment.ID)
	assert.True(t, comment.IsInternal)
}

// -------------------------------------------------------------------
// Events
// -------------------------------------------------------------------

func TestPublishEvent(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const projID = "proj-events"
	srv.routes["POST /api/v1/projects/"+projID+"/events"] = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "summary", body["event_type"])
		assert.Equal(t, "Sprint review complete", body["subject"])
		respond(w, http.StatusCreated, map[string]any{
			"id":          "event-1",
			"workspace_id": "ws-1",
			"project_id":  projID,
			"event_type":  "summary",
			"subject":     "Sprint review complete",
			"created_at":  "2026-01-01T00:00:00Z",
		})
	}

	event, err := client.PublishEvent(t.Context(), projID, PublishEventInput{
		EventType: "summary",
		Subject:   "Sprint review complete",
		Payload:   map[string]any{"tasks_done": 5},
	})
	require.NoError(t, err)
	assert.Equal(t, "event-1", event.ID)
	assert.Equal(t, "summary", event.EventType)
}

func TestGetContext(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	const projID = "proj-ctx"
	srv.routes["GET /api/v1/projects/"+projID+"/events"] = func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "summary", r.URL.Query().Get("event_type"))
		respond(w, http.StatusOK, map[string]any{
			"items": []map[string]any{
				{
					"id":          "ev-1",
					"workspace_id": "ws-1",
					"project_id":  projID,
					"event_type":  "summary",
					"subject":     "Done",
					"created_at":  "2026-01-01T00:00:00Z",
				},
			},
			"total_count": 1,
			"page":        1,
			"page_size":   50,
			"total_pages": 1,
			"has_more":    false,
		})
	}

	events, err := client.GetContext(t.Context(), projID, WithEventType("summary"))
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "ev-1", events[0].ID)
}

// -------------------------------------------------------------------
// Agents
// -------------------------------------------------------------------

func TestHeartbeat(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	called := false
	srv.routes["POST /api/v1/agents/heartbeat"] = func(w http.ResponseWriter, r *http.Request) {
		called = true
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	}

	require.NoError(t, client.Heartbeat(t.Context()))
	assert.True(t, called)
}

func TestRegisterSubAgent(t *testing.T) {
	const parentID = "agent-parent"
	const wsID = "ws-reg"
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, parentID, wsID)

	srv.routes["POST /api/v1/workspaces/"+wsID+"/agents"] = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "sub-agent-1", body["name"])
		assert.Equal(t, parentID, body["parent_agent_id"])
		respond(w, http.StatusCreated, map[string]any{
			"id":              "sub-1",
			"workspace_id":    wsID,
			"parent_agent_id": parentID,
			"name":            "sub-agent-1",
			"slug":            "sub-agent-1",
			"agent_type":      "custom",
			"status":          "offline",
			"api_key":         "agk_ws_generatedkey",
			"created_at":      "2026-01-01T00:00:00Z",
			"updated_at":      "2026-01-01T00:00:00Z",
		})
	}

	out, err := client.RegisterSubAgent(t.Context(), RegisterSubAgentInput{
		Name:      "sub-agent-1",
		AgentType: "custom",
	})
	require.NoError(t, err)
	assert.Equal(t, "sub-1", out.ID)
	assert.Equal(t, "agk_ws_generatedkey", out.APIKey)
}

func TestListSubAgents(t *testing.T) {
	const agentID = "agent-parent"
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, agentID, "ws-1")

	srv.routes["GET /api/v1/agents/"+agentID+"/sub-agents"] = func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "true", r.URL.Query().Get("recursive"))
		respond(w, http.StatusOK, map[string]any{
			"agents": []map[string]any{
				{
					"id":           "child-1",
					"workspace_id": "ws-1",
					"name":         "child agent",
					"status":       "online",
					"created_at":   "2026-01-01T00:00:00Z",
					"updated_at":   "2026-01-01T00:00:00Z",
				},
			},
			"count": 1,
		})
	}

	agents, err := client.ListSubAgents(t.Context(), agentID, true)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "child-1", agents[0].ID)
}

// -------------------------------------------------------------------
// Error handling
// -------------------------------------------------------------------

func TestErrorHandling_5xx(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	srv.routes["GET /api/v1/tasks/bad-task"] = func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusInternalServerError, map[string]string{"message": "database is down"})
	}

	_, err := client.GetTask(t.Context(), "bad-task")
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
	assert.Contains(t, apiErr.Message, "database is down")
}

func TestErrorHandling_ValidationError(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	srv.routes["POST /api/v1/projects/proj-1/tasks"] = func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusBadRequest, map[string]any{
			"message":    "validation failed",
			"validation": map[string]string{"title": "title is required"},
		})
	}

	_, err := client.CreateTask(t.Context(), "proj-1", CreateTaskInput{})
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, "title is required", apiErr.Validation["title"])
}

func TestErrorIs_Sentinel(t *testing.T) {
	srv := newTestServer(t)
	client := newAuthedClient(t, srv, "agent-1", "ws-1")

	// No route for this task → 404 from test server.
	_, err := client.GetTask(t.Context(), "ghost")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}
