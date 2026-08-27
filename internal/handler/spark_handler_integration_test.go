package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// fakeSparkIntegrationRepo is a minimal repository.IntegrationRepository
// stub, keyed by (workspace, provider) — enough for SparkIntegrationResolver,
// which only ever calls GetByProvider.
type fakeSparkIntegrationRepo struct {
	byWorkspaceProvider map[string]*domain.IntegrationConfig
}

func newFakeSparkIntegrationRepo() *fakeSparkIntegrationRepo {
	return &fakeSparkIntegrationRepo{byWorkspaceProvider: map[string]*domain.IntegrationConfig{}}
}

func (f *fakeSparkIntegrationRepo) put(workspaceID uuid.UUID, cfg domain.IntegrationConfig) {
	cfg.WorkspaceID = workspaceID
	f.byWorkspaceProvider[workspaceID.String()+"|"+string(cfg.Provider)] = &cfg
}

func (f *fakeSparkIntegrationRepo) Upsert(context.Context, *domain.IntegrationConfig) error {
	return nil
}
func (f *fakeSparkIntegrationRepo) GetByID(context.Context, uuid.UUID) (*domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeSparkIntegrationRepo) GetByProvider(_ context.Context, workspaceID uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	return f.byWorkspaceProvider[workspaceID.String()+"|"+string(provider)], nil
}
func (f *fakeSparkIntegrationRepo) Update(context.Context, uuid.UUID, domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeSparkIntegrationRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (f *fakeSparkIntegrationRepo) ListByWorkspace(context.Context, uuid.UUID) ([]domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeSparkIntegrationRepo) ListActiveByProvider(context.Context, domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	return nil, nil
}
func (f *fakeSparkIntegrationRepo) ListByProvider(context.Context, domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	return nil, nil
}

func sparkHandlerCfg(baseURL string) json.RawMessage {
	raw, _ := json.Marshal(service.SparkIntegrationConfig{BaseURL: baseURL})
	return raw
}

// fakeSparkCatalogServer answers /api/v1/assets with one item — enough to
// prove a request actually reached IT specifically (distinguishing "the
// handler called through to a resolved URL" from "the handler returned
// something without calling anyone").
func fakeSparkCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"a1","type":"agent","title":"Test Agent","slug":"test-agent"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// doSparkGET builds and runs a GET request. auth, when non-nil, authenticates
// the request context before the handler method runs — required whenever the
// path carries a workspace_id, since requireWorkspaceAccess now checks it
// (#4a3195a5's own query-tenant guard: a workspace's spark row can point at a
// different catalog host, so an unauthorized caller must be refused before
// clientFor ever resolves a foreign workspace_id, not just get an empty
// answer).
func doSparkGET(t *testing.T, h *SparkHandler, method func(echo.Context) error, path string, auth func(echo.Context)) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if auth != nil {
		auth(c)
	}
	require.NoError(t, method(c))
	return rec
}

// memberOf returns a repository.WorkspaceMemberRepository that reports
// userID as a member of ws (role "member" — Search/Popular/GetByID are reads,
// not the register-agent permission Install's requireRegisterAgent needs).
func memberOf(ws, userID uuid.UUID) *mockWorkspaceMemberRepo {
	return &mockWorkspaceMemberRepo{members: map[string]string{
		fmt.Sprintf("%s/%s", ws, userID): "member",
	}}
}

// ---------------------------------------------------------------------------
// #4a3195a5 acceptance §5: positive control BEFORE negative, then re-enable.
// ---------------------------------------------------------------------------

func TestSparkHandler_Search_WorkspaceRowEnabled_ReachesResolvedCatalog(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	ws := uuid.New()
	user := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{})
	h := NewSparkHandler(resolver, nil, memberOf(ws, user))

	rec := doSparkGET(t, h, h.Search, "/?workspace_id="+ws.String(), func(c echo.Context) { asUser(c, user) })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Count, "must reach the catalog the workspace row points at")
}

