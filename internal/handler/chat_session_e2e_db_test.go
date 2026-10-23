package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// End-to-end test of the chat-session token (E4.4, Mesh Doc agent-chat-session-token
// v2): real Postgres, the real repos/services, the real middleware chain
// main.go builds for /api/v1 and the real TaskHandler.MoveTask — only the
// transport is httptest. Same convention as oauth_e2e_db_test.go: no build tag,
// skips when no Postgres is reachable (CI provides one).

const chatTestControllerSecret = "chat-e2e-controller-secret"

type chatE2EEnv struct {
	db       *sqlx.DB
	authSvc  *auth.Service
	agentSvc service.AgentService
	chatSvc  service.ChatSessionService
	server   *httptest.Server
	client   *http.Client

	workspaceID uuid.UUID
	projectID   uuid.UUID // P: the chat's project
	otherProj   uuid.UUID // Q: another project of the same workspace
	todoID      uuid.UUID
	doneID      uuid.UUID
	otherTodoID uuid.UUID
	otherDoneID uuid.UUID
	agent       *domain.Agent
	agentKey    string
	ownerID     uuid.UUID
	ownerToken  string // the owner's own (human) JWT
}

func newChatE2EEnv(t *testing.T) *chatE2EEnv {
	t.Helper()
	t.Setenv(mw.EnvChatControllerToken, chatTestControllerSecret)
	db := oauthE2ETestDB(t)

	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	memberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	activityRepo := postgres.NewActivityLogRepo(db)
	grantRepo := postgres.NewAgentWorkspaceGrantRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	projectMemberRepo := postgres.NewProjectMemberRepo(db)

	agentSvc := service.NewAgentService(agentRepo, activityRepo, workspaceRepo, userRepo)
	if configurable, ok := agentSvc.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(grantRepo)
	}
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, memberRepo, "chat-e2e-test-jwt-secret-do-not-use-in-prod")
	chatSvc := service.NewChatSessionService(postgres.NewChatSessionTokenRepo(db), agentSvc, workspaceRepo, memberRepo, projectRepo, projectMemberRepo, grantRepo)

	taskSvc := service.NewTaskService(postgres.NewTaskRepo(db), postgres.NewTaskStatusRepo(db), postgres.NewTaskDependencyRepo(db), activityRepo,
		service.WithProjectRepo(projectRepo))
	taskHandler := NewTaskHandler(taskSvc)
	depHandler := NewDependencyHandler(service.NewTaskDependencyService(postgres.NewTaskDependencyRepo(db), postgres.NewTaskRepo(db), activityRepo, projectRepo), taskSvc)
	activityHandler := NewActivityHandler(service.NewActivityLogService(activityRepo))
	agentHandler := NewAgentHandler(agentSvc)
	projectHandler := NewProjectHandler(service.NewProjectService(projectRepo, postgres.NewTaskStatusRepo(db), activityRepo))
	chatHandler := NewChatSessionHandler(chatSvc)
	memoryHandler := NewMemoryHandler(service.NewMemoryService(postgres.NewMemoryRepo(db), postgres.NewMemoryEdgesRepo(db), nil), memberRepo)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)

	// The /api/v1 chain of cmd/api/main.go, in its order.
	api := e.Group("/api/v1")
	api.Use(mw.ChatSessionAuth(chatSvc))
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	api.Use(mw.ChatSessionProjectScope(db))
	api.POST("/tasks/:task_id/move", taskHandler.MoveTask, mw.RequireWorkspaceMember(db), mw.RequirePermission(mw.PermUpdateTask, memberRepo))
	api.POST("/tasks/:task_id/dependencies", depHandler.Create, mw.RequireWorkspaceMember(db), mw.RequirePermission(mw.PermUpdateTask, memberRepo))
	// Connector-only routes: the user-role half is checked by RequireConnectorPermission, no rbac().
	ownsWorkspace := func(ctx context.Context, wsID, userID uuid.UUID) bool {
		return mw.UserOwnsWorkspace(ctx, db, wsID, userID)
	}
	connectorRBAC := func(perm mw.Permission) echo.MiddlewareFunc {
		return mw.RequireConnectorPermission(perm, memberRepo, ownsWorkspace)
	}
	api.PATCH("/projects/:proj_id", projectHandler.Update, mw.RequireProjectMember(db), connectorRBAC(mw.PermManageProject))
	api.POST("/projects/:proj_id/knowledge", memoryHandler.SetProjectKnowledge, mw.RequireProjectMember(db), connectorRBAC(mw.PermWriteMemory))
	api.GET("/tasks/:task_id/activity", activityHandler.ListByTask)
	api.GET("/agents/me", agentHandler.Me)
	api.GET("/workspaces/:ws_id/projects", projectHandler.List) // workspace-level list: no project of its own
	api.DELETE("/projects/:proj_id", projectHandler.Delete, mw.RequireProjectMember(db), mw.RequirePermission(mw.PermDeleteProject, memberRepo))
	api.GET("/agents/me/tasks", agentHandler.Me) // a route naming no project: must be refused to a chat token
	// One probe route per child path parameter the middleware resolves (plus one it
	// does not know): the real chain runs, only the terminal handler is a stub.
	for _, name := range append(chatChildParams(), "unmapped_id") {
		api.GET("/probe/"+name+"/:"+name, func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	}

	e.POST("/internal/chat-sessions/tokens", chatHandler.Issue, mw.ChatControllerAuth())
	e.POST("/internal/chat-sessions/:session_id/renew", chatHandler.Renew, mw.ChatControllerAuth())
	e.POST("/internal/chat-sessions/:session_id/revoke", chatHandler.Revoke, mw.ChatControllerAuth())

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	env := &chatE2EEnv{db: db, authSvc: authSvc, agentSvc: agentSvc, chatSvc: chatSvc, server: server,
		client: &http.Client{Timeout: 60 * time.Second}}

	ownerTok, ownerID, wsID, _ := (&oauthE2EEnv{db: db, authSvc: authSvc}).registerTestUser(t, "chat-owner")
	env.ownerID, env.workspaceID, env.ownerToken = ownerID, wsID, ownerTok
	env.projectID, env.todoID, env.doneID = env.seedProject(t, "p")
	env.otherProj, env.otherTodoID, env.otherDoneID = env.seedProject(t, "q")

	reg, err := agentSvc.Register(context.Background(), service.RegisterAgentInput{
		WorkspaceID: wsID, Name: "Chat Agent " + uuid.New().String()[:8], AgentType: "custom",
	})
	require.NoError(t, err)
	env.agent, env.agentKey = reg.Agent, reg.APIKey
	env.addAgentToProject(t, env.projectID)
	env.addAgentToProject(t, env.otherProj)
	return env
}

