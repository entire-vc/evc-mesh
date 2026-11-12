package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// repoAccessEnv serves the real routes that touch an agent, wired as in
// cmd/api: the one writer (PUT repository-access), the two readers (GET
// /agents/me, GET /agents/:id), the audit read, and every generic write that
// must leave repository_access alone.
type repoAccessEnv struct {
	db      *sqlx.DB
	server  *httptest.Server
	authSvc *auth.Service
}

func newRepoAccessEnv(t *testing.T) *repoAccessEnv {
	t.Helper()
	db := oauthE2ETestDB(t)
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	memberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	projectRepo := postgres.NewProjectRepo(db)

	agentSvc := service.NewAgentService(agentRepo, postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	if configurable, ok := agentSvc.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
		configurable.SetAgentActivityLogRepo(postgres.NewAgentActivityLogRepo(db))
	}
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, memberRepo, "repo-access-e2e-test-jwt-secret")
	rulesSvc := service.NewRulesService(nil, nil, nil, agentRepo, memberRepo, workspaceRepo, projectRepo)

	accessRepo := postgres.NewAgentRepositoryAccessRepo(db)
	agentHandler := NewAgentHandler(agentSvc)
	agentHandler.SetRepositoryAccessReader(accessRepo)
	accessHandler := NewAgentRepositoryAccessHandler(accessRepo)
	rulesHandler := NewRulesHandler(rulesSvc)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	rbac := func(p mw.Permission) echo.MiddlewareFunc { return mw.RequirePermission(p, memberRepo) }

	api.GET("/agents/me", agentHandler.Me)
	api.PATCH("/agents/me", agentHandler.UpdateMe)
	api.POST("/agents/heartbeat", agentHandler.Heartbeat)
	api.GET("/agents/:agent_id", agentHandler.GetByID)
	api.PATCH("/agents/:agent_id", agentHandler.Update, rbac(mw.PermDeleteAgent))
	api.PUT("/agents/:agent_id/repository-access", accessHandler.Put)
	api.GET("/agents/:agent_id/repository-access/history", accessHandler.History)
	api.GET("/agents/:agent_id/activity", agentHandler.ListAgentActivity)
	api.POST("/agents/:agent_id/activity", agentHandler.CreateAgentActivity, rbac(mw.PermDeleteAgent))
	api.PUT("/agents/:agent_id/profile", rulesHandler.UpdateAgentProfile, mw.RequireSelfOrPermission("agent_id", mw.PermDeleteAgent, memberRepo))
	api.POST("/workspaces/:ws_id/config/import", rulesHandler.ImportConfig, rbac(mw.PermManageMembers))

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return &repoAccessEnv{db: db, server: server, authSvc: authSvc}
}

type repoAccessCaller struct{ header, value string }

func raBearer(token string) repoAccessCaller {
	return repoAccessCaller{"Authorization", "Bearer " + token}
}
func raAgentKey(key string) repoAccessCaller { return repoAccessCaller{"X-Agent-Key", key} }

func (env *repoAccessEnv) call(t *testing.T, who repoAccessCaller, method, path string, body any) (status int, out map[string]any) {
	t.Helper()
	var reader io.Reader = http.NoBody
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case string:
		reader, contentType = bytes.NewReader([]byte(b)), "application/x-yaml"
	case json.RawMessage:
		reader = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, env.server.URL+"/api/v1"+path, reader)
	require.NoError(t, err)
	req.Header.Set(who.header, who.value)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out = map[string]any{}
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

func (env *repoAccessEnv) registerOwner(t *testing.T) (token string, userID, wsID uuid.UUID) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	user, tokens, err := env.authSvc.Register(context.Background(), fmt.Sprintf("ra-%s@repo-access-e2e.example.com", suffix), "Correct-Horse-Battery-Staple-1", "ra-"+suffix)
	require.NoError(t, err)
	ws, err := postgres.NewWorkspaceRepo(env.db).ListForUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, ws)
	return tokens.AccessToken, user.ID, ws[0].ID
}

