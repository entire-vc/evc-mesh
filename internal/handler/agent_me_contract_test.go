package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Contract tests for the public agent API, /api/v1/agents/me/*.
//
// External runners (entire-vc/evc-mesh-runner, manifest mesh-extension.json,
// `mesh_api: ">=1 <2"`) depend on these routes: the runner reads
// src/mesh_runner/mesh.py::{me, my_tasks, session_report} and the SSE stream.
// Each test pins the part of the wire format a client parses — key names, status
// codes, event framing — not the handler's internals. A failure here is a breaking
// change for partners: see docs/extensions/agents-me-contract.md for the policy.
//
// Run all of them with: go test ./internal/handler -run Contract

// contractKeys returns the top-level keys of a JSON object response.
func contractKeys(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "response must be a JSON object: %s", body)
	return m
}

func requireContractKeys(t *testing.T, got map[string]json.RawMessage, want ...string) {
	t.Helper()
	for _, k := range want {
		assert.Contains(t, got, k, "contract key %q missing from response", k)
	}
}

func contractCtx(method, target, body string, agentID, wsID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if agentID != uuid.Nil {
		c.Set(mw.ContextKeyAgentID, agentID)
	}
	if wsID != uuid.Nil {
		c.Set(mw.ContextKeyAgentAuthWorkspaceID, wsID) // read by /agents/me
		c.Set(mw.ContextKeyWorkspaceID, wsID)          // read by the task feed
	}
	return c, rec
}

// --- GET /agents/me ---

func TestContract_AgentsMe_Profile(t *testing.T) {
	agentID, homeWS, authWS := uuid.New(), uuid.New(), uuid.New()
	svc := &MockAgentService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: id, WorkspaceID: homeWS, Name: "runner-agent",
				AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOnline}, nil
		},
	}
	h := NewAgentHandler(svc)
	c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me", "", agentID, authWS)

	require.NoError(t, h.Me(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := contractKeys(t, rec.Body.Bytes())
	requireContractKeys(t, body, "id", "workspace_id", "home_workspace_id", "name", "agent_type", "status")

	var got struct {
		ID              uuid.UUID `json:"id"`
		WorkspaceID     uuid.UUID `json:"workspace_id"`
		HomeWorkspaceID uuid.UUID `json:"home_workspace_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, agentID, got.ID)
	assert.Equal(t, authWS, got.WorkspaceID, "workspace_id is the workspace the key authenticated into")
	assert.Equal(t, homeWS, got.HomeWorkspaceID, "home_workspace_id is the agent's own row")
	assert.NotContains(t, body, "api_key_hash", "key hash must never be on the wire")
}

func TestContract_AgentsMe_Errors(t *testing.T) {
	h := NewAgentHandler(&MockAgentService{})

	t.Run("no agent key is 401", func(t *testing.T) {
		c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me", "", uuid.Nil, uuid.Nil)
		require.NoError(t, h.Me(c))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("unresolved key workspace is 500, never the home row", func(t *testing.T) {
		svc := &MockAgentService{
			GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
				return &domain.Agent{ID: id, WorkspaceID: uuid.New()}, nil
			},
		}
		c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me", "", uuid.New(), uuid.Nil)
		require.NoError(t, NewAgentHandler(svc).Me(c))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// --- GET /agents/me/tasks and /agents/me/tasks/poll ---

func contractTaskService(rows []domain.Task, total int, got *repository.AssigneeTaskFilter) *MockTaskService {
	return &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, typ domain.AssigneeType, f repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
			if got != nil {
				*got = f
			}
			return rows, total, nil
		},
	}
}

func TestContract_AgentsMeTasks_FeedShape(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	rows := []domain.Task{
		{ID: uuid.New(), ProjectID: uuid.New(), StatusID: uuid.New(), Title: "one", Description: "full description"},
		{ID: uuid.New(), ProjectID: uuid.New(), StatusID: uuid.New(), Title: "two"},
	}
	var filter repository.AssigneeTaskFilter
	h := NewAgentHandlerWithTaskService(nil, contractTaskService(rows, 5, &filter))
	projID := uuid.New()
	c, rec := contractCtx(http.MethodGet,
		"/api/v1/agents/me/tasks?status_category=todo&page=1&page_size=2&project_id="+projID.String(),
		"", agentID, wsID)

	require.NoError(t, h.GetMyTasks(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := contractKeys(t, rec.Body.Bytes())
	requireContractKeys(t, body, "tasks", "count", "total_count", "has_more")

	var got struct {
		Tasks      []map[string]any `json:"tasks"`
		Count      int              `json:"count"`
		TotalCount int              `json:"total_count"`
		HasMore    bool             `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Tasks, 2)
	assert.Equal(t, 2, got.Count)
	assert.Equal(t, 5, got.TotalCount)
	assert.True(t, got.HasMore, "has_more drives the runner's paging loop")
	for _, k := range []string{"id", "project_id", "status_id", "title"} {
		assert.Contains(t, got.Tasks[0], k, "task key %q is read by the runner", k)
	}
	assert.Equal(t, "full description", got.Tasks[0]["description"], "feed carries the description in full")

	require.NotNil(t, filter.StatusCategory)
	assert.Equal(t, domain.StatusCategory("todo"), *filter.StatusCategory)
	assert.Equal(t, 2, filter.Limit, "page_size is accepted as the limit alias")
	require.NotNil(t, filter.ProjectID)
	assert.Equal(t, projID, *filter.ProjectID)
}

func TestContract_AgentsMeTasks_LastPageHasNoMore(t *testing.T) {
	rows := []domain.Task{{ID: uuid.New(), Title: "only"}}
	h := NewAgentHandlerWithTaskService(nil, contractTaskService(rows, 1, nil))
	c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me/tasks", "", uuid.New(), uuid.New())

	require.NoError(t, h.GetMyTasks(c))

	var got struct {
		HasMore bool `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.False(t, got.HasMore)
}

func TestContract_AgentsMeTasks_EmptyFeedIsAnArrayNotNull(t *testing.T) {
	h := NewAgentHandlerWithTaskService(nil, contractTaskService([]domain.Task{}, 0, nil))
	c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me/tasks", "", uuid.New(), uuid.New())

	require.NoError(t, h.GetMyTasks(c))

	body := contractKeys(t, rec.Body.Bytes())
	assert.JSONEq(t, `[]`, string(body["tasks"]), "an empty feed is [], so clients can iterate without a nil check")
}

func TestContract_AgentsMeTasks_Errors(t *testing.T) {
	h := NewAgentHandlerWithTaskService(nil, contractTaskService(nil, 0, nil))
	cases := []struct {
		name    string
		target  string
		agent   uuid.UUID
		ws      uuid.UUID
		wantErr int
	}{
		{"no agent key", "/api/v1/agents/me/tasks", uuid.Nil, uuid.New(), http.StatusUnauthorized},
		{"no key workspace", "/api/v1/agents/me/tasks", uuid.New(), uuid.Nil, http.StatusForbidden},
		{"unknown status_category", "/api/v1/agents/me/tasks?status_category=nope", uuid.New(), uuid.New(), http.StatusBadRequest},
		{"project_id not a uuid", "/api/v1/agents/me/tasks?project_id=x", uuid.New(), uuid.New(), http.StatusBadRequest},
		{"limit not positive", "/api/v1/agents/me/tasks?limit=0", uuid.New(), uuid.New(), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := contractCtx(http.MethodGet, tc.target, "", tc.agent, tc.ws)
			require.NoError(t, h.GetMyTasks(c))
			assert.Equal(t, tc.wantErr, rec.Code)
		})
	}
}

func TestContract_AgentsMeTasksPoll_ChangedFlag(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	svc := contractTaskService([]domain.Task{{ID: uuid.New(), Title: "t"}}, 1, nil)
	h := NewAgentHandlerFull(&MockAgentService{}, svc, nil, rdb)
	c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me/tasks/poll?timeout=1", "", uuid.New(), uuid.New())

	start := time.Now()
	require.NoError(t, h.PollTasks(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond, "poll holds the request for `timeout` seconds")

	body := contractKeys(t, rec.Body.Bytes())
	requireContractKeys(t, body, "tasks", "count", "total_count", "has_more", "changed")
	assert.JSONEq(t, `false`, string(body["changed"]), "a timeout answers changed=false")
}

func TestContract_AgentsMeTasksPoll_BadFilterAnswersImmediately(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	h := NewAgentHandlerFull(&MockAgentService{}, contractTaskService(nil, 0, nil), nil, rdb)
	c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me/tasks/poll?timeout=30&status_category=nope", "", uuid.New(), uuid.New())

	start := time.Now()
	require.NoError(t, h.PollTasks(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Less(t, time.Since(start), 2*time.Second, "a client bug is reported before the caller is parked")
}

func TestContract_AgentsMeTasksPoll_AuthErrorsAnswerImmediately(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	h := NewAgentHandlerFull(&MockAgentService{}, contractTaskService(nil, 0, nil), nil, rdb)
	cases := []struct {
		name  string
		agent uuid.UUID
		ws    uuid.UUID
		want  int
	}{
		{"no agent key is 401", uuid.Nil, uuid.New(), http.StatusUnauthorized},
		{"no key workspace is 403", uuid.New(), uuid.Nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := contractCtx(http.MethodGet, "/api/v1/agents/me/tasks/poll?timeout=30", "", tc.agent, tc.ws)
			start := time.Now()
			require.NoError(t, h.PollTasks(c))
			assert.Equal(t, tc.want, rec.Code)
			assert.Less(t, time.Since(start), 2*time.Second, "refused before the caller is parked")
		})
	}
}

// --- GET /agents/me/events/stream (SSE) ---

func TestContract_AgentsMeEventsStream_FramingAndEventTypes(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	cursor, _ := uuid.NewV7()
	time.Sleep(2 * time.Millisecond)
	ids := map[string]uuid.UUID{}
	var events []domain.AgentEvent
	for _, typ := range []string{"task.assigned", "task.updated", "comment.created"} {
		id, _ := uuid.NewV7()
		time.Sleep(2 * time.Millisecond)
		ids[typ] = id
		payload, _ := json.Marshal(map[string]any{"event_id": id.String(), "event_type": typ})
		events = append(events, domain.AgentEvent{EventID: id, EventType: typ, Payload: payload})
	}
	repo := &mockSSEEventsRepo{
		LookupFunc: func(_ context.Context, id uuid.UUID) (*domain.AgentEvent, error) {
			if id == cursor {
				return &domain.AgentEvent{EventID: cursor}, nil
			}
			return nil, nil
		},
		ListAfterFunc: func(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.AgentEvent, error) {
			return events, nil
		},
	}
	h := newSSETestHandler(rdb, repo)
	c, rec, cancel := sseEchoCtxTimeout(t, echo.New(), uuid.New(),
		map[string]string{"Last-Event-ID": cursor.String()}, 200*time.Millisecond)
	defer cancel()

	_ = h.EventStream(c)

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	for typ, id := range ids {
		frame := "id: " + id.String() + "\nevent: " + typ + "\ndata: "
		assert.Contains(t, body, frame, "frame for %s must be `id:` / `event:` / `data:` lines", typ)
	}
	assert.True(t, strings.Index(body, "event: task.assigned") < strings.Index(body, "event: task.updated") &&
		strings.Index(body, "event: task.updated") < strings.Index(body, "event: comment.created"),
		"replay keeps event order")
	assert.Contains(t, body, "\n\n", "frames end with a blank line")
}

func TestContract_AgentsMeEventsStream_LiveEvent(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	agentID := uuid.New()
	h := newSSETestHandler(rdb, nil)
	c, rec, cancel := sseEchoCtxTimeout(t, echo.New(), agentID, nil, 400*time.Millisecond)
	defer cancel()

	go func() {
		time.Sleep(150 * time.Millisecond)
		payload, _ := json.Marshal(map[string]any{"event_id": uuid.NewString(), "event_type": "task.assigned"})
		rdb.Publish(context.Background(), agentNotifyChannelPrefix+agentID.String(), payload)
	}()
	_ = h.EventStream(c)

	assert.Contains(t, rec.Body.String(), "event: task.assigned\ndata: ")
}

func TestContract_AgentsMeEventsStream_CursorErrors(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	h := newSSETestHandler(rdb, &mockSSEEventsRepo{})

	t.Run("unknown cursor is 410 cursor_expired", func(t *testing.T) {
		c, rec := sseEchoCtx(t, echo.New(), uuid.New(), map[string]string{"Last-Event-ID": uuid.NewString()})
		require.NoError(t, h.EventStream(c))
		assert.Equal(t, http.StatusGone, rec.Code)
		body := contractKeys(t, rec.Body.Bytes())
		requireContractKeys(t, body, "error", "error_code", "code", "message")
		assert.JSONEq(t, `"cursor_expired"`, string(body["code"]))
	})
	t.Run("malformed cursor is 400", func(t *testing.T) {
		c, rec := sseEchoCtx(t, echo.New(), uuid.New(), map[string]string{"Last-Event-ID": "not-a-uuid"})
		require.NoError(t, h.EventStream(c))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("no agent key is 401", func(t *testing.T) {
		c, rec := sseEchoCtx(t, echo.New(), uuid.Nil, nil)
		c.Set("agent_id", nil)
		require.NoError(t, h.EventStream(c))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// --- POST /agents/me/sessions/report ---

func TestContract_AgentsMeSessionsReport_AccumulatesUsage(t *testing.T) {
	agentID, wsID, taskID := uuid.New(), uuid.New(), uuid.New()
	svc := &MockAgentService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
			return &domain.Agent{ID: id, WorkspaceID: wsID}, nil
		},
	}
	h := NewAgentHandlerWithEvents(svc, nil, nil, nil, nil, newMockSessionRepo())
	body := `{"tokens_in":100,"tokens_out":40,"model":"m-1","estimated_cost":0.5,"task_id":"` + taskID.String() + `"}`

	var last map[string]json.RawMessage
	for i := 0; i < 2; i++ {
		c, rec := contractCtx(http.MethodPost, "/api/v1/agents/me/sessions/report", body, agentID, wsID)
		require.NoError(t, h.ReportSession(c))
		require.Equal(t, http.StatusOK, rec.Code)
		last = contractKeys(t, rec.Body.Bytes())
	}

	requireContractKeys(t, last, "session_id", "totals")
	var totals struct {
		TokensIn      int64   `json:"tokens_in"`
		TokensOut     int64   `json:"tokens_out"`
		EstimatedCost float64 `json:"estimated_cost"`
		ModelUsed     string  `json:"model_used"`
	}
	require.NoError(t, json.Unmarshal(last["totals"], &totals))
	assert.Equal(t, int64(200), totals.TokensIn, "reports for the same task add up on one session")
	assert.Equal(t, int64(80), totals.TokensOut)
	assert.InDelta(t, 1.0, totals.EstimatedCost, 1e-9)
	assert.Equal(t, "m-1", totals.ModelUsed)
}

func TestContract_AgentsMeSessionsReport_Errors(t *testing.T) {
	t.Run("no agent key is 401", func(t *testing.T) {
		h := NewAgentHandlerWithEvents(&MockAgentService{}, nil, nil, nil, nil, newMockSessionRepo())
		c, rec := contractCtx(http.MethodPost, "/api/v1/agents/me/sessions/report", `{}`, uuid.Nil, uuid.Nil)
		require.NoError(t, h.ReportSession(c))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("session tracking off is 501", func(t *testing.T) {
		h := NewAgentHandler(&MockAgentService{})
		c, rec := contractCtx(http.MethodPost, "/api/v1/agents/me/sessions/report", `{}`, uuid.New(), uuid.New())
		require.NoError(t, h.ReportSession(c))
		assert.Equal(t, http.StatusNotImplemented, rec.Code)
	})
}