func (env *chatE2EEnv) seedProject(t *testing.T, slugPrefix string) (projectID, todoID, doneID uuid.UUID) {
	t.Helper()
	projectID, todoID, doneID = uuid.New(), uuid.New(), uuid.New()
	_, err := env.db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, $3, $4)`,
		projectID, env.workspaceID, "Chat "+slugPrefix, fmt.Sprintf("chat-%s-%s", slugPrefix, uuid.New().String()[:8]))
	require.NoError(t, err)
	for _, st := range []struct {
		id        uuid.UUID
		slug, cat string
		pos       int
	}{{todoID, "todo", "todo", 0}, {doneID, "done", "done", 1}} {
		_, err = env.db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1, $2, $3, $4, $5::task_status_category, $6)`,
			st.id, projectID, st.slug, st.slug, st.cat, st.pos)
		require.NoError(t, err)
	}
	return projectID, todoID, doneID
}

// newTask inserts a todo task into project and returns its id.
func (env *chatE2EEnv) newTask(t *testing.T, projectID, todoID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.db.Exec(`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1, $2, $3, 'chat task', (SELECT COALESCE(MAX(task_number),0)+1 FROM tasks WHERE project_id = $2), $4)`,
		id, projectID, todoID, env.ownerID)
	require.NoError(t, err)
	return id
}