func (env *repoAccessEnv) registerAgent(t *testing.T, wsID uuid.UUID, name string) (agentID uuid.UUID, apiKey string) {
	t.Helper()
	reg, err := service.NewAgentService(postgres.NewAgentRepo(env.db), postgres.NewActivityLogRepo(env.db),
		postgres.NewWorkspaceRepo(env.db), postgres.NewUserRepo(env.db)).
		Register(context.Background(), service.RegisterAgentInput{WorkspaceID: wsID, Name: name, AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	return reg.Agent.ID, reg.APIKey
}

// TestAgentRepositoryAccess_HTTPContract walks the contract end to end over the
// real routes: absent = legacy on both readers, an allowed write and re-read,
// 409 on a stale revision, strict bodies, the agent's own key refused, every
// generic write path ignoring the field, [] distinct from absent, and the
// audit readable through GET /agents/:id/repository-access/history (the UI
// contract: newest first, actor with name, old null for the first write,
// next_cursor paging, tenant-scoped like GET /agents/:id) and as raw events via
// GET /agents/:id/activity.
func TestAgentRepositoryAccess_HTTPContract(t *testing.T) {
	env := newRepoAccessEnv(t)
	owner, ownerID, wsID := env.registerOwner(t)
	suffix := uuid.New().String()[:8]
	agentName := "Repo Access " + suffix
	agentID, key := env.registerAgent(t, wsID, agentName)
	path := "/agents/" + agentID.String()
	ownerCall, agentCall := raBearer(owner), raAgentKey(key)

	read := func() (byOwner, byAgent map[string]any) {
		t.Helper()
		status, a := env.call(t, ownerCall, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, status, a)
		status, b := env.call(t, agentCall, http.MethodGet, "/agents/me", nil)
		require.Equal(t, http.StatusOK, status, b)
		return a, b
	}
	a, b := read()
	require.NotContains(t, a, "repository_access", "never configured: absent on GET /agents/:id")
	require.NotContains(t, b, "repository_access", "never configured: absent on GET /agents/me")

	status, out := env.call(t, ownerCall, http.MethodPut, path+"/repository-access", map[string]any{
		"expected_revision": 0,
		"repositories":      []map[string]string{{"provider": "gitlab", "host": "https://Git.Example.org/", "project_path": "entire-vc/evc-mesh.git"}},
	})
	require.Equal(t, http.StatusOK, status, out)
	want := map[string]any{"revision": float64(1), "repositories": []any{
		map[string]any{"provider": "gitlab", "host": "git.example.org", "project_path": "entire-vc/evc-mesh"}}}
	require.Equal(t, want, out)
	a, b = read()
	require.Equal(t, want, a["repository_access"], "editor read shows the saved state")
	require.Equal(t, want, b["repository_access"], "runner read shows the same state")

	status, out = env.call(t, ownerCall, http.MethodPut, path+"/repository-access", map[string]any{"expected_revision": 0, "repositories": []any{}})
	require.Equal(t, http.StatusConflict, status, out)

	for name, body := range map[string]any{
		"missing expected_revision": map[string]any{"repositories": []any{}},
		"null repositories":         map[string]any{"expected_revision": 1, "repositories": nil},
		"unknown field":             map[string]any{"expected_revision": 1, "repositories": []any{}, "revision": 5},
		"unknown item field":        map[string]any{"expected_revision": 1, "repositories": []map[string]string{{"provider": "gitlab", "host": "h.io", "project_path": "a/b", "ref": "main"}}},
		"duplicate after normalize": map[string]any{"expected_revision": 1, "repositories": []map[string]string{{"provider": "gitlab", "host": "h.io", "project_path": "a/b"}, {"provider": "gitlab", "host": "H.IO", "project_path": "a/b.git"}}},
		"trailing data":             json.RawMessage(`{"expected_revision":1,"repositories":[]}{}`),
	} {
		status, out = env.call(t, ownerCall, http.MethodPut, path+"/repository-access", body)
		require.Equal(t, http.StatusBadRequest, status, "%s: %v", name, out)
	}

	status, out = env.call(t, agentCall, http.MethodPut, path+"/repository-access", map[string]any{"expected_revision": 1, "repositories": []any{}})
	require.Equal(t, http.StatusForbidden, status, "an agent key never changes its own access: %v", out)

	// Every generic write path carries a repository_access attempt and must not change it.
	smuggled := map[string]any{"revision": 7, "repositories": []any{}}
	status, out = env.call(t, ownerCall, http.MethodPatch, path, map[string]any{"role": "developer", "repository_access": smuggled})
	require.Equal(t, http.StatusOK, status, out)
	status, out = env.call(t, agentCall, http.MethodPatch, "/agents/me", map[string]any{"profile_description": "x", "repository_access": smuggled})
	require.Equal(t, http.StatusOK, status, out)
	status, out = env.call(t, ownerCall, http.MethodPut, path+"/profile", map[string]any{"responsibility_zone": "mesh", "repository_access": smuggled})
	require.Equal(t, http.StatusNoContent, status, out)
	status, out = env.call(t, agentCall, http.MethodPut, path+"/profile", map[string]any{"responsibility_zone": "self", "repository_access": smuggled})
	require.Equal(t, http.StatusNoContent, status, "profile self-write (the MCP update_agent_profile path): %v", out)
	status, out = env.call(t, agentCall, http.MethodPost, "/agents/heartbeat", map[string]any{"status": "online", "repository_access": smuggled, "metadata": map[string]any{"repository_access": smuggled}})
	require.Equal(t, http.StatusOK, status, out)
	yaml := fmt.Sprintf("team:\n  agents:\n    - name: %q\n      role: reviewer\n      repository_access:\n        revision: 7\n        repositories: []\n", agentName)
	status, out = env.call(t, ownerCall, http.MethodPost, "/workspaces/"+wsID.String()+"/config/import", yaml)
	require.Equal(t, http.StatusOK, status, out)
	require.EqualValues(t, 1, out["team"].(map[string]any)["agents_updated"], "positive control: the import did reach the agent: %v", out)

	a, b = read()
	require.Equal(t, want, a["repository_access"], "no generic write changed it")
	require.Equal(t, want, b["repository_access"])
	require.Equal(t, "reviewer", a["role"], "positive control: the generic writes themselves took effect")

	// The audit event type cannot be forged through the generic activity writer.
	status, out = env.call(t, ownerCall, http.MethodPost, path+"/activity", map[string]any{"event_type": domain.AgentRepositoryAccessChangedEvent, "message": "forged"})
	require.Equal(t, http.StatusBadRequest, status, out)

	// [] is a configured deny-all: present, empty, distinct from absent.
	status, out = env.call(t, ownerCall, http.MethodPut, path+"/repository-access", map[string]any{"expected_revision": 1, "repositories": []any{}})
	require.Equal(t, http.StatusOK, status, out)
	a, b = read()
	denyAll := map[string]any{"revision": float64(2), "repositories": []any{}}
	require.Equal(t, denyAll, a["repository_access"])
	require.Equal(t, denyAll, b["repository_access"])

	status, out = env.call(t, ownerCall, http.MethodGet, path+"/activity?event_type="+domain.AgentRepositoryAccessChangedEvent, nil)
	require.Equal(t, http.StatusOK, status, out)
	items, _ := out["items"].([]any)
	require.Len(t, items, 2, "one audit event per change, none forged: %v", out)
	var metas []map[string]any
	for _, it := range items {
		metas = append(metas, it.(map[string]any)["metadata"].(map[string]any))
	}
	var first, second map[string]any
	for _, m := range metas {
		if m["new_revision"] == float64(1) {
			first = m
		} else {
			second = m
		}
	}
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.Equal(t, "user", first["actor_type"])
	require.Equal(t, float64(0), first["old_revision"])
	require.Nil(t, first["old_repositories"], "legacy old state")
	require.Equal(t, want["repositories"], first["new_repositories"])
	require.Equal(t, float64(1), second["old_revision"])
	require.Equal(t, float64(2), second["new_revision"])
	require.Equal(t, want["repositories"], second["old_repositories"])
	require.Equal(t, []any{}, second["new_repositories"])

	// The history read in the shape the editor uses.
	var ownerName string
	require.NoError(t, env.db.Get(&ownerName, `SELECT display_name FROM users WHERE id=$1`, ownerID))
	require.NotEmpty(t, ownerName)
	status, out = env.call(t, ownerCall, http.MethodGet, path+"/repository-access/history?limit=1", nil)
	require.Equal(t, http.StatusOK, status, out)
	page := out["items"].([]any)
	require.Len(t, page, 1)
	top := page[0].(map[string]any)
	require.NotEmpty(t, top["id"])
	require.NotEmpty(t, top["created_at"])
	require.Equal(t, map[string]any{"type": "user", "id": ownerID.String(), "name": ownerName}, top["actor"])
	require.Equal(t, map[string]any{"revision": float64(1), "repositories": want["repositories"]}, top["old"])
	require.Equal(t, denyAll, top["new"], "newest first")
	cursor, ok := out["next_cursor"].(string)
	require.True(t, ok, "a second entry exists: %v", out)

	status, out = env.call(t, ownerCall, http.MethodGet, path+"/repository-access/history?limit=1&cursor="+cursor, nil)
	require.Equal(t, http.StatusOK, status, out)
	page = out["items"].([]any)
	require.Len(t, page, 1)
	firstWrite := page[0].(map[string]any)
	require.Contains(t, firstWrite, "old")
	require.Nil(t, firstWrite["old"], "the first write had no configured state before it")
	require.Equal(t, map[string]any{"revision": float64(1), "repositories": want["repositories"]}, firstWrite["new"])
	require.Nil(t, out["next_cursor"])

	status, out = env.call(t, ownerCall, http.MethodGet, path+"/repository-access/history?limit=abc", nil)
	require.Equal(t, http.StatusBadRequest, status, out)
	status, out = env.call(t, ownerCall, http.MethodGet, path+"/repository-access/history?cursor=zz", nil)
	require.Equal(t, http.StatusBadRequest, status, out)

	// A signed-in person of another workspace reads nothing of it.
	stranger, _, _ := env.registerOwner(t)
	status, out = env.call(t, raBearer(stranger), http.MethodGet, path+"/repository-access/history", nil)
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, status, out)
	status, out = env.call(t, raBearer(stranger), http.MethodGet, path, nil)
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, status, "same boundary as GET /agents/:id: %v", out)
}
