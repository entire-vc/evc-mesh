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

// End-to-end coverage for the agent short_tag (Mesh card aa1b4845): an
// optional human-authored role label, ≤24 chars, set/cleared through
// PATCH /agents/:agent_id and read back from every list the UI feeds from.
//
// Every assert here is deliberately RAW-JSON (`out["short_tag"]`), never
// `domain.Agent.ShortTag`: the card's acceptance criterion is a red run on
// the pre-feature code, and a test that references the new field cannot even
// compile there. On the old code this suite fails exactly at the contract:
// PATCH accepts but silently drops the field, GET omits it, and a 25-char
// value is not rejected.
//
// The router below is the production slice for this feature — same handlers,
// same middleware chain and order as cmd/api/main.go (DualAuth → WorkspaceRLS
// → RequireWorkspaceMemberScoped → per-route rbac) — so the viewer/foreign
// 403s prove the rights model around the new field, not a test-only stand-in.

type shortTagEnv struct {
	db      *sqlx.DB
	server  *httptest.Server
	client  *http.Client
	authSvc *auth.Service
}

func newShortTagEnv(t *testing.T) *shortTagEnv {
	t.Helper()
	db := oauthE2ETestDB(t)

	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	activityLogRepo := postgres.NewActivityLogRepo(db)
	agentWorkspaceGrantRepo := postgres.NewAgentWorkspaceGrantRepo(db)
	refreshTokenRepo := postgres.NewRefreshTokenRepo(db)
	projectRepo := postgres.NewProjectRepo(db)

	agentSvc := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo, userRepo)
	if configurable, ok := agentSvc.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(agentWorkspaceGrantRepo)
	}
	authSvc := auth.NewService(userRepo, refreshTokenRepo, workspaceRepo, workspaceMemberRepo, "short-tag-e2e-test-jwt-secret")

	agentHandler := NewAgentHandler(agentSvc)
	mentionablesHandler := NewMentionablesHandler(service.NewMentionablesService(agentRepo, userRepo))

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)

	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	api.Use(mw.RequireWorkspaceMemberScoped(db))

	api.GET("/agents/:agent_id", agentHandler.GetByID)
	api.PATCH("/agents/:agent_id", agentHandler.Update, mw.RequirePermission(mw.PermDeleteAgent, workspaceMemberRepo))
	api.GET("/workspaces/:ws_id/agents", agentHandler.List)
	api.GET("/workspaces/:ws_id/mentionables", mentionablesHandler.Search)

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	return &shortTagEnv{
		db:      db,
		server:  server,
		client:  &http.Client{Timeout: 30 * time.Second},
		authSvc: authSvc,
	}
}

// registerUser creates a real user + default workspace through the real
// registration path; returns a valid JWT and the ids the tests need.
func (env *shortTagEnv) registerUser(t *testing.T, prefix string) (accessToken string, userID, workspaceID uuid.UUID) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	email := fmt.Sprintf("%s-%s@shorttag-e2e.example.com", prefix, suffix)
	user, tokens, err := env.authSvc.Register(context.Background(), email, "Correct-Horse-Battery-Staple-1", prefix+"-"+suffix)
	require.NoError(t, err)

	ws, err := postgres.NewWorkspaceRepo(env.db).ListForUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, ws, "Register must create a default workspace")
	return tokens.AccessToken, user.ID, ws[0].ID
}

// seedAgent registers an agent the way the production route does, returning
// its id.
func (env *shortTagEnv) seedAgent(t *testing.T, workspaceID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	reg, err := service.NewAgentService(
		postgres.NewAgentRepo(env.db),
		postgres.NewActivityLogRepo(env.db),
		postgres.NewWorkspaceRepo(env.db),
		postgres.NewUserRepo(env.db),
	).Register(context.Background(), service.RegisterAgentInput{
		WorkspaceID: workspaceID,
		Name:        name,
		AgentType:   domain.AgentTypeCustom,
	})
	require.NoError(t, err)
	require.NotNil(t, reg.Agent)
	return reg.Agent.ID
}

func (env *shortTagEnv) do(t *testing.T, method, path, token string, body map[string]interface{}) (status int, out map[string]interface{}) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, env.server.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out = map[string]interface{}{}
	if resp.ContentLength != 0 {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp.StatusCode, out
}

func (env *shortTagEnv) getAgent(t *testing.T, token string, agentID uuid.UUID) map[string]interface{} {
	t.Helper()
	status, out := env.do(t, http.MethodGet, "/api/v1/agents/"+agentID.String(), token, nil)
	require.Equal(t, http.StatusOK, status, "owner GET of own workspace agent: %v", out)
	return out
}

