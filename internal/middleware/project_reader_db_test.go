package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Project reader against real Postgres: the key issued by ProjectReaderService
// authenticates through the real (cached) agent service and DualAuth, the
// reader_project_id column reaches the context, and ProjectReaderGuard with the
// real RequestProjects admits only the reader's own task comments. A plain
// viewer agent of the same project and the same routes is the control.

func readerDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for real PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type readerFixture struct {
	db                *sqlx.DB
	ws, otherWS       *domain.Workspace
	ownProj, foreignP uuid.UUID
	otherWSProj       uuid.UUID
	ownTask, fTask    uuid.UUID
	agentRepo         *postgres.AgentRepo
	agents            service.AgentService
	readers           *service.ProjectReaderService
	ownerID           uuid.UUID
}

func newReaderFixture(t *testing.T) *readerFixture {
	t.Helper()
	db := readerDB(t)
	ctx := context.Background()
	sfx := uuid.New().String()[:8]
	userRepo := postgres.NewUserRepo(db)
	wsRepo := postgres.NewWorkspaceRepo(db)
	f := &readerFixture{db: db}
	mkWS := func(tag string) *domain.Workspace {
		owner := &domain.User{ID: uuid.New(), Email: tag + "-" + sfx + "@example.com", PasswordHash: "x", Name: tag, Username: tag + "-" + sfx, IsActive: true}
		require.NoError(t, userRepo.Create(ctx, owner))
		ws := &domain.Workspace{ID: uuid.New(), Name: tag, Slug: tag + "-" + sfx, OwnerID: owner.ID}
		require.NoError(t, wsRepo.Create(ctx, ws))
		if f.ownerID == uuid.Nil {
			f.ownerID = owner.ID
		}
		return ws
	}
	f.ws, f.otherWS = mkWS("rdr"), mkWS("rdrx")
	mkProj := func(ws uuid.UUID) (uuid.UUID, uuid.UUID) {
		p, st, task := uuid.New(), uuid.New(), uuid.New()
		_, err := db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, p, ws, "p-"+p.String()[:8])
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Todo','todo','todo'::task_status_category,0)`, st, p)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'t',1,$4)`, task, p, st, uuid.New())
		require.NoError(t, err)
		return p, task
	}
	f.ownProj, f.ownTask = mkProj(f.ws.ID)
	f.foreignP, f.fTask = mkProj(f.ws.ID)
	f.otherWSProj, _ = mkProj(f.otherWS.ID)
	t.Cleanup(func() {
		c := context.Background()
		for _, ws := range []uuid.UUID{f.ws.ID, f.otherWS.ID} {
			_, _ = db.ExecContext(c, `DELETE FROM project_members WHERE workspace_id = $1`, ws)
			_, _ = db.ExecContext(c, `DELETE FROM agent_workspace_grants WHERE workspace_id = $1`, ws)
			_, _ = db.ExecContext(c, `DELETE FROM agents WHERE workspace_id = $1`, ws)
			_, _ = db.ExecContext(c, `DELETE FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, ws)
			_, _ = db.ExecContext(c, `DELETE FROM task_statuses WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, ws)
			_, _ = db.ExecContext(c, `DELETE FROM projects WHERE workspace_id = $1`, ws)
		}
	})

	f.agentRepo = postgres.NewAgentRepo(db)
	inner := service.NewAgentService(f.agentRepo, postgres.NewActivityLogRepo(db), wsRepo, userRepo)
	inner.(interface {
		SetAgentWorkspaceGrantRepo(repo repository.AgentWorkspaceGrantRepository)
	}).SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	// The cached service, as main wires it: Delete evicts its entries.
	f.agents = service.NewCachedAgentAuth(inner, time.Minute)
	f.readers = service.NewProjectReaderService(wsRepo, userRepo, f.agentRepo, f.agentRepo)
	return f
}