func (env *chatE2EEnv) addAgentToProject(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, agent_id) VALUES ($1, $2, $3, $4)`,
		uuid.New(), projectID, env.workspaceID, env.agent.ID)
	require.NoError(t, err)
}

// newUser registers a user and adds it to the workspace (and project P) with role.
func (env *chatE2EEnv) newUser(t *testing.T, prefix, role string) uuid.UUID {
	t.Helper()
	_, userID, _, _ := (&oauthE2EEnv{db: env.db, authSvc: env.authSvc}).registerTestUser(t, prefix)
	_, err := env.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())`,
		uuid.New(), env.workspaceID, userID, role)
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, user_id) VALUES ($1, $2, $3, $4)`,
		uuid.New(), env.projectID, env.workspaceID, userID)
	require.NoError(t, err)
	return userID
}

func (env *chatE2EEnv) post(t *testing.T, path string, headers map[string]string, body any) (code int, resp []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(http.MethodPost, env.server.URL+path, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return env.do(t, req)
}

func (env *chatE2EEnv) get(t *testing.T, path string, headers map[string]string) (code int, resp []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.server.URL+path, http.NoBody)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return env.do(t, req)
}

func (env *chatE2EEnv) do(t *testing.T, req *http.Request) (status int, payload []byte) {
	t.Helper()
	res, err := env.client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(res.Body)
	return res.StatusCode, b.Bytes()
}

var chatCtl = map[string]string{mw.ChatControllerTokenHeader: chatTestControllerSecret}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

// chatSession is one chat's identifiers.
type chatSession struct{ thread, session uuid.UUID }

func newChatSession() chatSession { return chatSession{uuid.New(), uuid.New()} }

func (env *chatE2EEnv) issueBody(userID uuid.UUID, cs chatSession, epoch int64) map[string]any {
	return map[string]any{
		"user_id": userID, "agent_id": env.agent.ID, "workspace_id": env.workspaceID, "project_id": env.projectID,
		"chat_thread_id": cs.thread, "chat_session_id": cs.session, "runner_id": "runner-1", "execution_epoch": epoch,
	}
}

// issue mints a token through the real controller route and returns the bearer.
func (env *chatE2EEnv) issue(t *testing.T, userID uuid.UUID, cs chatSession, epoch int64) string {
	t.Helper()
	code, body := env.post(t, "/internal/chat-sessions/tokens", chatCtl, env.issueBody(userID, cs, epoch))
	require.Equal(t, http.StatusCreated, code, string(body))
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Contains(t, out.Token, service.ChatSessionTokenPrefix)
	require.True(t, out.ExpiresAt.After(time.Now()))
	return out.Token
}

func (env *chatE2EEnv) move(t *testing.T, taskID, statusID uuid.UUID, headers map[string]string) int {
	t.Helper()
	code, body := env.post(t, "/api/v1/tasks/"+taskID.String()+"/move", headers, map[string]any{"status_id": statusID})
	t.Logf("move %s -> %d %s", taskID.String()[:8], code, string(body))
	return code
}

func (env *chatE2EEnv) taskStatus(t *testing.T, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, env.db.Get(&id, `SELECT status_id FROM tasks WHERE id = $1`, taskID))
	return id
}

func (env *chatE2EEnv) reset(t *testing.T, taskID, statusID uuid.UUID) {
	t.Helper()
	_, err := env.db.Exec(`UPDATE tasks SET status_id = $2 WHERE id = $1`, taskID, statusID)
	require.NoError(t, err)
}

// AC1: a viewer working through a chat token cannot close a task (403, task
// unchanged); the same call with the agent's own key passes (the red control
// that proves the operation and the agent's right are real); a member through
// a chat token can (the intersection is not simply "deny everything").
func TestChatSessionToken_ViewerCannotCloseTask_AgentKeyCan(t *testing.T) {
	env := newChatE2EEnv(t)
	viewer := env.newUser(t, "chat-viewer", domain.RoleViewer)
	member := env.newUser(t, "chat-member", domain.RoleMember)
	task := env.newTask(t, env.projectID, env.todoID)

	// Red control: the agent key closes the task.
	require.Equal(t, http.StatusOK, env.move(t, task, env.doneID, map[string]string{"X-Agent-Key": env.agentKey}))
	require.Equal(t, env.doneID, env.taskStatus(t, task), "agent key must really have closed the task")
	env.reset(t, task, env.todoID)

	// Same call, chat token of a viewer: 403, and nothing changes.
	viewerTok := env.issue(t, viewer, newChatSession(), 1)
	assert.Equal(t, http.StatusForbidden, env.move(t, task, env.doneID, bearer(viewerTok)))
	assert.Equal(t, env.todoID, env.taskStatus(t, task), "a denied call must not change the task")

	// A member's chat token closes it.
	memberTok := env.issue(t, member, newChatSession(), 1)
	assert.Equal(t, http.StatusOK, env.move(t, task, env.doneID, bearer(memberTok)))
	assert.Equal(t, env.doneID, env.taskStatus(t, task))
}

// AC3: the task history records the agent AND the user it acted for.
func TestChatSessionToken_ActivityCarriesAttribution(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-attrib", domain.RoleMember)
	task := env.newTask(t, env.projectID, env.todoID)
	cs := newChatSession()
	tok := env.issue(t, member, cs, 3)
	require.Equal(t, http.StatusOK, env.move(t, task, env.doneID, bearer(tok)))

	code, body := env.get(t, "/api/v1/tasks/"+task.String()+"/activity", bearer(tok))
	require.Equal(t, http.StatusOK, code, string(body))
	var page struct {
		Items []domain.ActivityLog `json:"items"`
	}
	require.NoError(t, json.Unmarshal(body, &page))

	var moved *domain.ActivityLog
	for i := range page.Items {
		if page.Items[i].Action == "task.moved" {
			moved = &page.Items[i]
		}
	}
	require.NotNil(t, moved, "no task.moved entry in %s", string(body))
	assert.Equal(t, domain.ActorTypeAgent, moved.ActorType)
	assert.Equal(t, env.agent.ID, moved.ActorID)
	require.NotNil(t, moved.ActorName)
	assert.Equal(t, env.agent.Name, *moved.ActorName)
	require.NotNil(t, moved.InitiatedByName, "the user the agent acted for must be named")

	var wantName string
	require.NoError(t, env.db.Get(&wantName, `SELECT display_name FROM users WHERE id = $1`, member))
	assert.Equal(t, wantName, *moved.InitiatedByName)

	var changes map[string]any
	require.NoError(t, json.Unmarshal(moved.Changes, &changes))
	assert.Equal(t, member.String(), changes["initiated_by_user_id"])
	assert.Equal(t, cs.thread.String(), changes["chat_thread_id"])
	assert.Equal(t, cs.session.String(), changes["chat_session_id"])
	assert.NotContains(t, string(moved.Changes), tok, "the bearer must never reach the audit trail")

	// A plain agent-key move carries no chat attribution.
	task2 := env.newTask(t, env.projectID, env.todoID)
	require.Equal(t, http.StatusOK, env.move(t, task2, env.doneID, map[string]string{"X-Agent-Key": env.agentKey}))
	_, body = env.get(t, "/api/v1/tasks/"+task2.String()+"/activity", map[string]string{"X-Agent-Key": env.agentKey})
	assert.NotContains(t, string(body), "initiated_by")
}

// AC2: expiry is a 401 and renewal works; renewal never revives a stopped
// session or a stale epoch.
func TestChatSessionToken_ExpiryRenewalAndStop(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-expiry", domain.RoleMember)
	env.chatSvc.(interface{ SetTTLForTesting(time.Duration) }).SetTTLForTesting(1500 * time.Millisecond)

	cs := newChatSession()
	tok := env.issue(t, member, cs, 1)
	code, _ := env.get(t, "/api/v1/agents/me", bearer(tok))
	require.Equal(t, http.StatusOK, code, "a fresh token must work")

	time.Sleep(1700 * time.Millisecond)
	code, _ = env.get(t, "/api/v1/agents/me", bearer(tok))
	assert.Equal(t, http.StatusUnauthorized, code, "an expired token is a 401")

	// Only the expiry phase needs a short TTL; a loaded CI box must not expire the renewed token mid-test.
	env.chatSvc.(interface{ SetTTLForTesting(time.Duration) }).SetTTLForTesting(time.Minute)

	// Renewal is the runner's call under the controller credential; the expired token is not the basis.
	code, body := env.post(t, "/internal/chat-sessions/"+cs.session.String()+"/renew", chatCtl, map[string]any{"runner_id": "runner-1", "execution_epoch": 1})
	require.Equal(t, http.StatusOK, code, string(body))
	var renewed struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(body, &renewed))
	require.NotEqual(t, tok, renewed.Token)
	code, _ = env.get(t, "/api/v1/agents/me", bearer(renewed.Token))
	assert.Equal(t, http.StatusOK, code, "the renewed token works")

	// Another runner / epoch cannot renew.
	code, _ = env.post(t, "/internal/chat-sessions/"+cs.session.String()+"/renew", chatCtl, map[string]any{"runner_id": "runner-2", "execution_epoch": 1})
	assert.Equal(t, http.StatusConflict, code)
	code, _ = env.post(t, "/internal/chat-sessions/"+cs.session.String()+"/renew", chatCtl, map[string]any{"runner_id": "runner-1", "execution_epoch": 0})
	assert.Equal(t, http.StatusConflict, code)

	// A newer execution issues under a higher epoch; the older epoch is then stale.
	newer := env.issue(t, member, cs, 2)
	code, _ = env.post(t, "/internal/chat-sessions/tokens", chatCtl, env.issueBody(member, cs, 1))
	assert.Equal(t, http.StatusConflict, code, "an older epoch must not be issued again")
	code, _ = env.get(t, "/api/v1/agents/me", bearer(newer))
	assert.Equal(t, http.StatusOK, code)

	// Stop: every token of the session dies at once, and the session cannot be renewed.
	code, _ = env.post(t, "/internal/chat-sessions/"+cs.session.String()+"/revoke", chatCtl, nil)
	require.Equal(t, http.StatusOK, code)
	code, _ = env.get(t, "/api/v1/agents/me", bearer(newer))
	assert.Equal(t, http.StatusUnauthorized, code, "a stopped session's token is a 401")
	code, _ = env.post(t, "/internal/chat-sessions/"+cs.session.String()+"/renew", chatCtl, map[string]any{"runner_id": "runner-1", "execution_epoch": 2})
	assert.Equal(t, http.StatusUnauthorized, code, "a stopped session is not revived by renewal")
	code, _ = env.post(t, "/internal/chat-sessions/tokens", chatCtl, env.issueBody(member, cs, 2))
	assert.Equal(t, http.StatusConflict, code, "re-issue under the epoch that was stopped is refused")
	code, _ = env.post(t, "/internal/chat-sessions/tokens", chatCtl, env.issueBody(member, cs, 3))
	assert.Equal(t, http.StatusConflict, code, "a stopped session is not revived even by a higher epoch")
	_ = env.issue(t, member, newChatSession(), 1) // a restart is a new session id
}

// Contract §3.7 / matrix rows 7-8: revocation takes effect on the very next
// call, long before the token's own expiry — the chosen, pinned code is 401.
func TestChatSessionToken_RevocationBeforeExpiry(t *testing.T) {
	env := newChatE2EEnv(t)

	t.Run("user membership revoked", func(t *testing.T) {
		member := env.newUser(t, "chat-revoked-user", domain.RoleMember)
		tok := env.issue(t, member, newChatSession(), 1)
		code, _ := env.get(t, "/api/v1/agents/me", bearer(tok))
		require.Equal(t, http.StatusOK, code)
		_, err := env.db.Exec(`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, env.projectID, member)
		require.NoError(t, err)
		code, _ = env.get(t, "/api/v1/agents/me", bearer(tok))
		assert.Equal(t, http.StatusUnauthorized, code)
	})

	t.Run("agent connection revoked", func(t *testing.T) {
		member := env.newUser(t, "chat-revoked-agent", domain.RoleMember)
		tok := env.issue(t, member, newChatSession(), 1)
		_, err := env.db.Exec(`DELETE FROM project_members WHERE project_id = $1 AND agent_id = $2`, env.projectID, env.agent.ID)
		require.NoError(t, err)
		code, _ := env.get(t, "/api/v1/agents/me", bearer(tok))
		assert.Equal(t, http.StatusUnauthorized, code)
		env.addAgentToProject(t, env.projectID)
	})

	t.Run("a role downgrade denies writes at once", func(t *testing.T) {
		member := env.newUser(t, "chat-downgrade", domain.RoleMember)
		task := env.newTask(t, env.projectID, env.todoID)
		tok := env.issue(t, member, newChatSession(), 1)
		_, err := env.db.Exec(`UPDATE workspace_members SET role = 'viewer' WHERE workspace_id = $1 AND user_id = $2`, env.workspaceID, member)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, env.move(t, task, env.doneID, bearer(tok)))
		assert.Equal(t, env.todoID, env.taskStatus(t, task))
	})
}

