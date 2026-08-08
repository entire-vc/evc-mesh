package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The agent's own task feed carries no path parameter, so the only workspace in
// play is the one its API key resolved to. These tests pin that the handler takes
// it from there and fails closed when it is absent.
//
// Why fail closed matters here specifically: the alternative — calling the
// service with uuid.Nil — would send an unscoped-looking query to a repository
// whose whole job is now to scope, and the most likely outcome of a
// workspace_id = NULL comparison is an empty feed that looks like "no work" rather
// than like a bug. An explicit 403 says which of the two happened.

func TestGetMyTasks_RefusesWhenTheKeyResolvesNoWorkspace(t *testing.T) {
	agentID := uuid.New()
	called := false
	taskSvc := &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType) ([]domain.Task, error) {
			called = true
			return nil, nil
		},
	}
	h := NewAgentHandlerWithTaskService(nil, taskSvc)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, agentID) // authenticated, but no workspace on the context

	require.NoError(t, h.GetMyTasks(c))

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"an unresolvable workspace must refuse the feed, not serve an unscoped one")
	assert.False(t, called,
		"the service must not be reached at all — reaching it with uuid.Nil would turn an "+
			"authorization failure into an empty feed that reads as 'no work'")
}

func TestGetMyTasks_ScopesToTheWorkspaceOnTheKey(t *testing.T) {
	agentID, wsID := uuid.New(), uuid.New()
	var gotWorkspace, gotAssignee uuid.UUID
	taskSvc := &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, workspaceID, assigneeID uuid.UUID, _ domain.AssigneeType) ([]domain.Task, error) {
			gotWorkspace, gotAssignee = workspaceID, assigneeID
			return []domain.Task{}, nil
		},
	}
	h := NewAgentHandlerWithTaskService(nil, taskSvc)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, agentID)
	c.Set(mw.ContextKeyWorkspaceID, wsID)

	require.NoError(t, h.GetMyTasks(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, wsID, gotWorkspace,
		"the feed must be scoped to the workspace the key resolved to, not to anything in the request")
	assert.Equal(t, agentID, gotAssignee)
}

func TestGetMyTasks_RefusesWithoutAnAgentIdentity(t *testing.T) {
	h := NewAgentHandlerWithTaskService(nil, &MockTaskService{})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)

	require.NoError(t, h.GetMyTasks(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGetMyTasks_RejectsAMalformedAgentIdentity(t *testing.T) {
	h := NewAgentHandlerWithTaskService(nil, &MockTaskService{})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, "not-a-uuid")

	require.NoError(t, h.GetMyTasks(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestHandleError_ForeignAssigneeIsUnprocessableNotForbidden pins the status and
// the code the API answers with.
//
// 422 rather than 403 is deliberate: the caller is normally a legitimate member
// of this workspace, and what is being refused is the principal they named, not
// their own right to be here. A 403 sends them to audit their own permissions.
// The machine-readable code is what clients branch on, so it is asserted too.
func TestHandleError_ForeignAssigneeIsUnprocessableNotForbidden(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), rec)

	foreignWS := uuid.New()
	err := handleError(c, &service.AssigneeNotInWorkspaceError{
		PrincipalID:  uuid.New(),
		AssigneeType: domain.AssigneeTypeAgent,
		ProjectID:    uuid.New(),
		Reason:       "agent belongs to a different workspace",
	})
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "assignee_not_in_workspace", body["code"])
	assert.NotContains(t, rec.Body.String(), foreignWS.String(),
		"the refusal must not name the workspace the principal actually belongs to — that would "+
			"turn it into an oracle for probing which ids live in which tenant")
}

// The long-poll twin of the feed carries the same guard, and the reason it is
// resolved BEFORE the poll rather than after matters: refusing a caller who has
// already been parked for thirty seconds reports an authorization outcome as a
// timeout, which is indistinguishable from "no work right now".
//
// A non-nil Redis client is required only to get past the handler's
// not-configured check — these cases all return before anything subscribes, so
// the address is never dialled.
func pollHandlerWithUnusedRedis(ts service.TaskService) *AgentHandler {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	return NewAgentHandlerFull(nil, ts, nil, rdb)
}

func TestPollTasks_RefusesWhenTheKeyResolvesNoWorkspace(t *testing.T) {
	called := false
	h := pollHandlerWithUnusedRedis(&MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType) ([]domain.Task, error) {
			called = true
			return nil, nil
		},
	})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, uuid.New())

	require.NoError(t, h.PollTasks(c))

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"the refusal must arrive immediately, not after the poll window — a 403 says "+
			"'not allowed', a timed-out empty result says 'nothing to do'")
	assert.False(t, called)
}

func TestPollTasks_RefusesWithoutAnAgentIdentity(t *testing.T) {
	h := pollHandlerWithUnusedRedis(&MockTaskService{})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)

	require.NoError(t, h.PollTasks(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPollTasks_RejectsAMalformedAgentIdentity(t *testing.T) {
	h := pollHandlerWithUnusedRedis(&MockTaskService{})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, "not-a-uuid")

	require.NoError(t, h.PollTasks(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
