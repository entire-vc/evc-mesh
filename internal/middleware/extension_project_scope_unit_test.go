package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Branches of ExtensionProjectScopeGuard that need no rows: the DB-backed
// behaviour is in extension_project_scope_db_test.go.

type erringScope struct {
	repository.ExtensionProjectScopeRepository
}

func (erringScope) AllowsProject(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, errors.New("db down")
}

func runScopeGuard(t *testing.T, db *sqlx.DB, scope repository.ExtensionProjectScopeRepository, p *service.ExtensionPrincipal, method, path, name, value string) int {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, "/", http.NoBody), rec)
	c.SetPath(path)
	if name != "" {
		c.SetParamNames(name)
		c.SetParamValues(value)
	}
	if p != nil {
		c.Set(ContextKeyExtensionPrincipal, p)
	}
	h := ExtensionProjectScopeGuard(db, scope, nil)(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	require.NoError(t, h(c))
	return rec.Code
}

func TestExtensionProjectScopeGuard_Branches(t *testing.T) {
	bound := &service.ExtensionPrincipal{ConnectionID: uuid.New(), ProjectBound: true}

	// Not an extension, or a workspace-scoped one: untouched (nil db is never read).
	assert.Equal(t, http.StatusNoContent, runScopeGuard(t, nil, nil, nil, http.MethodGet, "/api/v1/tasks/:task_id", "task_id", uuid.NewString()))
	assert.Equal(t, http.StatusNoContent, runScopeGuard(t, nil, nil, &service.ExtensionPrincipal{}, http.MethodGet, "/api/v1/tasks/:task_id", "task_id", uuid.NewString()))

	// HEAD is classified as the GET it mirrors.
	assert.Equal(t, http.StatusNoContent, runScopeGuard(t, nil, nil, bound, http.MethodHead, "/api/v1/ext/me", "", ""))
	// A route with no project parameter that is not workspace-level is refused.
	assert.Equal(t, http.StatusForbidden, runScopeGuard(t, nil, nil, bound, http.MethodPatch, "/api/v1/vcs-links/ship-sha", "", ""))
	// Malformed id resolves to no project: refused without a query.
	assert.Equal(t, http.StatusForbidden, runScopeGuard(t, nil, nil, bound, http.MethodGet, "/api/v1/projects/:proj_id", "proj_id", "nope"))

	// Unreachable database: 500, never a pass.
	db, err := sqlx.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	assert.Equal(t, http.StatusInternalServerError, runScopeGuard(t, db, nil, bound, http.MethodGet, "/api/v1/projects/:proj_id", "proj_id", uuid.NewString()))
}

func TestExtensionProjectScopeGuard_CheckerErrorFailsClosed(t *testing.T) {
	s := newScopeFixture(t)
	p := &service.ExtensionPrincipal{ConnectionID: s.conn.ID, ProjectBound: true}
	assert.Equal(t, http.StatusInternalServerError,
		runScopeGuard(t, s.db, erringScope{}, p, http.MethodGet, "/api/v1/projects/:proj_id", "proj_id", s.boundProject.String()))
}

func TestExtensionWorkspaceLevelRoutes_IsACopy(t *testing.T) {
	got := ExtensionWorkspaceLevelRoutes()
	require.True(t, got["GET /api/v1/ext/me"])
	delete(got, "GET /api/v1/ext/me")
	assert.True(t, ExtensionWorkspaceLevelRoutes()["GET /api/v1/ext/me"])
}