// Contract §2 / matrix row 6: a token for project P cannot touch project Q, and a
// route that names no project is refused rather than waved through.
func TestChatSessionToken_ConfinedToItsProject(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-scope", domain.RoleMember)
	tok := env.issue(t, member, newChatSession(), 1)

	foreign := env.newTask(t, env.otherProj, env.otherTodoID)
	assert.Equal(t, http.StatusForbidden, env.move(t, foreign, env.otherDoneID, bearer(tok)))
	assert.Equal(t, env.otherTodoID, env.taskStatus(t, foreign), "the foreign task must be untouched")

	code, _ := env.get(t, "/api/v1/agents/me/tasks", bearer(tok))
	assert.Equal(t, http.StatusForbidden, code, "a route that names no project is refused to a chat token")

	// Control: the agent key reaches the other project's task (so the 403 above is the token's scope).
	assert.Equal(t, http.StatusOK, env.move(t, foreign, env.otherDoneID, map[string]string{"X-Agent-Key": env.agentKey}))
}

// A query project_id is a filter, not proof of project scope: the workspace-level
// project list stays refused whether it is absent, foreign or equal to the token's
// project, and never leaks project Q to a token bound to P.
func TestChatSessionToken_WorkspaceListRefusedDespiteQueryProject(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-wslist", domain.RoleMember)
	tok := env.issue(t, member, newChatSession(), 1)
	base := "/api/v1/workspaces/" + env.workspaceID.String() + "/projects"

	for name, path := range map[string]string{
		"no-query":       base,
		"foreign-query":  base + "?project_id=" + env.otherProj.String(),
		"matching-query": base + "?project_id=" + env.projectID.String(),
	} {
		code, body := env.get(t, path, bearer(tok))
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.NotContains(t, string(body), env.otherProj.String(), name+": project Q must not be disclosed")
	}

	// Control: the agent key (member of P and Q) gets the list, including Q.
	code, body := env.get(t, base, map[string]string{"X-Agent-Key": env.agentKey})
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), env.otherProj.String())
}