// server mounts DualAuth and the two agent guards exactly as the protected
// group does, with stand-in handlers.
func (f *readerFixture) server() *echo.Echo {
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(middleware.DualAuth(nil, f.agents, nil))
	g.Use(middleware.AgentReadOnlyGuard())
	g.Use(middleware.ProjectReaderGuard(func(ctx context.Context, c echo.Context) ([]uuid.UUID, bool, error) {
		return middleware.RequestProjects(ctx, f.db, c)
	}))
	ok := func(c echo.Context) error { return c.String(http.StatusOK, "handler-reached") }
	g.GET("/agents/me", ok)
	g.GET("/tasks/:task_id/comments", ok)
	g.POST("/tasks/:task_id/comments", ok)
	g.GET("/tasks/:task_id", ok)
	g.GET("/memories/:id", ok)
	g.GET("/memories", ok)
	g.GET("/projects/:proj_id/knowledge", ok)
	return e
}

func get(e *echo.Echo, method, path, key string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, http.NoBody)
	if key != "" {
		req.Header.Set("X-Agent-Key", key)
	}
	e.ServeHTTP(rec, req)
	return rec
}

func TestProjectReader_IssueAuthenticateAndGuard_RealPostgres(t *testing.T) {
	f := newReaderFixture(t)
	ctx := context.Background()
	owner := f.ownerID
	out, err := f.readers.Issue(ctx, service.IssueProjectReaderInput{WorkspaceID: f.ws.ID, ProjectID: f.ownProj, Name: "Consent Reader " + f.ws.Slug, IssuedBy: &owner})
	require.NoError(t, err)

	// Stored: the column, one viewer grant, one viewer membership — re-read.
	stored, err := f.agentRepo.GetByID(ctx, out.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ReaderProjectID)
	assert.Equal(t, f.ownProj, *stored.ReaderProjectID)
	assert.JSONEq(t, `[]`, string(stored.AcceptsFrom))
	assert.JSONEq(t, `{"no_lane":true}`, string(stored.Capabilities))
	var grant []string
	require.NoError(t, f.db.Select(&grant, `SELECT role::text FROM agent_workspace_grants WHERE agent_id = $1`, out.Agent.ID))
	assert.Equal(t, []string{"viewer"}, grant)
	var member []string
	require.NoError(t, f.db.Select(&member, `SELECT project_id::text || ':' || role::text FROM project_members WHERE agent_id = $1`, out.Agent.ID))
	assert.Equal(t, []string{f.ownProj.String() + ":viewer"}, member)

	// An ordinary update keeps the marker (no UPDATE writes the column).
	stored.ProfileDescription = "edited"
	stored.Capabilities = []byte(`{}`)
	require.NoError(t, f.agentRepo.Update(ctx, stored))
	again, err := f.agentRepo.GetByID(ctx, out.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, again.ReaderProjectID, "an agent update cleared reader_project_id")

	e := f.server()
	key := out.APIKey
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/agents/me", http.StatusOK},
		{http.MethodGet, "/api/v1/tasks/" + f.ownTask.String() + "/comments", http.StatusOK},
		{http.MethodGet, "/api/v1/tasks/" + f.ownTask.String()[:8] + "/comments", http.StatusOK},
		{http.MethodPost, "/api/v1/tasks/" + f.ownTask.String() + "/comments", http.StatusForbidden},
		{http.MethodGet, "/api/v1/tasks/" + f.fTask.String() + "/comments", http.StatusForbidden},
		{http.MethodGet, "/api/v1/tasks/" + uuid.NewString() + "/comments", http.StatusForbidden},
		{http.MethodGet, "/api/v1/tasks/not-an-id/comments", http.StatusForbidden},
		{http.MethodGet, "/api/v1/tasks/" + f.ownTask.String(), http.StatusForbidden},
		{http.MethodGet, "/api/v1/memories/" + uuid.NewString(), http.StatusForbidden},
		{http.MethodGet, "/api/v1/memories?scope=workspace", http.StatusForbidden},
		{http.MethodGet, "/api/v1/memories?project_id=" + f.ownProj.String(), http.StatusForbidden},
		{http.MethodGet, "/api/v1/projects/" + f.ownProj.String() + "/knowledge", http.StatusForbidden},
	} {
		rec := get(e, tc.method, tc.path, key)
		assert.Equal(t, tc.want, rec.Code, "reader %s %s: %s", tc.method, tc.path, rec.Body.String())
		if tc.want == http.StatusForbidden && tc.method != http.MethodPost {
			assert.Contains(t, rec.Body.String(), middleware.ProjectReaderMessage, "%s %s refused by another guard", tc.method, tc.path)
		}
	}
	assert.Equal(t, http.StatusUnauthorized, get(e, http.MethodGet, "/api/v1/tasks/"+f.ownTask.String()+"/comments", "").Code)

	// Control: a plain viewer agent of the same project (same grant role, same
	// membership) is untouched by the new guard — it still reaches the memory
	// and foreign-task handlers. This is the unchanged plain viewer.
	plain, err := f.agents.Register(ctx, service.RegisterAgentInput{WorkspaceID: f.ws.ID, Name: "Plain Viewer " + f.ws.Slug, AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET role = 'viewer' WHERE agent_id = $1`, plain.Agent.ID)
	require.NoError(t, err)
	for _, p := range []string{"/api/v1/memories/" + uuid.NewString(), "/api/v1/tasks/" + f.fTask.String() + "/comments", "/api/v1/memories?scope=workspace"} {
		rec := get(e, http.MethodGet, p, plain.APIKey)
		assert.Equal(t, "handler-reached", rec.Body.String(), "plain viewer %s: %d", p, rec.Code)
	}
	assert.Equal(t, http.StatusForbidden, get(e, http.MethodPost, "/api/v1/tasks/"+f.ownTask.String()+"/comments", plain.APIKey).Code)

	// Revocation: soft delete + cache eviction, the key stops at once.
	require.NoError(t, f.agents.Delete(ctx, out.Agent.ID))
	assert.Equal(t, http.StatusUnauthorized, get(e, http.MethodGet, "/api/v1/agents/me", key).Code)
}

func TestProjectReader_IssueIsAtomicAndWorkspaceBound_RealPostgres(t *testing.T) {
	f := newReaderFixture(t)
	ctx := context.Background()
	count := func() int {
		var n int
		require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM agents WHERE workspace_id = $1`, f.ws.ID))
		return n
	}
	before := count()

	// A project of another workspace, an unknown project, a deleted project:
	// NotFound and nothing written.
	_, err := f.db.Exec(`UPDATE projects SET deleted_at = now() WHERE id = $1`, f.foreignP)
	require.NoError(t, err)
	for _, p := range []uuid.UUID{f.otherWSProj, uuid.New(), f.foreignP} {
		_, ierr := f.readers.Issue(ctx, service.IssueProjectReaderInput{WorkspaceID: f.ws.ID, ProjectID: p, Name: "R " + p.String()[:8]})
		var apiErr *apierror.Error
		require.ErrorAs(t, ierr, &apiErr, "project %s", p)
		assert.Equal(t, http.StatusNotFound, apiErr.StatusCode(), "project %s", p)
	}
	assert.Equal(t, before, count(), "a refused issuance wrote an agent")
	var grants int
	require.NoError(t, f.db.Get(&grants, `SELECT count(*) FROM agent_workspace_grants WHERE workspace_id = $1`, f.ws.ID))
	assert.Zero(t, grants, "a refused issuance wrote a grant")

	// Missing name / project are validation errors; a duplicate handle conflicts.
	_, err = f.readers.Issue(ctx, service.IssueProjectReaderInput{WorkspaceID: f.ws.ID, ProjectID: f.ownProj, Name: "  "})
	require.Error(t, err)
	_, err = f.readers.Issue(ctx, service.IssueProjectReaderInput{WorkspaceID: f.ws.ID, Name: "No Project"})
	require.Error(t, err)
	_, err = f.readers.Issue(ctx, service.IssueProjectReaderInput{WorkspaceID: f.ws.ID, ProjectID: f.ownProj, Name: "Dup Reader"})
	require.NoError(t, err)
	_, err = f.readers.Issue(ctx, service.IssueProjectReaderInput{WorkspaceID: f.ws.ID, ProjectID: f.ownProj, Name: "Dup Reader"})
	require.Error(t, err)
	assert.Equal(t, before+1, count())
}
