package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// fakeScope stands in for ExtensionProjectScopeRepo in handler tests; the SQL
// predicate itself is exercised against Postgres in
// internal/middleware/extension_project_scope_db_test.go.
type fakeScope struct {
	repository.ExtensionProjectScopeRepository
	allowed  map[uuid.UUID]bool
	all      bool
	err      error
	setScope string
	setExt   string
}

func (f *fakeScope) AllowsProjectFor(_ context.Context, _ uuid.UUID, _ string, p uuid.UUID) (bool, error) {
	return f.allowed[p], f.err
}

func (f *fakeScope) BoundProjects(context.Context, uuid.UUID) (bool, []uuid.UUID, error) {
	if f.err != nil {
		return false, nil, f.err
	}
	var ids []uuid.UUID
	for id := range f.allowed {
		ids = append(ids, id)
	}
	return f.all, ids, nil
}

func (f *fakeScope) SetProjectScope(_ context.Context, _ uuid.UUID, ext, scope string) error {
	if f.err != nil {
		return f.err
	}
	f.setExt, f.setScope = ext, scope
	return nil
}

func TestUIContext_ProjectScope(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	body := `{"workspace_id":"` + f.ws.String() + `","slot":"sidebar.item","task_id":"` + f.ownTask.String() + `"}`
	scope := &fakeScope{allowed: map[uuid.UUID]bool{}}
	f.h.WithProjectScope(scope)

	rec, _ := f.call(t, f.user, body)
	assert.Equal(t, http.StatusForbidden, rec.Code, "the task's project is not bound")

	// Positive control: the same request once the project is bound.
	scope.allowed = map[uuid.UUID]bool{f.h.tasks.(fakeTasks)[f.ownTask].ProjectID: true}
	rec, _ = f.call(t, f.user, body)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	scope.err = errors.New("db down")
	rec, _ = f.call(t, f.user, body)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func boundHierarchyCall(h *WorkspaceExportHandler, ws uuid.UUID) *httptest.ResponseRecorder {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(ws.String())
	c.Set(middleware.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{
		WorkspaceID: ws, ConnectionID: uuid.New(), ExtensionID: "mm", ProjectBound: true,
		Capabilities: map[string][]string{"api": {"workspaces:read", "projects:read", "members:read", "agents:read"}},
	})
	_ = h.Hierarchy(c)
	return rec
}

func TestWorkspaceExportHandler_ProjectBound(t *testing.T) {
	ws, bound, other := uuid.New(), uuid.New(), uuid.New()
	newSnap := func() *domain.HierarchyExport {
		return &domain.HierarchyExport{Projects: []domain.HierarchyProject{{ID: other, Slug: "o"}, {ID: bound, Slug: "b"}}}
	}

	// Without a scope checker a bound connection is refused rather than shown everything.
	h := NewWorkspaceExportHandler(&fakeHierarchyReader{snap: newSnap(), ok: true})
	assert.Equal(t, http.StatusForbidden, boundHierarchyCall(h, ws).Code)

	h = NewWorkspaceExportHandler(&fakeHierarchyReader{snap: newSnap(), ok: true}).
		WithProjectScope(&fakeScope{allowed: map[uuid.UUID]bool{bound: true}})
	rec := boundHierarchyCall(h, ws)
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Projects []domain.HierarchyProject `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Projects, 1)
	assert.Equal(t, bound, out.Projects[0].ID)

	// all=true (scope switched back to workspace in the meantime): unfiltered.
	h = NewWorkspaceExportHandler(&fakeHierarchyReader{snap: newSnap(), ok: true}).WithProjectScope(&fakeScope{all: true})
	rec = boundHierarchyCall(h, ws)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), other.String())

	h = NewWorkspaceExportHandler(&fakeHierarchyReader{snap: newSnap(), ok: true}).WithProjectScope(&fakeScope{err: errors.New("db down")})
	assert.Equal(t, http.StatusInternalServerError, boundHierarchyCall(h, ws).Code)
}

func TestProjectHandler_List_ExtensionScope(t *testing.T) {
	wsID, connID := uuid.New(), uuid.New()
	var got repository.ProjectFilter
	svc := &MockProjectService{ListFunc: func(_ context.Context, _ uuid.UUID, f repository.ProjectFilter, _ pagination.Params) (*pagination.Page[domain.Project], error) {
		got = f
		return &pagination.Page[domain.Project]{Items: []domain.Project{}}, nil
	}}
	h, e := setupProjectTest(svc)
	list := func(bound bool) {
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(wsID.String())
		c.Set(middleware.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{WorkspaceID: wsID, ConnectionID: connID, ProjectBound: bound})
		require.NoError(t, h.List(c))
		require.Equal(t, http.StatusOK, rec.Code)
	}

	list(true)
	require.NotNil(t, got.ExtensionConnectionID)
	assert.Equal(t, connID, *got.ExtensionConnectionID)
	assert.Nil(t, got.MemberUserID)

	list(false)
	assert.Nil(t, got.ExtensionConnectionID, "a workspace-scoped extension lists the whole workspace")
	assert.Nil(t, got.MemberUserID)
}

type scopeTestProvider struct{ extensions.BaseProvider }

func (scopeTestProvider) Validate(extensions.Level, map[string]any) error { return nil }

func TestExtensionWorkspaceHandler_SetProjectScope(t *testing.T) {
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(scopeTestProvider{extensions.BaseProvider{Man: extensions.Manifest{ID: "withproj", Levels: extensions.Levels{Workspace: true, Project: true}}}}))
	require.NoError(t, reg.Register(scopeTestProvider{extensions.BaseProvider{Man: extensions.Manifest{ID: "noproj", Levels: extensions.Levels{Workspace: true}}}}))
	scope := &fakeScope{}

	call := func(h *ExtensionWorkspaceHandler, ws, ext, body string) *httptest.ResponseRecorder {
		e := echo.New()
		e.PUT("/workspaces/:ws_id/extensions/:ext_id/project-scope", h.SetProjectScope)
		req := httptest.NewRequest(http.MethodPut, "/workspaces/"+ws+"/extensions/"+ext+"/project-scope", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	ws := uuid.NewString()
	h := NewExtensionWorkspaceHandler(nil).WithProjectScope(reg, scope)

	rec := call(h, ws, "withproj", `{"project_scope":"bound"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "bound", scope.setScope)
	assert.Equal(t, "withproj", scope.setExt)
	assert.JSONEq(t, `{"extension_id":"withproj","project_scope":"bound"}`, rec.Body.String())

	assert.Equal(t, http.StatusBadRequest, call(h, ws, "noproj", `{"project_scope":"bound"}`).Code, "no project level, nothing can be bound")
	assert.Equal(t, http.StatusOK, call(h, ws, "noproj", `{"project_scope":"workspace"}`).Code)
	assert.Equal(t, http.StatusNotFound, call(h, ws, "nosuch", `{"project_scope":"bound"}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(h, ws, "withproj", `{`).Code)
	assert.Equal(t, http.StatusBadRequest, call(h, "x", "withproj", `{"project_scope":"bound"}`).Code)

	scope.err = errors.New("db down")
	assert.Equal(t, http.StatusInternalServerError, call(h, ws, "withproj", `{"project_scope":"bound"}`).Code)

	// Not wired: answered, never a nil dereference.
	assert.Equal(t, http.StatusNotImplemented, call(NewExtensionWorkspaceHandler(nil), ws, "withproj", `{"project_scope":"bound"}`).Code)
}