// move-to-project and its batch variant take a second project from the body, which
// the path-based scope check cannot see: both are refused to a chat token (the
// batch route names no project at all, so it fails closed), the task stays in P,
// and the agent key (member of P and Q) is the positive control.
func TestChatSessionToken_MoveToProjectAndBatchRefused(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-mtp", domain.RoleMember)
	tok := env.issue(t, member, newChatSession(), 1)
	task := env.newTask(t, env.projectID, env.todoID)
	projectOf := func() uuid.UUID {
		var id uuid.UUID
		require.NoError(t, env.db.Get(&id, `SELECT project_id FROM tasks WHERE id = $1`, task))
		return id
	}

	single := "/api/v1/tasks/" + task.String() + "/move-to-project"
	batch := "/api/v1/tasks/move-to-project-batch"
	for name, tc := range map[string]struct {
		path string
		body map[string]any
	}{
		"single":                  {single, map[string]any{"project_id": env.otherProj.String()}},
		"batch":                   {batch, map[string]any{"project_id": env.otherProj.String(), "task_ids": []string{task.String()}}},
		"batch-with-query-filter": {batch + "?project_id=" + env.projectID.String(), map[string]any{"project_id": env.otherProj.String(), "task_ids": []string{task.String()}}},
	} {
		code, body := env.post(t, tc.path, bearer(tok), tc.body)
		assert.Equal(t, http.StatusForbidden, code, name+": "+string(body))
		assert.Equal(t, env.projectID, projectOf(), name+": the task must stay in the chat's project")
	}

	// Control: the agent key is not stopped by the chat scope on the same routes.
	code, body := env.post(t, single, map[string]string{"X-Agent-Key": env.agentKey}, map[string]any{"project_id": env.otherProj.String()})
	assert.NotEqual(t, http.StatusForbidden, code, "control (single): "+string(body))
	assert.NotEqual(t, http.StatusUnauthorized, code, "control (single): "+string(body))
	code, body = env.post(t, batch, map[string]string{"X-Agent-Key": env.agentKey}, map[string]any{"project_id": env.projectID.String(), "task_ids": []string{task.String()}})
	assert.NotEqual(t, http.StatusForbidden, code, "control (batch): "+string(body))
	assert.NotEqual(t, http.StatusUnauthorized, code, "control (batch): "+string(body))
}

