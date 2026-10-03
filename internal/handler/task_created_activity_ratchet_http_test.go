package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// REST-entry legs of the task.created ratchet — the companion to
// internal/service/task_created_activity_ratchet_db_test.go, which drives the
// service-layer creation paths. Two HTTP paths create tasks in production
// traffic: the UI's POST with a user JWT and the agent-key POST with an
// X-Agent-Key header. The service suite proves TaskService.Create journals for
// the actor-context shapes those handlers produce; this file proves the entry
// points themselves — TaskHandler.Create's actor extraction (mw.IsAgent →
// agent, else the JWT user) behind the production middleware chain — land a
// task.created activity_log row attributed to the right actor. A refactor that
// drops actor propagation goes red here on attribution, not just on absence.
//
// The router below is the production slice, same chain and order as
// cmd/api/main.go (DualAuth → WorkspaceRLS → RequireWorkspaceMemberScoped →
// RequireProjectMember → rbac PermCreateTask), so the 201s also prove the
// rights model admits both creator kinds on this route.
//
// Same untagged-CI contract as the service suite: runs in the test job
// against the migrated DATABASE_URL on every pipeline, skips locally when no
// Postgres is reachable.

type ratchetHTTPEnv struct {
	db        *sqlx.DB
	server    *httptest.Server
	client    *http.Client
	projectID uuid.UUID
	userID    uuid.UUID
	agentID   uuid.UUID
	apiKey    string
	jwt       string
}

func newRatchetHTTPEnv(t *testing.T) *ratchetHTTPEnv {
	t.Helper()
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	// Production wiring, same slice as agent_short_tag_db_test.go.
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	activityLogRepo := postgres.NewActivityLogRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	projectMemberRepo := postgres.NewProjectMemberRepo(db)
	refreshTokenRepo := postgres.NewRefreshTokenRepo(db)

	agentSvc := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo, userRepo)
	authSvc := auth.NewService(userRepo, refreshTokenRepo, workspaceRepo, workspaceMemberRepo, "ratchet-http-e2e-test-jwt-secret")
	taskSvc := service.NewTaskService(
		postgres.NewTaskRepo(db), postgres.NewTaskStatusRepo(db), nil, nil,
		service.WithProjectRepo(projectRepo),
	)
	taskHandler := NewTaskHandler(taskSvc)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	api.POST("/projects/:proj_id/tasks", taskHandler.Create,
		mw.RequireProjectMember(db), mw.RequirePermission(mw.PermCreateTask, workspaceMemberRepo))

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	// The workspace comes from the real registration path (owner JWT, default
	// workspace), not a hand-inserted row — DualAuth must accept this exact
	// token shape.
	email := fmt.Sprintf("ratchet-http-%s@e2e.example.com", suffix)
	user, tokens, err := authSvc.Register(ctx, email, "Correct-Horse-Battery-Staple-1", "ratchet-http-"+suffix)
	require.NoError(t, err)
	ws, err := workspaceRepo.ListForUser(ctx, user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, ws, "Register must create a default workspace")
	wsID := ws[0].ID

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: wsID, Name: "ratchet-http-proj", Slug: "ratchet-http-proj-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, projectRepo.Create(ctx, proj))
	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, status))

	// The agent is registered the way the production route does and gets the
	// plaintext API key back exactly once — the header the agent-key POST
	// presents. Its project membership row is what RequireProjectMember asks
	// for on a :proj_id route.
	reg, err := agentSvc.Register(ctx, service.RegisterAgentInput{
		WorkspaceID: wsID, Name: "ratchet-http-agent-" + suffix, AgentType: domain.AgentTypeCustom,
	})
	require.NoError(t, err)
	require.NotNil(t, reg.Agent)
	require.NotEmpty(t, reg.APIKey, "agent registration must return the plaintext key once")
	require.NoError(t, projectMemberRepo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: proj.ID, AgentID: &reg.Agent.ID,
		Role: "member", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	// The human leg is not exempt either: RequireProjectMember wants a row for
	// the owner too when the project was seeded directly instead of via the API.
	require.NoError(t, projectMemberRepo.Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: proj.ID, UserID: &user.ID,
		Role: "member", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM activity_log WHERE workspace_id = $1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", reg.Agent.ID)
	})

	return &ratchetHTTPEnv{
		db: db, server: server, client: &http.Client{Timeout: 30 * time.Second},
		projectID: proj.ID, userID: user.ID, agentID: reg.Agent.ID,
		apiKey: reg.APIKey, jwt: tokens.AccessToken,
	}
}

// ratchetPOST fires the real create route with the given credential header
// setup and returns (status, decoded body).
func (env *ratchetHTTPEnv) ratchetPOST(t *testing.T, title string, asAgentKey bool) (status int, body map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"title": title})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost,
		env.server.URL+"/api/v1/projects/"+env.projectID.String()+"/tasks", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if asAgentKey {
		req.Header.Set("X-Agent-Key", env.apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+env.jwt)
	}
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out := map[string]any{}
	if resp.ContentLength != 0 {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp.StatusCode, out
}

// requireEvent asserts the task.created row exists for the task and is
// attributed to the expected actor — presence AND attribution, because an
// event written with a lost actor would still "exist".
func (env *ratchetHTTPEnv) requireEvent(t *testing.T, taskID uuid.UUID, wantActorType string, wantActorID uuid.UUID, what string) {
	t.Helper()
	var row struct {
		ActorType string    `db:"actor_type"`
		ActorID   uuid.UUID `db:"actor_id"`
	}
	err := env.db.GetContext(context.Background(), &row,
		`SELECT actor_type, actor_id FROM activity_log WHERE entity_id = $1 AND action = 'task.created'`, taskID)
	require.NoError(t, err, "%s: task %s has no task.created row — the ratchet defect class", what, taskID)
	assert.Equal(t, wantActorType, row.ActorType, "%s: event actor_type", what)
	if wantActorID != uuid.Nil {
		assert.Equal(t, wantActorID, row.ActorID, "%s: event actor_id", what)
	}
}

func TestTaskCreatedActivityRatchetRESTEntryPoints(t *testing.T) {
	env := newRatchetHTTPEnv(t)

	t.Run("human UI POST with JWT", func(t *testing.T) {
		status, out := env.ratchetPOST(t, "ratchet http: human leg", false)
		require.Equal(t, http.StatusCreated, status, "body: %v", out)
		id, err := uuid.Parse(out["id"].(string))
		require.NoError(t, err, "response must carry the created task id: %v", out)
		env.requireEvent(t, id, "user", env.userID, "human UI POST")
	})

	t.Run("agent POST with X-Agent-Key", func(t *testing.T) {
		status, out := env.ratchetPOST(t, "ratchet http: agent leg", true)
		require.Equal(t, http.StatusCreated, status, "body: %v", out)
		id, err := uuid.Parse(out["id"].(string))
		require.NoError(t, err, "response must carry the created task id: %v", out)
		env.requireEvent(t, id, "agent", env.agentID, "agent X-Agent-Key POST")
	})
}
