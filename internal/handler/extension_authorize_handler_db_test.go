package handler

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// DB-backed, no build tag: skipped when Postgres is not reachable. The chain is
// the production one — ExtensionAuth -> ExtensionCapabilityGuard -> handler —
// over real rows, because "removed from the project → deny at once" is a claim
// about reading the database, which a mock cannot make.

func extAuthorizeDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type extAuthzEnv struct {
	db      *sqlx.DB
	e       *echo.Echo
	token   string
	ws      uuid.UUID
	project uuid.UUID
	task    uuid.UUID
	// another workspace with its own project and task
	otherWS, otherProject, otherTask uuid.UUID
}

func (env *extAuthzEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := env.db.ExecContext(context.Background(), q, args...)
	require.NoError(t, err)
}

func (env *extAuthzEnv) user(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	s := id.String()[:8]
	env.exec(t, `INSERT INTO users (id, email, password_hash, display_name, username, is_active) VALUES ($1,$2,'x',$3,$4,true)`,
		id, "authz-"+s+"@example.com", "authz-"+s, "authz-"+s)
	return id
}

func (env *extAuthzEnv) workspace(t *testing.T, owner uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	env.exec(t, `INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'authz',$2,$3)`, id, "authz-"+id.String()[:8], owner)
	env.member(t, id, owner, domain.RoleOwner)
	return id
}

func (env *extAuthzEnv) member(t *testing.T, ws, user uuid.UUID, role string) {
	t.Helper()
	env.exec(t, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4)`, uuid.New(), ws, user, role)
}

func (env *extAuthzEnv) projectMember(t *testing.T, ws, project, user uuid.UUID, role string) {
	t.Helper()
	env.exec(t, `INSERT INTO project_members (id, project_id, user_id, workspace_id, role) VALUES ($1,$2,$3,$4,$5)`, uuid.New(), project, user, ws, role)
}

func (env *extAuthzEnv) projectWithTask(t *testing.T, ws, creator uuid.UUID) (project, task uuid.UUID) {
	t.Helper()
	project, status, task := uuid.New(), uuid.New(), uuid.New()
	env.exec(t, `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,$3,$3)`, project, ws, "authz-"+project.String()[:8])
	env.exec(t, `INSERT INTO task_statuses (id, project_id, name, slug, category) VALUES ($1,$2,'Todo','todo','todo')`, status, project)
	env.exec(t, `INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'t',1,$4)`, task, project, status, creator)
	return project, task
}

func newExtAuthzEnv(t *testing.T, consented []string) *extAuthzEnv {
	t.Helper()
	db := extAuthorizeDB(t)
	env := &extAuthzEnv{db: db, e: echo.New()}
	ctx := context.Background()

	owner := env.user(t)
	env.ws = env.workspace(t, owner)
	env.project, env.task = env.projectWithTask(t, env.ws, owner)
	otherOwner := env.user(t)
	env.otherWS = env.workspace(t, otherOwner)
	env.otherProject, env.otherTask = env.projectWithTask(t, env.otherWS, otherOwner)

	integ := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: env.ws, Provider: domain.IntegrationProviderGitLab, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, conn))
	svc := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, postgres.NewWorkspaceRepo(db))
	issued, err := svc.Issue(ctx, env.ws, conn.ID, "gitlab", "GitLab", consented)
	require.NoError(t, err)
	env.token = issued.Token

	g := env.e.Group("/api/v1")
	g.Use(middleware.ExtensionAuth(svc))
	g.Use(middleware.ExtensionCapabilityGuard(svc))
	g.Use(middleware.ExtensionProjectScopeGuard(db, postgres.NewExtensionProjectScopeRepo(db), svc))
	g.POST("/ext/me/authorize", NewExtensionAuthorizeHandler(db, postgres.NewExtensionProjectScopeRepo(db), svc).Authorize)
	return env
}

func (env *extAuthzEnv) ask(t *testing.T, body map[string]any) (code int, out map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ext/me/authorize", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	out = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func allowed(t *testing.T, env *extAuthzEnv, body map[string]any) (ok bool, reason string) {
	t.Helper()
	code, out := env.ask(t, body)
	require.Equal(t, http.StatusOK, code, "%v", out)
	reason, _ = out["reason"].(string)
	return out["allowed"] == true, reason
}

// A user taken out of the project is denied by the very next call: the answer
// comes from project_members, not from anything the user was issued earlier.
func TestExtAuthorize_RemovedFromProjectDeniedAtOnce(t *testing.T) {
	env := newExtAuthzEnv(t, []string{"api:members:authorize"})
	u := env.user(t)
	env.member(t, env.ws, u, domain.RoleMember)
	env.projectMember(t, env.ws, env.project, u, domain.ProjectRoleMember)

	q := map[string]any{"user_id": u, "action": "write", "project_id": env.project}
	ok, reason := allowed(t, env, q)
	assert.True(t, ok)
	assert.Equal(t, "project_role", reason)
	ok, _ = allowed(t, env, map[string]any{"user_id": u, "action": "read", "task_id": env.task})
	assert.True(t, ok, "a task resolves to its project")

	env.exec(t, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, env.project, u)
	ok, reason = allowed(t, env, q)
	assert.False(t, ok)
	assert.Equal(t, "not_project_member", reason)
	ok, _ = allowed(t, env, map[string]any{"user_id": u, "action": "read", "task_id": env.task})
	assert.False(t, ok)
	// Still a workspace member: the workspace-level question is still yes.
	ok, _ = allowed(t, env, map[string]any{"user_id": u, "action": "read"})
	assert.True(t, ok)

	env.exec(t, `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, env.ws, u)
	ok, reason = allowed(t, env, map[string]any{"user_id": u, "action": "read"})
	assert.False(t, ok)
	assert.Equal(t, "not_workspace_member", reason)
}