// The issuing routes accept only the controller credential, fail closed when it
// is unconfigured, and refuse a binding whose user/agent is not entitled.
func TestChatSessionToken_IssueIsControllerOnly(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-issue", domain.RoleMember)
	body := env.issueBody(member, newChatSession(), 1)

	code, _ := env.post(t, "/internal/chat-sessions/tokens", nil, body)
	assert.Equal(t, http.StatusUnauthorized, code, "no credential")
	code, _ = env.post(t, "/internal/chat-sessions/tokens", map[string]string{mw.ChatControllerTokenHeader: "wrong"}, body)
	assert.Equal(t, http.StatusUnauthorized, code, "wrong credential")
	code, _ = env.post(t, "/internal/chat-sessions/tokens", map[string]string{"X-Agent-Key": env.agentKey}, body)
	assert.Equal(t, http.StatusUnauthorized, code, "an agent key is not a controller credential")

	t.Setenv(mw.EnvChatControllerToken, "")
	code, _ = env.post(t, "/internal/chat-sessions/tokens", map[string]string{mw.ChatControllerTokenHeader: ""}, body)
	assert.Equal(t, http.StatusUnauthorized, code, "unconfigured fails closed, even for an empty header")
	t.Setenv(mw.EnvChatControllerToken, chatTestControllerSecret)

	stranger := uuid.New()
	code, _ = env.post(t, "/internal/chat-sessions/tokens", chatCtl, env.issueBody(stranger, newChatSession(), 1))
	assert.Equal(t, http.StatusForbidden, code, "a user outside the workspace gets no token")

	code, _ = env.post(t, "/internal/chat-sessions/tokens", chatCtl, map[string]any{"user_id": member})
	assert.Equal(t, http.StatusBadRequest, code, "an incomplete binding is refused")

	// A garbage / foreign-prefix bearer is a 401, with no fall-through.
	code, _ = env.get(t, "/api/v1/agents/me", bearer(service.ChatSessionTokenPrefix+"deadbeef"))
	assert.Equal(t, http.StatusUnauthorized, code)
}

func (env *chatE2EEnv) deleteProject(t *testing.T, projectID uuid.UUID, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, env.server.URL+"/api/v1/projects/"+projectID.String(), http.NoBody)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	code, body := env.do(t, req)
	t.Logf("delete project %s -> %d %s", projectID.String()[:8], code, string(body))
	return code
}

func (env *chatE2EEnv) projectDeleted(t *testing.T, projectID uuid.UUID) bool {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM projects WHERE id = $1 AND deleted_at IS NOT NULL`, projectID))
	return n > 0
}

// AC5: a right the user holds but the agent lacks is not granted by a chat
// token. The workspace owner may delete the project (positive control, human
// JWT); the agent, by its own key and through the owner's chat token, may not.
// Denied calls leave the project untouched; only the human call deletes it.
func TestChatSessionToken_UserAllowsAgentDenies_DeleteProject(t *testing.T) {
	env := newChatE2EEnv(t)
	tok := env.issue(t, env.ownerID, newChatSession(), 1)

	assert.Equal(t, http.StatusForbidden, env.deleteProject(t, env.projectID, map[string]string{"X-Agent-Key": env.agentKey}))
	assert.False(t, env.projectDeleted(t, env.projectID), "a denied agent-key call must not delete the project")

	assert.Equal(t, http.StatusForbidden, env.deleteProject(t, env.projectID, bearer(tok)))
	assert.False(t, env.projectDeleted(t, env.projectID), "a denied chat-token call must not delete the project")

	// Positive control: the same call by the user themself goes through.
	assert.Equal(t, http.StatusNoContent, env.deleteProject(t, env.projectID, bearer(env.ownerToken)))
	assert.True(t, env.projectDeleted(t, env.projectID), "the human owner's delete must really delete the project")
}

// The far end of a dependency edge arrives in the body: a token for project P must
// not link its task to a task of project Q (found by review on the path-only check).
func TestChatSessionToken_DependencyCannotCrossProjects(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-dep", domain.RoleMember)
	tok := env.issue(t, member, newChatSession(), 1)

	own := env.newTask(t, env.projectID, env.todoID)
	sibling := env.newTask(t, env.projectID, env.todoID)
	foreign := env.newTask(t, env.otherProj, env.otherTodoID)
	path := "/api/v1/tasks/" + own.String() + "/dependencies"
	edge := func(to uuid.UUID) map[string]any {
		return map[string]any{"depends_on_task_id": to.String(), "dependency_type": "blocks"}
	}
	count := func() (n int) {
		require.NoError(t, env.db.Get(&n, `SELECT COUNT(*) FROM task_dependencies WHERE task_id = $1`, own))
		return n
	}

	code, body := env.post(t, path, bearer(tok), edge(foreign))
	assert.Equal(t, http.StatusForbidden, code, string(body))
	assert.Zero(t, count(), "no edge to the foreign task may be written")
	code, _ = env.post(t, path, bearer(tok), map[string]any{"dependency_type": "blocks"})
	assert.Equal(t, http.StatusForbidden, code, "a missing far end is refused, not waved through")

	// Controls: the same edge is accepted for a task of the token's own project, and
	// the agent key (no chat scope) can reach the foreign one, so the 403 is the token's scope.
	code, body = env.post(t, path, bearer(tok), edge(sibling))
	assert.Equal(t, http.StatusCreated, code, string(body))
	code, body = env.post(t, path, map[string]string{"X-Agent-Key": env.agentKey}, edge(foreign))
	assert.Equal(t, http.StatusCreated, code, string(body))
}

// A right the user holds and the connector-only routes check by role alone is still
// not granted to a chat token when the agent lacks it: PATCH /projects/:id and
// POST /projects/:id/knowledge (no PermManageProject / PermWriteMemory for agents).
// The owner's own JWT is the positive control on the same routes.
func TestChatSessionToken_ConnectorRBACRoutesStillNeedAgentPerms(t *testing.T) {
	env := newChatE2EEnv(t)
	tok := env.issue(t, env.ownerID, newChatSession(), 1)
	projPath := "/api/v1/projects/" + env.projectID.String()

	patch := func(headers map[string]string) int {
		req, err := http.NewRequest(http.MethodPatch, env.server.URL+projPath, strings.NewReader(`{"description":"cst probe"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		code, _ := env.do(t, req)
		return code
	}
	knowledge := func(headers map[string]string) int {
		code, _ := env.post(t, projPath+"/knowledge", headers, map[string]any{"key": "cst-probe", "value": "v"})
		return code
	}

	assert.Equal(t, http.StatusForbidden, patch(bearer(tok)), "chat token must not PATCH the project")
	assert.Equal(t, http.StatusForbidden, knowledge(bearer(tok)), "chat token must not write project knowledge")

	assert.Equal(t, http.StatusOK, patch(bearer(env.ownerToken)), "positive control: the owner's JWT may PATCH")
	assert.Less(t, knowledge(bearer(env.ownerToken)), 300, "positive control: the owner's JWT may write knowledge")
}

