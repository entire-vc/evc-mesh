package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// An extension actor reads a project's statuses through the same guard chain
// cmd/api mounts on the protected group: ext_ token -> capability guard ->
// project-scope guard -> workspace guards -> project guard -> real handler ->
// real Postgres. DB-backed, skipped when Postgres is unreachable (same
// convention as the other *_db_test.go files).
func TestTaskStatusList_ExtensionActor_DB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	now := time.Now()

	users := postgres.NewUserRepo(db)
	workspaces := postgres.NewWorkspaceRepo(db)
	mkWorkspace := func(tag string) *domain.Workspace {
		u := &domain.User{ID: uuid.New(), Email: "st-" + tag + "-" + suffix + "@example.com", PasswordHash: "x", Name: "Owner", Username: "st-" + tag + "-" + suffix, IsActive: true}
		require.NoError(t, users.Create(ctx, u))
		ws := &domain.Workspace{ID: uuid.New(), Name: "st-" + tag, Slug: "st-" + tag + "-" + suffix, OwnerID: u.ID}
		require.NoError(t, workspaces.Create(ctx, ws))
		return ws
	}
	projects := postgres.NewProjectRepo(db)
	mkProject := func(ws *domain.Workspace, slug string, statuses ...string) uuid.UUID {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: slug, Slug: slug + "-" + suffix,
			DefaultAssigneeType: domain.DefaultAssigneeNone, Settings: []byte("{}"), CreatedAt: now, UpdatedAt: now}
		require.NoError(t, projects.Create(ctx, p))
		for i, name := range statuses {
			_, insErr := db.ExecContext(ctx, `INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,$3,$4,'todo',$5)`,
				uuid.New(), p.ID, name, strings.ToLower(name), i)
			require.NoError(t, insErr)
		}
		return p.ID
	}

	home, foreign := mkWorkspace("home"), mkWorkspace("foreign")
	// The bound project has statuses and no task: the case GET /tasks/:task_id/statuses cannot serve.
	bound := mkProject(home, "bound", "Inbox", "Shipped")
	unbound := mkProject(home, "unbound", "Hiddenhome")
	other := mkProject(foreign, "other", "Foreignws")
	var tasks int
	require.NoError(t, db.GetContext(ctx, &tasks, `SELECT count(*) FROM tasks WHERE project_id = $1`, bound))
	require.Zero(t, tasks)

	integ := postgres.NewIntegrationRepo(db)
	actors := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, workspaces)
	connect := func(provider domain.IntegrationProvider, consented []string) (*domain.IntegrationConfig, string) {
		conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: home.ID, Provider: provider, IsActive: true, CreatedAt: now, UpdatedAt: now}
		require.NoError(t, integ.Upsert(ctx, conn))
		issued, issueErr := actors.Issue(ctx, home.ID, conn.ID, string(provider), "Ext", consented)
		require.NoError(t, issueErr)
		return conn, issued.Token
	}
	reader, readerTok := connect(domain.IntegrationProviderGitLab, []string{"api:projects:read", "api:projects:write"})
	noCap, noCapTok := connect(domain.IntegrationProviderGitHub, []string{"api:tasks:read"})
	violations := func(conn *domain.IntegrationConfig) int64 {
		row, getErr := actors.Get(ctx, conn.ID)
		require.NoError(t, getErr)
		require.NotNil(t, row)
		return row.ViolationCount
	}

	scope := postgres.NewExtensionProjectScopeRepo(db)
	_, err = db.ExecContext(ctx, `INSERT INTO project_integrations (project_id, type, extension_id, enabled) VALUES ($1,'gitlab','gitlab',true)`, bound)
	require.NoError(t, err)

	statusRepo := postgres.NewTaskStatusRepo(db)
	h := NewTaskStatusHandler(service.NewTaskStatusService(statusRepo, postgres.NewTaskRepo(db), postgres.NewActivityLogRepo(db)))
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(middleware.ExtensionAuth(actors))
	api.Use(middleware.ExtensionCapabilityGuard(actors))
	api.Use(middleware.ExtensionProjectScopeGuard(db, scope, actors))
	api.Use(middleware.WorkspaceRLS(db, projects))
	api.Use(middleware.RequireWorkspaceMemberScoped(db))
	api.Use(middleware.RequireTaskProjectMember(db))
	projAccess := middleware.RequireProjectMember(db)
	manage := middleware.RequirePermission(middleware.PermManageProject, postgres.NewWorkspaceMemberRepo(db))
	api.GET("/projects/:proj_id/statuses", h.List, projAccess)
	api.POST("/projects/:proj_id/statuses", h.Create, projAccess, manage)
	api.PATCH("/projects/:proj_id/statuses/:status_id", h.Update, projAccess, manage)
	api.PUT("/projects/:proj_id/statuses/reorder", h.Reorder, projAccess, manage)

	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, http.NoBody)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	statusesOf := func(p uuid.UUID) string { return "/api/v1/projects/" + p.String() + "/statuses" }
	names := func(rec *httptest.ResponseRecorder) []string {
		var got []domain.TaskStatus
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
		out := make([]string, 0, len(got))
		for _, s := range got {
			out = append(out, s.Name)
		}
		return out
	}

	// Workspace-scoped connection (the default): every project of its own workspace,
	// never one of another workspace.
	rec := do(http.MethodGet, statusesOf(bound), readerTok, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"Inbox", "Shipped"}, names(rec))
	rec = do(http.MethodGet, statusesOf(unbound), readerTok, "")
	require.Equal(t, http.StatusOK, rec.Code, "positive control for the bound refusal below: %s", rec.Body.String())
	assert.Equal(t, []string{"Hiddenhome"}, names(rec))
	rec = do(http.MethodGet, statusesOf(other), readerTok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, "project of another workspace")
	assert.NotContains(t, rec.Body.String(), "Foreignws")

	// Project-bound connection: only its bound projects, each refusal counted.
	require.NoError(t, scope.SetProjectScope(ctx, home.ID, "gitlab", domain.ExtensionProjectScopeBound))
	rec = do(http.MethodGet, statusesOf(bound), readerTok, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"Inbox", "Shipped"}, names(rec))
	before := violations(reader)
	rec = do(http.MethodGet, statusesOf(unbound), readerTok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, "unbound project")
	assert.NotContains(t, rec.Body.String(), "Hiddenhome")
	rec = do(http.MethodGet, statusesOf(other), readerTok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, "project of another workspace")
	assert.NotContains(t, rec.Body.String(), "Foreignws")
	assert.Equal(t, before+2, violations(reader), "each scope refusal is counted")

	// Without projects:read: refused and counted as a violation.
	require.Equal(t, int64(0), violations(noCap))
	rec = do(http.MethodGet, statusesOf(bound), noCapTok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, "no projects:read")
	assert.NotContains(t, rec.Body.String(), "Inbox")
	assert.Equal(t, int64(1), violations(noCap))

	// Writes stay closed, even with projects:write, and change nothing.
	var first uuid.UUID
	require.NoError(t, db.GetContext(ctx, &first, `SELECT id FROM task_statuses WHERE project_id = $1 ORDER BY position LIMIT 1`, bound))
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPost, statusesOf(bound), `{"name":"Extra","category":"todo"}`},
		{http.MethodPatch, statusesOf(bound) + "/" + first.String(), `{"name":"Renamed"}`},
		{http.MethodPut, statusesOf(bound) + "/reorder", `{"status_ids":[]}`},
	} {
		assert.Equal(t, http.StatusForbidden, do(w.method, w.path, readerTok, w.body).Code, "%s %s", w.method, w.path)
	}
	after, err := statusRepo.ListByProject(ctx, bound)
	require.NoError(t, err)
	require.Len(t, after, 2)
	assert.Equal(t, "Inbox", after[0].Name)

	// Disconnected connection: the token no longer authenticates.
	off := false
	_, err = integ.Update(ctx, reader.ID, domain.UpdateIntegrationInput{IsActive: &off})
	require.NoError(t, err)
	rec = do(http.MethodGet, statusesOf(bound), readerTok, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "disconnected connection")
	assert.NotContains(t, rec.Body.String(), "Inbox")
}
