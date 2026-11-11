package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
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

// The runner's resolve over real repositories: the wait's own agent ends it,
// a repeat answers resolved=false, another agent is refused, and the read
// that follows shows no active wait while the row keeps how it ended.
func TestTypedOutcomeResolve_DB(t *testing.T) {
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{ID: uuid.New(), Name: "typed-resolve", Slug: "typed-resolve-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "typed-resolve", Slug: "typed-resolve-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, proj))
	status := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Category: domain.StatusCategoryTodo, IsDefault: true}
	require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, status))
	task := &domain.Task{ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID, Title: "typed resolve",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
	require.NoError(t, postgres.NewTaskRepo(db).Create(ctx, task, nil))
	newAgent := func(name string) uuid.UUID {
		s := uuid.New().String()[:8]
		a := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: name, Slug: name + "-" + s,
			AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
			APIKeyHash: "$2a$12$" + s, APIKeyPrefix: s, APIKeySHA256: "typed-resolve-" + uuid.New().String()}
		require.NoError(t, postgres.NewAgentRepo(db).Create(ctx, a))
		return a.ID
	}
	owner, stranger := newAgent("resolve-owner"), newAgent("resolve-stranger")

	waits, turns := postgres.NewTaskWaitRepo(db), postgres.NewTurnOutcomeRepo(db)
	reg, ref := "receipt", task.ID.String()[:8]
	wait := &domain.TaskWait{TaskID: task.ID, AgentID: owner, Kind: domain.TaskWaitRegistered, Registration: &reg, RegistrationRef: &ref}
	require.NoError(t, waits.Declare(ctx, wait))

	h := NewTypedOutcomeHandler(service.NewTypedOutcomeService(waits, turns), nil)
	e := echo.New()
	e.GET("/api/v1/tasks/:task_id/wait", h.GetActiveWait)
	e.POST("/api/v1/tasks/:task_id/waits/:wait_id/resolve", h.ResolveWait)
	path := "/api/v1/tasks/" + task.ID.String() + "/waits/" + wait.ID.String() + "/resolve"
	post := func(agent uuid.UUID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/tasks/:task_id/waits/:wait_id/resolve")
		c.SetParamNames("task_id", "wait_id")
		c.SetParamValues(task.ID.String(), wait.ID.String())
		if agent != uuid.Nil {
			c.Set(mw.ContextKeyAgentID, agent)
			c.Set(mw.ContextKeyAgentAuthWorkspaceID, ws.ID)
		}
		require.NoError(t, h.ResolveWait(c))
		return rec
	}

	assert.Equal(t, http.StatusForbidden, post(stranger, `{"cause":"saw it"}`).Code)
	assert.Equal(t, http.StatusForbidden, post(uuid.Nil, `{"cause":"saw it"}`).Code)
	assert.Equal(t, http.StatusBadRequest, post(owner, `{}`).Code, "a cause is required")

	rec := post(owner, `{"resolution":"met","cause":"receipt 42 arrived"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Wait     domain.TaskWait `json:"wait"`
		Resolved bool            `json:"resolved"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.True(t, out.Resolved)
	assert.Equal(t, domain.WaitResolutionMet, *out.Wait.Resolution)
	assert.Equal(t, "receipt 42 arrived", *out.Wait.ResolveCause)
	assert.Equal(t, owner, *out.Wait.ResolvedBy)
	assert.Equal(t, "receipt", *out.Wait.Registration)

	rec = post(owner, `{"cause":"again"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.False(t, out.Resolved, "an idempotent repeat")
	assert.Equal(t, http.StatusConflict, post(owner, `{"resolution":"invalid","cause":"x"}`).Code)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+task.ID.String()+"/wait", http.NoBody))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