// The workspace owner may have no workspace_members row (the workspace guard and
// the consent flow admit them anyway); a chat token for them must keep working on
// routes behind rbac(), not 403 at the role clamp.
func TestChatSessionToken_OwnerWithoutMembershipRowIsNotLockedOut(t *testing.T) {
	env := newChatE2EEnv(t)
	_, err := env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, env.workspaceID, env.ownerID)
	require.NoError(t, err)
	tok := env.issue(t, env.ownerID, newChatSession(), 1)
	task := env.newTask(t, env.projectID, env.todoID)

	assert.Equal(t, http.StatusOK, env.move(t, task, env.doneID, bearer(tok)), "the owner's chat token passes the role clamp")
	assert.Equal(t, env.doneID, env.taskStatus(t, task))
	// Control: a user with no row who is not the owner stays out.
	stranger := env.newUser(t, "chat-no-row", domain.RoleMember)
	_, err = env.db.Exec(`DELETE FROM project_members WHERE user_id = $1`, stranger)
	require.NoError(t, err)
	_, err = env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, env.workspaceID, stranger)
	require.NoError(t, err)
	code, _ := env.post(t, "/internal/chat-sessions/tokens", chatCtl, env.issueBody(stranger, newChatSession(), 1))
	assert.Equal(t, http.StatusForbidden, code, "a non-owner without membership gets no token")
}

// chatChildParams are the child-resource path parameters ChatSessionProjectScope resolves.
func chatChildParams() []string {
	return []string{"status_id", "field_id", "view_id", "atr_id", "tmpl_id", "recurring_id", "rule_id",
		"doc_id", "dep_id", "comment_id", "artifact_id", "link_id", "att_id", "dcom_id"}
}

