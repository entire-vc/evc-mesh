package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The runner's two reads over real repositories: a card with no outcomes and
// no wait answers [] and 404 no_active_wait, and both turn into data once rows
// exist. Same convention as the other *_db_test.go here: skips without Postgres.
func TestTypedOutcomeReads_DB(t *testing.T) {
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{ID: uuid.New(), Name: "typed-http", Slug: "typed-http-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "typed-http", Slug: "typed-http-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, proj))
	status := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Category: domain.StatusCategoryTodo, IsDefault: true}
	require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, status))
	task := &domain.Task{ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID, Title: "typed reads",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
	require.NoError(t, postgres.NewTaskRepo(db).Create(ctx, task, nil))
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "typed-http", Slug: "typed-http-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + suffix, APIKeyPrefix: suffix, APIKeySHA256: "typed-http-" + uuid.New().String()}
	require.NoError(t, postgres.NewAgentRepo(db).Create(ctx, agent))

	waits, turns := postgres.NewTaskWaitRepo(db), postgres.NewTurnOutcomeRepo(db)
	h := NewTypedOutcomeHandler(service.NewTypedOutcomeService(waits, turns), nil)
	e := echo.New()
	e.GET("/api/v1/tasks/:task_id/turn-outcomes", h.ListTurnOutcomes)
	e.GET("/api/v1/tasks/:task_id/wait", h.GetActiveWait)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		return rec
	}
	base := "/api/v1/tasks/" + task.ID.String()

	rec := get(base + "/turn-outcomes?session_id=x")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[]`, rec.Body.String())

	rec = get(base + "/wait")
	require.Equal(t, http.StatusNotFound, rec.Code)
	var apiErr struct {
		ErrorCode string `json:"error_code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, domain.CodeNoActiveWait, apiErr.ErrorCode)

	session := "x"
	require.NoError(t, turns.Append(ctx, &domain.TurnOutcome{TaskID: task.ID, AgentID: agent.ID, SessionID: &session,
		Kind: domain.TurnOutcomeNext, Note: "edited"}))
	human := "gate-1"
	require.NoError(t, waits.Declare(ctx, &domain.TaskWait{TaskID: task.ID, AgentID: agent.ID,
		Kind: domain.TaskWaitHuman, HumanGateRef: &human}))

	rec = get(base + "/turn-outcomes?session_id=x")
	require.Equal(t, http.StatusOK, rec.Code)
	var outs []domain.TurnOutcome
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &outs))
	require.Len(t, outs, 1)
	assert.Equal(t, domain.TurnOutcomeNext, outs[0].Kind)
	assert.JSONEq(t, `[]`, get(base+"/turn-outcomes?session_id=other").Body.String())

	rec = get(base + "/wait?agent_id=" + agent.ID.String())
	require.Equal(t, http.StatusOK, rec.Code)
	var w domain.TaskWait
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &w))
	assert.Equal(t, domain.TaskWaitHuman, w.Kind)

	assert.Equal(t, http.StatusBadRequest, get(base+"/wait?agent_id=nope").Code)
	assert.Equal(t, http.StatusBadRequest, get(base+"/turn-outcomes?limit=0").Code)
}
