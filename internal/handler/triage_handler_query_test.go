package handler

import (
	"context"
	"errors"
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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type fakeTriageService struct {
	q    repository.TaskQuery
	pg   pagination.Params
	call string
}

func (f *fakeTriageService) ListTriageTasks(_ context.Context, q repository.TaskQuery, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	f.q, f.pg, f.call = q, pg, "triage"
	return pagination.NewPage([]domain.Task{{ID: uuid.New(), Description: "kept"}}, 7, pg), nil
}

func (f *fakeTriageService) QueryTasks(_ context.Context, q repository.TaskQuery, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	f.q, f.pg, f.call = q, pg, "query"
	return pagination.NewPage([]domain.Task{}, 0, pg), nil
}

type taskQueryCaller struct {
	user, agent   uuid.UUID
	role          string
	extension     bool
	ownerFallback bool
}

func runTaskQuery(t *testing.T, path string, who taskQueryCaller, query string) (*fakeTriageService, *httptest.ResponseRecorder, error) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/x?"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.NewString())
	switch {
	case who.extension:
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeExtension)
	case who.agent != uuid.Nil:
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
		c.Set(mw.ContextKeyAgentID, who.agent)
	default:
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
		c.Set(mw.ContextKeyUserID, who.user)
		c.Set(mw.ContextKeyWorkspaceRole, who.role)
		if who.ownerFallback {
			c.Set(mw.ContextKeyWorkspaceOwnerWithoutMembership, true)
		}
	}
	svc := &fakeTriageService{}
	h := NewTriageHandler(svc)
	var err error
	if path == "triage" {
		err = h.List(c)
	} else {
		err = h.Query(c)
	}
	return svc, rec, err
}

func requireValidationError(t *testing.T, err error, field string) {
	t.Helper()
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "want an apierror, got %v", err)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	assert.Contains(t, apiErr.Validation, field)
}

func TestTaskQuery_ViewerScope(t *testing.T) {
	user, agent := uuid.New(), uuid.New()

	svc, _, err := runTaskQuery(t, "query", taskQueryCaller{user: user, role: domain.RoleMember}, "scope=workspace")
	require.NoError(t, err)
	assert.Equal(t, repository.TaskViewer{ActorID: user}, svc.q.Viewer, "a member reads only projects they are in")

	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin} {
		svc, _, err = runTaskQuery(t, "query", taskQueryCaller{user: user, role: role}, "scope=workspace")
		require.NoError(t, err)
		assert.True(t, svc.q.Viewer.AllProjects, "%s reads every project", role)
	}

	// the guard admits an owner without a workspace_members row and sets no role; they still read every project
	for _, path := range []string{"query", "triage"} {
		svc, _, err = runTaskQuery(t, path, taskQueryCaller{user: user, ownerFallback: true}, "scope=workspace")
		require.NoError(t, err)
		assert.True(t, svc.q.Viewer.AllProjects, "%s: the fallback owner reads every project", path)
	}

	svc, _, err = runTaskQuery(t, "triage", taskQueryCaller{agent: agent}, "")
	require.NoError(t, err)
	assert.Equal(t, repository.TaskViewer{ActorID: agent, Agent: true}, svc.q.Viewer, "an agent is checked by its own membership")
}

func TestTaskQuery_ParsesEveryFilter(t *testing.T) {
	user, proj := uuid.New(), uuid.New()
	svc, rec, err := runTaskQuery(t, "query", taskQueryCaller{user: user, role: domain.RoleMember},
		"scope=mine&project_id="+proj.String()+"&status_category=todo,review&priority=urgent,high&due_before=2026-12-01&gated=true&sort=due_date&limit=7")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "query", svc.call)
	q := svc.q
	assert.Equal(t, &user, q.AssigneeUserID)
	assert.Equal(t, &proj, q.ProjectID)
	assert.Equal(t, []domain.StatusCategory{domain.StatusCategoryTodo, domain.StatusCategoryReview}, q.StatusCategories)
	assert.Equal(t, []domain.Priority{domain.PriorityUrgent, domain.PriorityHigh}, q.Priorities)
	require.NotNil(t, q.DueBefore)
	assert.True(t, q.DueBefore.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, q.Gated)
	assert.Equal(t, repository.TaskQuerySortDueDate, q.Sort)
	assert.Equal(t, 7, svc.pg.PageSize)
}

func TestTaskQuery_Defaults(t *testing.T) {
	svc, _, err := runTaskQuery(t, "query", taskQueryCaller{user: uuid.New()}, "scope=workspace")
	require.NoError(t, err)
	assert.Equal(t, repository.TaskQuerySortUpdated, svc.q.Sort)
	assert.Equal(t, taskQueryMaxLimit, svc.pg.PageSize)
	assert.Nil(t, svc.q.AssigneeUserID)
	assert.Empty(t, svc.q.StatusCategories, "empty means every open category; the repository applies it")
}

func TestTaskQuery_RefusesBadInput(t *testing.T) {
	member := taskQueryCaller{user: uuid.New(), role: domain.RoleMember}
	for _, tc := range []struct{ query, field string }{
		{"", "scope"},
		{"scope=everything", "scope"},
		{"scope=project", "project_id"},
		{"scope=workspace&project_id=nope", "project_id"},
		{"scope=workspace&status_category=doing", "status_category"},
		{"scope=workspace&priority=critical", "priority"},
		{"scope=workspace&due_before=tomorrow", "due_before"},
		{"scope=workspace&gated=maybe", "gated"},
		{"scope=workspace&sort=random", "sort"},
		{"scope=workspace&limit=51", "limit"},
		{"scope=workspace&limit=0", "limit"},
	} {
		svc, _, err := runTaskQuery(t, "query", member, tc.query)
		requireValidationError(t, err, tc.field)
		assert.Empty(t, svc.call, "%q must not reach the service", tc.query)
	}
	_, _, err := runTaskQuery(t, "query", taskQueryCaller{agent: uuid.New()}, "scope=mine")
	requireValidationError(t, err, "scope")
}

func TestTriageList_FiltersAndTotal(t *testing.T) {
	proj := uuid.New()
	svc, rec, err := runTaskQuery(t, "triage", taskQueryCaller{user: uuid.New(), role: domain.RoleMember},
		"project_id="+proj.String()+"&hard_gates_only=true&page_size=5")
	require.NoError(t, err)
	assert.Equal(t, "triage", svc.call)
	assert.Equal(t, &proj, svc.q.ProjectID)
	assert.True(t, svc.q.HardGatesOnly)
	assert.Equal(t, 5, svc.pg.PageSize)
	assert.Contains(t, rec.Body.String(), `"total_count":7`)
	assert.Contains(t, rec.Body.String(), `"description":"kept"`, "the inbox row shows the description")

	_, _, err = runTaskQuery(t, "triage", taskQueryCaller{user: uuid.New()}, "hard_gates_only=yes-please")
	requireValidationError(t, err, "hard_gates_only")
}