func TestAgentShortTagContract(t *testing.T) {
	env := newShortTagEnv(t)
	ownerToken, _, wsID := env.registerUser(t, "st-owner")
	agentID := env.seedAgent(t, wsID, "Keep Dev "+uuid.New().String()[:8])

	// Fresh agent: field is present and null, not absent (contract: string|null).
	got := env.getAgent(t, ownerToken, agentID)
	require.Contains(t, got, "short_tag", "GET /agents/:id must carry an explicit short_tag key once the feature exists")
	assert.Nil(t, got["short_tag"], "an agent that never had a short_tag reads as null")

	// Set: PATCH stores the exact value.
	status, out := env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": "keep-dev"})
	require.Equal(t, http.StatusOK, status, "PATCH setting a valid short_tag: %v", out)
	got = env.getAgent(t, ownerToken, agentID)
	assert.Equal(t, "keep-dev", got["short_tag"], "GET after set must return the exact stored value")

	// Omit: a PATCH that does not mention short_tag preserves it.
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"role": "developer"})
	require.Equal(t, http.StatusOK, status, "PATCH touching an unrelated field: %v", out)
	got = env.getAgent(t, ownerToken, agentID)
	assert.Equal(t, "keep-dev", got["short_tag"], "omitting short_tag from PATCH must preserve the stored value")

	// JSON null clears.
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": nil})
	require.Equal(t, http.StatusOK, status, "PATCH clearing via null: %v", out)
	assert.Nil(t, env.getAgent(t, ownerToken, agentID)["short_tag"], "null must clear back to unset")

	// Whitespace-only is also "empty" and clears.
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": "  "})
	require.Equal(t, http.StatusOK, status, "PATCH whitespace-only short_tag: %v", out)
	assert.Nil(t, env.getAgent(t, ownerToken, agentID)["short_tag"], "a whitespace-only value clears rather than storing padding")

	// >24 chars is refused.
	tooLong := strings.Repeat("x", 25)
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": tooLong})
	assert.Equal(t, http.StatusBadRequest, status, "a 25-char short_tag must be rejected, got 200 with body %v", out)
	assert.Nil(t, env.getAgent(t, ownerToken, agentID)["short_tag"], "a rejected value must not be stored")

	// Exactly 24 is the boundary that works.
	exact24 := strings.Repeat("y", 24)
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": exact24})
	require.Equal(t, http.StatusOK, status, "a 24-char short_tag is the legal maximum: %v", out)
	assert.Equal(t, exact24, env.getAgent(t, ownerToken, agentID)["short_tag"])

	// The limit counts CHARACTERS, not UTF-8 bytes: 13 Cyrillic letters are
	// 26 bytes, and a byte budget would reject a legal human-authored label.
	cyrillic13 := strings.Repeat("д", 13)
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": cyrillic13})
	require.Equal(t, http.StatusOK, status, "13 non-ASCII chars (26 bytes) fit the 24-CHAR budget: %v", out)
	assert.Equal(t, cyrillic13, env.getAgent(t, ownerToken, agentID)["short_tag"])
	cyrillic25 := strings.Repeat("д", 25)
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": cyrillic25})
	assert.Equal(t, http.StatusBadRequest, status, "25 chars are over the budget in any alphabet, got %d: %v", status, out)
	assert.Equal(t, cyrillic13, env.getAgent(t, ownerToken, agentID)["short_tag"], "an over-limit value must not be stored")

	// Non-string JSON values are a type error, not a coerced value.
	for _, bad := range []interface{}{42, true, []interface{}{"dev"}, map[string]interface{}{"v": "dev"}} {
		status, _ = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": bad})
		assert.Equal(t, http.StatusBadRequest, status, "short_tag=%v must be rejected as a type error, got %d", bad, status)
	}
	assert.Equal(t, cyrillic13, env.getAgent(t, ownerToken, agentID)["short_tag"], "rejected type payloads must not change the stored value")

	// Surrounding spaces are trimmed before the length check.
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": "  dev  "})
	require.Equal(t, http.StatusOK, status, "PATCH with padding: %v", out)
	assert.Equal(t, "dev", env.getAgent(t, ownerToken, agentID)["short_tag"], "stored value is trimmed")

	// The workspace agents list the UI reads carries the field too.
	status, out = env.do(t, http.MethodGet, "/api/v1/workspaces/"+wsID.String()+"/agents?page=1&page_size=50", ownerToken, nil)
	require.Equal(t, http.StatusOK, status, "workspace agents list: %v", out)
	items, _ := out["items"].([]interface{})
	var listEntry map[string]interface{}
	for _, it := range items {
		if m, ok := it.(map[string]interface{}); ok && m["id"] == agentID.String() {
			listEntry = m
		}
	}
	require.NotNil(t, listEntry, "seeded agent must appear in the workspace agents list")
	assert.Equal(t, "dev", listEntry["short_tag"], "GET /workspaces/:ws/agents must carry short_tag")

	// Mentionables: agent entries carry the label, user entries carry null.
	mentionables := env.getMentionables(t, ownerToken, wsID)
	var agentEntry, userEntry map[string]interface{}
	for _, m := range mentionables {
		if m["id"] == agentID.String() {
			agentEntry = m
		}
		if m["kind"] == "user" {
			userEntry = m
		}
	}
	require.NotNil(t, agentEntry, "seeded agent must appear in mentionables")
	assert.Equal(t, "dev", agentEntry["short_tag"], "mentionables agent entries must carry short_tag")
	// The user entry is REQUIRED, not conditional: an `if userEntry != nil`
	// guard lets the whole explicit-null assertion silently skip the day the
	// fixture stops returning users, and the contract goes untested.
	require.NotNil(t, userEntry, "the registering owner must appear in mentionables as a user entry")
	// An omitted key and an explicit null both read as nil from the map —
	// pin the key's PRESENCE so omitempty can't quietly eat the field.
	require.Contains(t, userEntry, "short_tag", "mentionables user entries must carry an explicit short_tag:null, not an omitted key")
	assert.Nil(t, userEntry["short_tag"], "mentionables user entries have no short_tag (null)")

	// An agent with no label reads as an explicit null too — same omitempty guard.
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": nil})
	require.Equal(t, http.StatusOK, status, "clearing for mentionables re-check: %v", out)
	mentionables = env.getMentionables(t, ownerToken, wsID)
	for _, m := range mentionables {
		if m["id"] == agentID.String() {
			require.Contains(t, m, "short_tag", "a label-less agent must still carry an explicit short_tag:null")
			assert.Nil(t, m["short_tag"])
		}
	}
}