func TestExtAuthorize_OwnerAdminAllowedWithoutProjectRow(t *testing.T) {
	env := newExtAuthzEnv(t, []string{"api:members:authorize"})
	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin} {
		u := env.user(t)
		env.member(t, env.ws, u, role)
		for _, action := range []string{"read", "write", "manage"} {
			ok, reason := allowed(t, env, map[string]any{"user_id": u, "action": action, "project_id": env.project})
			assert.True(t, ok, "%s %s", role, action)
			assert.Equal(t, "workspace_role", reason)
		}
	}
}

func TestExtAuthorize_RolesAndDisabledAccount(t *testing.T) {
	env := newExtAuthzEnv(t, []string{"api:members:authorize"})

	viewer := env.user(t)
	env.member(t, env.ws, viewer, domain.RoleMember)
	env.projectMember(t, env.ws, env.project, viewer, domain.ProjectRoleViewer)
	ok, _ := allowed(t, env, map[string]any{"user_id": viewer, "action": "read", "project_id": env.project})
	assert.True(t, ok)
	ok, reason := allowed(t, env, map[string]any{"user_id": viewer, "action": "write", "project_id": env.project})
	assert.False(t, ok)
	assert.Equal(t, "insufficient_role", reason)

	// Project admin may manage its project, but a workspace viewer stays read-only
	// whatever its project row says.
	projAdmin := env.user(t)
	env.member(t, env.ws, projAdmin, domain.RoleMember)
	env.projectMember(t, env.ws, env.project, projAdmin, domain.ProjectRoleAdmin)
	ok, _ = allowed(t, env, map[string]any{"user_id": projAdmin, "action": "manage", "project_id": env.project})
	assert.True(t, ok)
	ok, _ = allowed(t, env, map[string]any{"user_id": projAdmin, "action": "manage"})
	assert.False(t, ok, "project admin is not a workspace admin")

	wsViewer := env.user(t)
	env.member(t, env.ws, wsViewer, domain.RoleViewer)
	env.projectMember(t, env.ws, env.project, wsViewer, domain.ProjectRoleAdmin)
	ok, _ = allowed(t, env, map[string]any{"user_id": wsViewer, "action": "write", "project_id": env.project})
	assert.False(t, ok)

	disabled := env.user(t)
	env.member(t, env.ws, disabled, domain.RoleAdmin)
	ok, _ = allowed(t, env, map[string]any{"user_id": disabled, "action": "read"})
	assert.True(t, ok)
	env.exec(t, `UPDATE users SET is_active = false WHERE id = $1`, disabled)
	ok, reason = allowed(t, env, map[string]any{"user_id": disabled, "action": "read"})
	assert.False(t, ok)
	assert.Equal(t, "user_disabled", reason)
}

