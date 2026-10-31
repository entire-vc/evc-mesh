package middleware

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// A project-bound extension connection, against a real database: the guard in
// the cmd/api chain refuses every route that addresses a project without an
// enabled binding for the extension, by project id, task id, short id alike.

type scopeFixture struct {
	*extFixture
	scope                      *postgres.ExtensionProjectScopeRepo
	boundProject, otherProject uuid.UUID
	boundTask, otherTask       uuid.UUID
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := newExtFixture(t, []string{"api:projects:read", "api:tasks:read", "api:workspaces:read", "api:vcs_links:write"})
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	projRepo := postgres.NewProjectRepo(f.db)
	mkProject := func(slug string) uuid.UUID {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: f.ws.ID, Name: slug, Slug: slug + "-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone, Settings: []byte("{}"), CreatedAt: time.Now(), UpdatedAt: time.Now()}
		require.NoError(t, projRepo.Create(ctx, p))
		return p.ID
	}
	mkTask := func(project uuid.UUID) uuid.UUID {
		status := uuid.New()
		_, err := f.db.ExecContext(ctx, `INSERT INTO task_statuses (id, project_id, name, slug, category) VALUES ($1,$2,'Todo',$3,'todo')`, status, project, "todo-"+status.String()[:8])
		require.NoError(t, err)
		id := uuid.New()
		_, err = f.db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'t',(SELECT COALESCE(MAX(task_number),0)+1 FROM tasks WHERE project_id=$2),$4)`, id, project, status, f.ws.OwnerID)
		require.NoError(t, err)
		return id
	}
	s := &scopeFixture{extFixture: f, scope: postgres.NewExtensionProjectScopeRepo(f.db)}
	s.boundProject, s.otherProject = mkProject("bound"), mkProject("other")
	s.boundTask, s.otherTask = mkTask(s.boundProject), mkTask(s.otherProject)

	_, err := f.db.ExecContext(ctx, `INSERT INTO project_integrations (project_id, type, extension_id, enabled) VALUES ($1,'gitlab','gitlab',true)`, s.boundProject)
	require.NoError(t, err)

	f.e = echo.New()
	api := f.e.Group("/api/v1")
	api.Use(ExtensionAuth(f.svc))
	api.Use(ExtensionCapabilityGuard(f.svc))
	api.Use(ExtensionProjectScopeGuard(f.db, s.scope, f.svc))
	reach := func(name string) echo.HandlerFunc {
		return func(c echo.Context) error {
			f.reached[name]++
			return c.NoContent(http.StatusOK)
		}
	}
	api.GET("/projects/:proj_id", reach("project"))
	api.GET("/projects/:proj_id/tasks", reach("tasks"))
	api.GET("/tasks/:task_id", reach("task"))
	api.GET("/tasks/by-short-id/:short", reach("short"))
	api.GET("/workspaces/:ws_id/projects", reach("ws-projects"))
	api.PATCH("/vcs-links/ship-sha", reach("ship-sha"))
	return s
}

func (s *scopeFixture) setScope(t *testing.T, scope string) {
	t.Helper()
	require.NoError(t, s.scope.SetProjectScope(context.Background(), s.ws.ID, "gitlab", scope))
}

func TestExtensionProjectScope_WorkspaceScopeIsUnchanged(t *testing.T) {
	s := newScopeFixture(t)
	// Default scope: the whole workspace, as before scopes existed.
	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/projects/"+s.otherProject.String(), s.token))
	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/tasks/"+s.otherTask.String(), s.token))
	assert.Equal(t, http.StatusOK, s.do(http.MethodPatch, "/api/v1/vcs-links/ship-sha", s.token))
}

func TestExtensionProjectScope_BoundConnectionReachesOnlyBoundProjects(t *testing.T) {
	s := newScopeFixture(t)
	s.setScope(t, domain.ExtensionProjectScopeBound)

	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/projects/"+s.boundProject.String(), s.token))
	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/projects/"+s.boundProject.String()+"/tasks", s.token))
	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/tasks/"+s.boundTask.String(), s.token))
	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/tasks/by-short-id/"+s.boundTask.String()[:8], s.token))
	assert.Equal(t, http.StatusOK, s.do(http.MethodGet, "/api/v1/workspaces/"+s.ws.ID.String()+"/projects", s.token))

	before := s.violations(t)
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, "/api/v1/projects/"+s.otherProject.String(), s.token))
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, "/api/v1/projects/"+s.otherProject.String()+"/tasks", s.token))
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, "/api/v1/tasks/"+s.otherTask.String(), s.token))
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, "/api/v1/tasks/"+s.otherTask.String()[:8], s.token), "short id in :task_id")
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, "/api/v1/tasks/by-short-id/"+s.otherTask.String()[:8], s.token))
	// A missing task answers like a foreign one: nothing to learn from the code.
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, "/api/v1/tasks/"+uuid.New().String(), s.token))
	// Body-addressed route: refused outright to a bound connection.
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodPatch, "/api/v1/vcs-links/ship-sha", s.token))
	assert.Equal(t, before+7, s.violations(t), "each refusal is recorded on the connection")
	assert.Equal(t, 1, s.reached["task"], "the foreign task never reached the handler")
}

func TestExtensionProjectScope_DisablingTheBindingCutsAccess(t *testing.T) {
	s := newScopeFixture(t)
	s.setScope(t, domain.ExtensionProjectScopeBound)
	p := "/api/v1/tasks/" + s.boundTask.String()
	require.Equal(t, http.StatusOK, s.do(http.MethodGet, p, s.token))

	_, err := s.db.ExecContext(context.Background(), `UPDATE project_integrations SET enabled = false WHERE project_id = $1 AND extension_id = 'gitlab'`, s.boundProject)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, s.do(http.MethodGet, p, s.token))
}

func TestExtensionProjectScope_ProjectListAndRepo(t *testing.T) {
	s := newScopeFixture(t)
	ctx := context.Background()
	s.setScope(t, domain.ExtensionProjectScopeBound)

	all, ids, err := s.scope.BoundProjects(ctx, s.conn.ID)
	require.NoError(t, err)
	assert.False(t, all)
	assert.Equal(t, []uuid.UUID{s.boundProject}, ids)

	ok, err := s.scope.AllowsProjectFor(ctx, s.ws.ID, "gitlab", s.otherProject)
	require.NoError(t, err)
	assert.False(t, ok)
	// No connection row (instance-level extension): never bound.
	ok, err = s.scope.AllowsProjectFor(ctx, s.ws.ID, "no-such-ext", s.otherProject)
	require.NoError(t, err)
	assert.True(t, ok)

	connID := s.conn.ID
	pg := pagination.Params{}
	pg.Normalize()
	page, err := postgres.NewProjectRepo(s.db).List(ctx, s.ws.ID, repository.ProjectFilter{ExtensionConnectionID: &connID}, pg)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, s.boundProject, page.Items[0].ID)

	err = s.scope.SetProjectScope(ctx, s.ws.ID, "gitlab", "everything")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "project_scope") || strings.Contains(strings.ToLower(err.Error()), "validation"))
}