// seedChildResources inserts one resource per child parameter into project and
// returns param -> resource id.
func (env *chatE2EEnv) seedChildResources(t *testing.T, projectID, todoID, doneID uuid.UUID) map[string]uuid.UUID {
	t.Helper()
	ids := map[string]uuid.UUID{}
	for _, n := range chatChildParams() {
		ids[n] = uuid.New()
	}
	taskA, taskB := env.newTask(t, projectID, todoID), env.newTask(t, projectID, todoID)
	docID := uuid.New()
	slug := uuid.New().String()[:8]
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1, $2, 'extra', $3, 'todo', 9)`, []any{ids["status_id"], projectID, "x" + slug}},
		{`INSERT INTO custom_field_definitions (id, project_id, name, slug, field_type) VALUES ($1, $2, 'f', $3, 'text')`, []any{ids["field_id"], projectID, "f" + slug}},
		{`INSERT INTO saved_views (id, project_id, name, created_by) VALUES ($1, $2, 'v', $3)`, []any{ids["view_id"], projectID, env.ownerID}},
		{`INSERT INTO auto_transition_rules (id, project_id, trigger, target_status_id) VALUES ($1, $2, 'all_subtasks_done', $3)`, []any{ids["atr_id"], projectID, doneID}},
		{`INSERT INTO task_templates (id, project_id, name) VALUES ($1, $2, 't')`, []any{ids["tmpl_id"], projectID}},
		{`INSERT INTO recurring_schedules (id, workspace_id, project_id, title_template, cron_expr, created_by) VALUES ($1, $2, $3, 'r', '0 * * * *', $4)`, []any{ids["recurring_id"], env.workspaceID, projectID, env.ownerID}},
		{`INSERT INTO rules (id, workspace_id, project_id, scope, rule_type, name, created_by) VALUES ($1, $2, $3, 'project', 'chat.project_probe', 'r', $4)`, []any{ids["rule_id"], env.workspaceID, projectID, env.ownerID}},
		{`INSERT INTO documents (id, project_id, slug, title, storage_key, created_by, created_by_type) VALUES ($1, $2, $3, 'd', $4, $5, 'user')`, []any{docID, projectID, "d" + slug, "k/" + slug, env.ownerID}},
		{`INSERT INTO task_dependencies (id, task_id, depends_on_task_id) VALUES ($1, $2, $3)`, []any{ids["dep_id"], taskA, taskB}},
		{`INSERT INTO comments (id, task_id, author_id, author_type, body) VALUES ($1, $2, $3, 'user', 'c')`, []any{ids["comment_id"], taskA, env.ownerID}},
		{`INSERT INTO artifacts (id, task_id, name, storage_key, uploaded_by, uploaded_by_type) VALUES ($1, $2, 'a', $3, $4, 'user')`, []any{ids["artifact_id"], taskA, "a/" + slug, env.ownerID}},
		{`INSERT INTO vcs_links (id, task_id, link_type, external_id, url) VALUES ($1, $2, 'pr', $3, 'https://example.test/pr/1')`, []any{ids["link_id"], taskA, slug}},
		{`INSERT INTO document_attachments (id, document_id, name, mime_type, size_bytes, storage_key, uploaded_by, uploaded_by_type) VALUES ($1, $2, 'a', 'text/plain', 1, $3, $4, 'user')`, []any{ids["att_id"], docID, "da/" + slug, env.ownerID}},
		{`INSERT INTO document_comments (id, document_id, author_id, author_type, body) VALUES ($1, $2, $3, 'user', 'c')`, []any{ids["dcom_id"], docID, env.ownerID}},
	}
	ids["doc_id"] = docID
	for _, st := range stmts {
		_, err := env.db.Exec(st.q, st.args...)
		require.NoError(t, err, st.q)
	}
	return ids
}

// A child resource resolves to its project: the token's own resource passes, a
// resource of project Q and an unknown parameter are refused, and the agent key
// reaches Q's resource (so the 403 is the token's scope, not a dead route).
func TestChatSessionToken_ChildResourcesResolveToProject(t *testing.T) {
	env := newChatE2EEnv(t)
	member := env.newUser(t, "chat-child", domain.RoleMember)
	tok := env.issue(t, member, newChatSession(), 1)

	own := env.seedChildResources(t, env.projectID, env.todoID, env.doneID)
	foreign := env.seedChildResources(t, env.otherProj, env.otherTodoID, env.otherDoneID)

	for _, name := range chatChildParams() {
		path := func(id uuid.UUID) string { return "/api/v1/probe/" + name + "/" + id.String() }
		code, body := env.get(t, path(own[name]), bearer(tok))
		assert.Equal(t, http.StatusOK, code, "%s own resource: %s", name, body)
		code, _ = env.get(t, path(foreign[name]), bearer(tok))
		assert.Equal(t, http.StatusForbidden, code, "%s resource of another project", name)
		code, _ = env.get(t, path(uuid.New()), bearer(tok))
		// WorkspaceRLS runs first and answers 404 for ids it cannot place in a workspace;
		// either way a nonexistent resource is refused, never served.
		assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, code, "%s nonexistent resource", name)
		code, body = env.get(t, path(foreign[name]), map[string]string{"X-Agent-Key": env.agentKey})
		assert.Equal(t, http.StatusOK, code, "%s agent-key control: %s", name, body)
	}

	code, _ := env.get(t, "/api/v1/probe/unmapped_id/"+uuid.New().String(), bearer(tok))
	assert.Equal(t, http.StatusForbidden, code, "an unknown path parameter stays refused")

	// A workspace-wide rule (NULL project_id) resolves to no project and is refused.
	wsRule := uuid.New()
	_, err := env.db.Exec(`INSERT INTO rules (id, workspace_id, scope, rule_type, name, created_by) VALUES ($1, $2, 'workspace', 'chat.workspace_probe', 'ws', $3)`,
		wsRule, env.workspaceID, env.ownerID)
	require.NoError(t, err)
	code, _ = env.get(t, "/api/v1/probe/rule_id/"+wsRule.String(), bearer(tok))
	assert.Equal(t, http.StatusForbidden, code, "a workspace-scoped rule is outside the chat's project")
}