func TestSparkHandler_Search_IsActiveFalse_RefusesWithNamedReason_DoesNotCallCatalog(t *testing.T) {
	// Wrap to detect any call — is_active=false must refuse before ever
	// dialing out, not just return an empty/degraded result that LOOKS the
	// same as "disabled" (§0x: a check that can't go red proves nothing).
	// This matters concretely here: Client.Search degrades an unreachable
	// catalog to an empty 200, which is indistinguishable from "disabled" at
	// the response-shape level alone — only "was the catalog dialed at all"
	// tells the two apart.
	catalogHit := false
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"a1","type":"agent","title":"Test Agent","slug":"test-agent"}]}`))
	}))
	t.Cleanup(catalog.Close)

	ws := uuid.New()
	user := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: false, Config: sparkHandlerCfg(catalog.URL)})
	// Env IS configured — the point of this test is that a disabled workspace
	// row must not fall through to it.
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{URL: catalog.URL, Enabled: true})
	h := NewSparkHandler(resolver, nil, memberOf(ws, user))

	rec := doSparkGET(t, h, h.Search, "/?workspace_id="+ws.String(), func(c echo.Context) { asUser(c, user) })
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "not configured", "refusal must name the reason, not just fail silently")
	assert.False(t, catalogHit, "a disabled workspace must never reach the catalog — refusal must happen before dialing out")
}

func TestSparkHandler_Search_ReenabledAfterDisable_WorksAgain(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	ws := uuid.New()
	user := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: false, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{})
	h := NewSparkHandler(resolver, nil, memberOf(ws, user))
	auth := func(c echo.Context) { asUser(c, user) }

	disabledRec := doSparkGET(t, h, h.Search, "/?workspace_id="+ws.String(), auth)
	require.Equal(t, http.StatusServiceUnavailable, disabledRec.Code)

	// Flip it back on — same simulated PATCH .../integrations/:id is_active=true.
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkHandlerCfg(catalog.URL)})

	reenabledRec := doSparkGET(t, h, h.Search, "/?workspace_id="+ws.String(), auth)
	require.Equal(t, http.StatusOK, reenabledRec.Code, reenabledRec.Body.String())
}

func TestSparkHandler_Search_NoWorkspaceID_FallsToEnv(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	resolver := service.NewSparkIntegrationResolver(nil, service.SparkEnvFallback{URL: catalog.URL, Enabled: true})
	h := NewSparkHandler(resolver, nil, nil)

	// No workspace_id at all → requireWorkspaceAccess is never consulted
	// (clientFor only checks it for a non-nil id) → no auth needed either.
	rec := doSparkGET(t, h, h.Search, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestSparkHandler_Search_MalformedWorkspaceID_Rejected(t *testing.T) {
	resolver := service.NewSparkIntegrationResolver(nil, service.SparkEnvFallback{})
	h := NewSparkHandler(resolver, nil, nil)

	rec := doSparkGET(t, h, h.Search, "/?workspace_id=not-a-uuid", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestSparkHandler_Search_ForeignWorkspaceID_Refused is the direct regression
// test for the query-tenant guard this fix had to satisfy
// (internal/middleware/query_tenant_verdict_test.go): an authenticated
// stranger — a member of SOME workspace, just not this one — passing another
// workspace's workspace_id must be refused before the resolver (and
// therefore the catalog it might point at) is ever reached.
func TestSparkHandler_Search_ForeignWorkspaceID_Refused(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	victimWS := uuid.New()
	stranger := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(victimWS, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{})
	// stranger is a member of nothing — memberOf is keyed to a DIFFERENT
	// workspace than victimWS, so GetRole(victimWS, stranger) misses.
	h := NewSparkHandler(resolver, nil, memberOf(uuid.New(), stranger))

	rec := doSparkGET(t, h, h.Search, "/?workspace_id="+victimWS.String(), func(c echo.Context) { asUser(c, stranger) })
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestSparkHandler_Search_AgentForeignWorkspaceID_Refused is the agent-actor
// counterpart: an agent may only ever pass the workspace_id its own key is
// bound to (mw.GetWorkspaceID), never another one.
func TestSparkHandler_Search_AgentForeignWorkspaceID_Refused(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	victimWS := uuid.New()
	agentOwnWS := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(victimWS, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{})
	h := NewSparkHandler(resolver, nil, nil)

	rec := doSparkGET(t, h, h.Search, "/?workspace_id="+victimWS.String(), func(c echo.Context) { asAgent(c, uuid.New(), agentOwnWS) })
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestSparkHandler_Search_AgentOwnWorkspaceID_Allowed proves the guard above
// is scoped to FOREIGN ids, not agents as a class — the exact distinction
// requireRegisterAgent draws for Install (agents forbidden outright) does NOT
// apply here, and this pins that on purpose.
func TestSparkHandler_Search_AgentOwnWorkspaceID_Allowed(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	ws := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{})
	h := NewSparkHandler(resolver, nil, nil)

	rec := doSparkGET(t, h, h.Search, "/?workspace_id="+ws.String(), func(c echo.Context) { asAgent(c, uuid.New(), ws) })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestSparkHandler_Popular_IsActiveFalse_RefusesWithNamedReason(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	ws := uuid.New()
	user := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: false, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{URL: catalog.URL, Enabled: true})
	h := NewSparkHandler(resolver, nil, memberOf(ws, user))

	rec := doSparkGET(t, h, h.Popular, "/?workspace_id="+ws.String(), func(c echo.Context) { asUser(c, user) })
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestSparkHandler_GetByID_IsActiveFalse_RefusesWithNamedReason(t *testing.T) {
	catalog := fakeSparkCatalogServer(t)
	ws := uuid.New()
	user := uuid.New()
	repo := newFakeSparkIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: false, Config: sparkHandlerCfg(catalog.URL)})
	resolver := service.NewSparkIntegrationResolver(repo, service.SparkEnvFallback{URL: catalog.URL, Enabled: true})
	h := NewSparkHandler(resolver, nil, memberOf(ws, user))

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?workspace_id="+ws.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asUser(c, user)
	c.SetParamNames("agent_id")
	c.SetParamValues("some-agent")
	require.NoError(t, h.GetByID(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}