// Not an oracle: everything outside the extension's workspace is 404, and a user
// id that does not exist is answered exactly like a user of another workspace.
func TestExtAuthorize_ForeignWorkspaceIs404AndNoUserOracle(t *testing.T) {
	env := newExtAuthzEnv(t, []string{"api:members:authorize"})
	u := env.user(t)
	env.member(t, env.ws, u, domain.RoleOwner)

	for name, body := range map[string]map[string]any{
		"workspace":           {"user_id": u, "action": "read", "workspace_id": env.otherWS},
		"project":             {"user_id": u, "action": "read", "project_id": env.otherProject},
		"task":                {"user_id": u, "action": "read", "task_id": env.otherTask},
		"task of other proj":  {"user_id": u, "action": "read", "task_id": env.task, "project_id": env.otherProject},
		"nonexistent project": {"user_id": u, "action": "read", "project_id": uuid.New()},
	} {
		code, _ := env.ask(t, body)
		assert.Equal(t, http.StatusNotFound, code, name)
	}
	code, _ := env.ask(t, map[string]any{"user_id": u, "action": "read", "workspace_id": env.ws})
	assert.Equal(t, http.StatusOK, code, "own workspace named explicitly")

	stranger := env.user(t)
	env.member(t, env.otherWS, stranger, domain.RoleOwner)
	for _, id := range []uuid.UUID{stranger, uuid.New()} {
		ok, reason := allowed(t, env, map[string]any{"user_id": id, "action": "read"})
		assert.False(t, ok)
		assert.Equal(t, "not_workspace_member", reason)
	}
}

func TestExtAuthorize_BadRequestAndCapability(t *testing.T) {
	env := newExtAuthzEnv(t, []string{"api:members:authorize"})
	for _, body := range []map[string]any{
		{"action": "read"},
		{"user_id": uuid.New(), "action": "delete"},
		{"user_id": uuid.New(), "action": "read", "email": "x@example.com"},
	} {
		code, _ := env.ask(t, body)
		assert.Equal(t, http.StatusBadRequest, code, "%v", body)
	}

	// members:read is not members:authorize.
	noCap := newExtAuthzEnv(t, []string{"api:members:read"})
	code, _ := noCap.ask(t, map[string]any{"user_id": uuid.New(), "action": "read"})
	assert.Equal(t, http.StatusForbidden, code)
}

// A project-bound connection is answered only for its bound projects, by project
// id and by task id; unbinding takes effect on the next call. The workspace-level
// question stays open.
func TestExtAuthorize_ProjectBoundConnection(t *testing.T) {
	env := newExtAuthzEnv(t, []string{"api:members:authorize"})
	u := env.user(t)
	env.member(t, env.ws, u, domain.RoleOwner)
	otherProject, otherTask := env.projectWithTask(t, env.ws, u)

	scope := postgres.NewExtensionProjectScopeRepo(env.db)
	require.NoError(t, scope.SetProjectScope(context.Background(), env.ws, "gitlab", domain.ExtensionProjectScopeBound))
	env.exec(t, `INSERT INTO project_integrations (project_id, type, extension_id, enabled) VALUES ($1,'gitlab','gitlab',true)`, env.project)

	ok, _ := allowed(t, env, map[string]any{"user_id": u, "action": "read", "project_id": env.project})
	assert.True(t, ok, "bound project")
	ok, _ = allowed(t, env, map[string]any{"user_id": u, "action": "read", "task_id": env.task})
	assert.True(t, ok, "task of a bound project")
	ok, _ = allowed(t, env, map[string]any{"user_id": u, "action": "read"})
	assert.True(t, ok, "workspace-level question")

	for name, body := range map[string]map[string]any{
		"unbound project": {"user_id": u, "action": "read", "project_id": otherProject},
		"unbound task":    {"user_id": u, "action": "read", "task_id": otherTask},
	} {
		code, out := env.ask(t, body)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Nil(t, out["allowed"], "%s: no answer about the user", name)
	}
	// Another workspace is still 404, not a scope answer.
	code, _ := env.ask(t, map[string]any{"user_id": u, "action": "read", "project_id": env.otherProject})
	assert.Equal(t, http.StatusNotFound, code)

	// Unbinding refuses at once.
	env.exec(t, `UPDATE project_integrations SET enabled = false WHERE project_id = $1`, env.project)
	code, _ = env.ask(t, map[string]any{"user_id": u, "action": "read", "project_id": env.project})
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = env.ask(t, map[string]any{"user_id": u, "action": "read", "task_id": env.task})
	assert.Equal(t, http.StatusForbidden, code)

	// Back to a workspace-wide connection: every project of the workspace again.
	require.NoError(t, scope.SetProjectScope(context.Background(), env.ws, "gitlab", domain.ExtensionProjectScopeWorkspace))
	ok, _ = allowed(t, env, map[string]any{"user_id": u, "action": "read", "project_id": otherProject})
	assert.True(t, ok)
}