// TestAgentBriefShortTagAlwaysPresent pins the serialization guard for the
// agent-grants read surface: the e2e env above does not wire the grants
// handler, so the omitempty-vs-null contract is asserted at the JSON layer —
// a nil ShortTag must serialize as an explicit null, never an omitted key.
func TestAgentBriefShortTagAlwaysPresent(t *testing.T) {
	raw, err := json.Marshal(&domain.AgentBrief{ID: uuid.New(), Name: "Probe"})
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Contains(t, m, "short_tag", "AgentBrief must serialize short_tag as explicit null, not omit it")
	assert.Nil(t, m["short_tag"])

	withTag := "dev"
	raw, err = json.Marshal(&domain.AgentBrief{ID: uuid.New(), Name: "Probe", ShortTag: &withTag})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "dev", m["short_tag"])
}

// getMentionables reads GET /workspaces/:ws_id/mentionables, whose body is a
// top-level JSON array rather than an object.
func (env *shortTagEnv) getMentionables(t *testing.T, token string, wsID uuid.UUID) []map[string]interface{} {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/workspaces/"+wsID.String()+"/mentionables?limit=50", http.NoBody)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var results []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&results))
	return results
}

// TestAgentShortTagPermissions pins the write path to the existing rights
// model: a viewer of the same workspace and a member of a DIFFERENT workspace
// both get 403 and neither can move the stored value.
func TestAgentShortTagPermissions(t *testing.T) {
	env := newShortTagEnv(t)
	ownerToken, _, wsID := env.registerUser(t, "perm-owner")
	agentID := env.seedAgent(t, wsID, "Guarded "+uuid.New().String()[:8])

	status, out := env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), ownerToken, map[string]interface{}{"short_tag": "keep-dev"})
	require.Equal(t, http.StatusOK, status, "owner sets the baseline value: %v", out)

	// Viewer of the same workspace: read is fine, write is 403.
	viewerToken, viewerID, _ := env.registerUser(t, "perm-viewer")
	_, err := env.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())`,
		uuid.New(), wsID, viewerID, domain.RoleViewer,
	)
	require.NoError(t, err)
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), viewerToken, map[string]interface{}{"short_tag": "pwned-by-viewer"})
	assert.Equal(t, http.StatusForbidden, status, "a viewer must not be able to write short_tag, got %d: %v", status, out)
	assert.Equal(t, "keep-dev", env.getAgent(t, ownerToken, agentID)["short_tag"], "the refused viewer PATCH must not change the stored value")

	// A user from a different workspace: no read, no write.
	foreignToken, _, _ := env.registerUser(t, "perm-foreign")
	status, out = env.do(t, http.MethodPatch, "/api/v1/agents/"+agentID.String(), foreignToken, map[string]interface{}{"short_tag": "pwned-by-foreign"})
	assert.Equal(t, http.StatusForbidden, status, "a non-member of the workspace must not be able to write short_tag, got %d: %v", status, out)
	status, out = env.do(t, http.MethodGet, "/api/v1/agents/"+agentID.String(), foreignToken, nil)
	assert.Equal(t, http.StatusForbidden, status, "a non-member must not even read the agent, got %d: %v", status, out)
	assert.Equal(t, "keep-dev", env.getAgent(t, ownerToken, agentID)["short_tag"], "the refused foreign PATCH must not change the stored value")
}
