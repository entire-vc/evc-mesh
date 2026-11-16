package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// The Sessions page and the dashboard agent widgets label each card with the
// agent's harness. Without agent_type in this payload the UI always fell back
// to the generic "Agent" label.
func TestAgentHandler_GetAgentsStatus_ReturnsAgentType(t *testing.T) {
	wsID := uuid.New()
	now := time.Now()
	codexID, unsetID := uuid.New(), uuid.New()

	mockSvc := &MockAgentService{
		ListFunc: func(_ context.Context, gotWs uuid.UUID, _ repository.AgentFilter, _ pagination.Params) (*pagination.Page[domain.Agent], error) {
			assert.Equal(t, wsID, gotWs)
			return &pagination.Page[domain.Agent]{Items: []domain.Agent{
				{ID: codexID, WorkspaceID: wsID, Name: "codex-lane", AgentType: domain.AgentTypeCodex, Status: domain.AgentStatusOnline, LastHeartbeat: &now},
				{ID: unsetID, WorkspaceID: wsID, Name: "legacy", Status: domain.AgentStatusOffline},
			}}, nil
		},
	}

	h := NewAgentHandler(mockSvc)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	require.NoError(t, h.GetAgentsStatus(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Agents, 2)

	byID := map[string]map[string]any{}
	for _, a := range body.Agents {
		byID[a["id"].(string)] = a
	}
	assert.Equal(t, "codex", byID[codexID.String()]["agent_type"])
	// An agent registered without a type keeps the key (empty), so the UI
	// can tell "unknown" from "field missing".
	got, ok := byID[unsetID.String()]["agent_type"]
	assert.True(t, ok, "agent_type key must be present for every agent")
	assert.Equal(t, "", got)
}

// A workspace member who is not in the project of an agent's current task must
// not learn that task's id or title from the agent list; a member who is keeps
// both. The agent itself is listed for both.
func TestAgentHandler_GetAgentsStatus_HidesCurrentTaskOutsideCallerProjects(t *testing.T) {
	wsID := uuid.New()
	visibleAgent, hiddenAgent := uuid.New(), uuid.New()
	visibleTask, hiddenTask := uuid.New(), uuid.New()

	mockSvc := &MockAgentService{
		ListFunc: func(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, _ pagination.Params) (*pagination.Page[domain.Agent], error) {
			return &pagination.Page[domain.Agent]{Items: []domain.Agent{
				{ID: visibleAgent, WorkspaceID: wsID, Name: "in-my-project", Status: domain.AgentStatusBusy, CurrentTaskID: &visibleTask},
				{ID: hiddenAgent, WorkspaceID: wsID, Name: "in-closed-project", Status: domain.AgentStatusBusy, CurrentTaskID: &hiddenTask},
			}}, nil
		},
	}
	var looked []uuid.UUID
	taskSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			looked = append(looked, id)
			return &domain.Task{ID: id, Title: "title of " + id.String()}, nil
		},
	}

	run := func(t *testing.T, setAccess bool) map[string]map[string]any {
		t.Helper()
		h := NewAgentHandlerWithTaskService(mockSvc, taskSvc)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(wsID.String())
		if setAccess {
			mw.SetProjectAccess(c, mw.ProjectAccessCheck{
				Task: func(id uuid.UUID) bool { return id == visibleTask },
			})
		}
		require.NoError(t, h.GetAgentsStatus(c))
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Agents []map[string]any `json:"agents"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Len(t, body.Agents, 2)
		byID := map[string]map[string]any{}
		for _, a := range body.Agents {
			byID[a["id"].(string)] = a
		}
		return byID
	}

	t.Run("member sees own project's task, not the other", func(t *testing.T) {
		looked = nil
		byID := run(t, true)
		assert.Equal(t, visibleTask.String(), byID[visibleAgent.String()]["current_task_id"])
		assert.Equal(t, "title of "+visibleTask.String(), byID[visibleAgent.String()]["current_task_title"])

		_, hasID := byID[hiddenAgent.String()]["current_task_id"]
		_, hasTitle := byID[hiddenAgent.String()]["current_task_title"]
		assert.False(t, hasID, "current_task_id of a task outside the caller's projects must be omitted")
		assert.False(t, hasTitle, "current_task_title of a task outside the caller's projects must be omitted")
		assert.Equal(t, "busy", byID[hiddenAgent.String()]["status"], "the agent itself stays listed")
		assert.NotContains(t, looked, hiddenTask, "a hidden task's title is not even looked up")
	})

	t.Run("no access check on the request hides every task", func(t *testing.T) {
		byID := run(t, false)
		for _, a := range byID {
			_, hasID := a["current_task_id"]
			_, hasTitle := a["current_task_title"]
			assert.False(t, hasID, "current_task_id must be omitted without an access check")
			assert.False(t, hasTitle, "current_task_title must be omitted without an access check")
		}
	})
}
