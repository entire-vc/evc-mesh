package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// No //go:build integration tag — same convention as every other *_db_test.go
// file in this codebase (see internal/repository/postgres/agent_api_key_sha256_db_test.go):
// runs against a real Postgres whenever one is reachable, skips otherwise.
//
// This is the end-to-end test task MCP-OAuth 1/5's acceptance criteria (§2)
// asks for: DCR client AND CIMD client, each through the full
// authorize -> consent -> code -> token -> GET /api/v1/agents/me chain, over
// real HTTP against a real echo router wired with the actual production
// handlers/middleware/services — no internal layer is mocked, only the
// outbound CIMD-fetch transport is swapped (via OAuthServiceConfigurable) so
// a loopback-bound httptest.Server can stand in for a real CIMD document
// host, which the real SSRF guard correctly refuses for anything else.

func oauthE2ETestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// oauthE2EEnv wires every real component the flow touches: real Postgres
// repos, the real agentService (with the U2 grant repo wired, same as
// cmd/api/main.go), the real oauthService, the real auth.Service, and a real
// echo.Echo serving the actual route set — a slice of what main.go
// registers, just enough for this flow, with the same middleware chain.
type oauthE2EEnv struct {
	db           *sqlx.DB
	authSvc      *auth.Service
	oauthSvc     service.OAuthService
	agentSvc     service.AgentService
	server       *httptest.Server
	client       *http.Client
	issuer       string
	oauthHandler *OAuthHandler
}

func newOAuthE2EEnv(t *testing.T) *oauthE2EEnv {
	t.Helper()
	db := oauthE2ETestDB(t)

	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	activityLogRepo := postgres.NewActivityLogRepo(db)
	agentWorkspaceGrantRepo := postgres.NewAgentWorkspaceGrantRepo(db)
	refreshTokenRepo := postgres.NewRefreshTokenRepo(db)
	oauthRepo := postgres.NewOAuthRepo(db)

	agentSvc := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo, userRepo)
	if configurable, ok := agentSvc.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(agentWorkspaceGrantRepo)
	}

	oauthSvc := service.NewOAuthService(oauthRepo, agentSvc, userRepo, workspaceRepo, workspaceMemberRepo, agentWorkspaceGrantRepo)

	authSvc := auth.NewService(userRepo, refreshTokenRepo, workspaceRepo, workspaceMemberRepo, "oauth-e2e-test-jwt-secret-do-not-use-in-prod")

	agentHandler := NewAgentHandler(agentSvc)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)

	// NewUnstartedServer binds the listener (so the port, and therefore the
	// issuer URL, is known) without serving yet — Authorize needs its own
	// issuer at route-registration time to build the consent redirect, so
	// routes cannot be added until this is known; a started server's routes
	// cannot cleanly be swapped out from under live traffic instead.
	server := httptest.NewUnstartedServer(e)
	issuer := "http://" + server.Listener.Addr().String()
	oauthHandler := NewOAuthHandler(oauthSvc, issuer)

	// Public OAuth endpoints — same paths as cmd/api/main.go, registered
	// directly on e (not under /api/v1), matching task MCP-OAuth 4/5's
	// routing split.
	// Through the same function production uses, limiters included — with
	// budgets no test in this file comes near, so it stays a functional
	// test; the limiting behaviour itself is oauth_routes_db_test.go.
	RegisterOAuthPublicRoutes(e, oauthHandler, oauthRepo, OAuthRateLimits{
		Enabled: true, Register: 10000, Authorize: 10000, AuthorizeNewClient: 10000, Token: 10000,
	})

	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, oauthSvc))
	api.GET("/agents/me", agentHandler.Me)
	api.GET("/oauth/consent", oauthHandler.ConsentInfo, mw.RequireUserAuth())
	api.POST("/oauth/consent", oauthHandler.Decide, mw.RequireUserAuth())
	api.GET("/oauth/grants", oauthHandler.ListGrants, mw.RequireUserAuth())
	api.DELETE("/oauth/grants/:oauth_grant_id", oauthHandler.RevokeGrant, mw.RequireUserAuth())

	server.Start()
	t.Cleanup(server.Close)
	require.Equal(t, issuer, server.URL, "the pre-Start issuer must match the actual server URL")

	return &oauthE2EEnv{
		db:       db,
		authSvc:  authSvc,
		oauthSvc: oauthSvc,
		agentSvc: agentSvc,
		server:   server,
		client: &http.Client{
			// Consent registers a connector agent, i.e. a bcrypt hash, and the
			// suite runs under -race next to whatever else the host is doing:
			// 10 s was measured too tight (consent POSTs timing out at load
			// average ~30, while the server side was still working).
			Timeout: 60 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		issuer:       server.URL,
		oauthHandler: oauthHandler,
	}
}

// registerTestUser creates a real user + default workspace through the real
// registration path and returns a valid access JWT plus the ids the rest of
// the flow needs.
func (env *oauthE2EEnv) registerTestUser(t *testing.T, emailPrefix string) (accessToken string, userID, workspaceID uuid.UUID, username string) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	email := fmt.Sprintf("%s-%s@oauth-e2e.example.com", emailPrefix, suffix)
	user, tokens, err := env.authSvc.Register(context.Background(), email, "Correct-Horse-Battery-Staple-1", emailPrefix+"-"+suffix)
	require.NoError(t, err)

	ws, err := postgres.NewWorkspaceRepo(env.db).ListForUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, ws, "Register must create a default workspace")

	return tokens.AccessToken, user.ID, ws[0].ID, user.Username
}

// pkcePair returns a fresh RFC 7636 S256 verifier/challenge pair.
func pkcePair() (verifier, challenge string) {
	raw := uuid.New().String() + uuid.New().String() // > 43 chars, URL-safe once hex/base64'd is not needed: uuid strings are already [0-9a-f-]
	verifier = raw
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// registerDCRClient runs a real POST /oauth/register and returns the issued client_id.
func (env *oauthE2EEnv) registerDCRClient(t *testing.T, redirectURI string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"redirect_uris": []string{redirectURI},
		"client_name":   "OAuth E2E Test Client",
	})
	resp, err := env.client.Post(env.server.URL+"/oauth/register", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	clientID, _ := out["client_id"].(string)
	require.NotEmpty(t, clientID)
	return clientID
}

// authorizeAndConsent drives GET /oauth/authorize (asserting it redirects to
// the consent screen, never itself), then GET+POST /api/v1/oauth/consent as
// the frontend would, and returns the redeemable authorization code.
func (env *oauthE2EEnv) authorizeAndConsent(t *testing.T, accessToken, clientID, redirectURI, challenge, state string, workspaceID uuid.UUID) string {
	t.Helper()

	authorizeURL := env.server.URL + "/oauth/authorize?" + url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"mesh offline_access"},
		"state":                 {state},
	}.Encode()

	resp, err := env.client.Get(authorizeURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode, "a fully valid authorize request must redirect, not render")
	loc := resp.Header.Get("Location")
	require.Contains(t, loc, env.issuer+oauthConsentPath, "must redirect to the frontend consent screen, not answer inline")

	locURL, err := url.Parse(loc)
	require.NoError(t, err)
	assert.Equal(t, clientID, locURL.Query().Get("client_id"), "the redirect must carry the original request through unchanged")

	// GET the consent info the frontend would render.
	infoReq, _ := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/oauth/consent?"+locURL.RawQuery, http.NoBody)
	infoReq.Header.Set("Authorization", "Bearer "+accessToken)
	infoResp, err := env.client.Do(infoReq)
	require.NoError(t, err)
	defer infoResp.Body.Close()
	require.Equal(t, http.StatusOK, infoResp.StatusCode)
	var info map[string]interface{}
	require.NoError(t, json.NewDecoder(infoResp.Body).Decode(&info))
	assert.NotEmpty(t, info["client_name"], "consent info must carry the client's display name")

	// POST the user's decision (Allow).
	decision, _ := json.Marshal(map[string]interface{}{
		"client_id":             clientID,
		"redirect_uri":          redirectURI,
		"response_type":         "code",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
		"scope":                 "mesh offline_access",
		"state":                 state,
		"workspace_id":          workspaceID,
		"allow":                 true,
	})
	decReq, _ := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/oauth/consent", bytes.NewReader(decision))
	decReq.Header.Set("Authorization", "Bearer "+accessToken)
	decReq.Header.Set("Content-Type", "application/json")
	decResp, err := env.client.Do(decReq)
	require.NoError(t, err)
	defer decResp.Body.Close()
	require.Equal(t, http.StatusOK, decResp.StatusCode)
	var decOut map[string]string
	require.NoError(t, json.NewDecoder(decResp.Body).Decode(&decOut))

	redirectBack, err := url.Parse(decOut["redirect_uri"])
	require.NoError(t, err)
	code := redirectBack.Query().Get("code")
	require.NotEmpty(t, code, "consent Allow must produce a code")
	assert.Equal(t, state, redirectBack.Query().Get("state"))
	return code
}

func (env *oauthE2EEnv) exchangeCode(t *testing.T, clientID, redirectURI, code, verifier string) map[string]interface{} {
	t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"code":          {code},
		"code_verifier": {verifier},
	}
	resp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "token exchange must succeed")
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func (env *oauthE2EEnv) meAgentName(t *testing.T, accessToken string) (status int, agentName string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/agents/me", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	name, _ := out["name"].(string)
	return resp.StatusCode, name
}

// --- The end-to-end scenario, DCR client ---

func TestOAuthEndToEnd_DCRClient(t *testing.T) {
	env := newOAuthE2EEnv(t)
	accessJWT, _, workspaceID, username := env.registerTestUser(t, "dcr-user")

	redirectURI := "http://localhost/callback"
	clientID := env.registerDCRClient(t, redirectURI)
	verifier, challenge := pkcePair()

	code := env.authorizeAndConsent(t, accessJWT, clientID, redirectURI, challenge, "state-dcr-1", workspaceID)

	tokens := env.exchangeCode(t, clientID, redirectURI, code, verifier)
	accessTok, _ := tokens["access_token"].(string)
	refreshTok, _ := tokens["refresh_token"].(string)
	require.True(t, len(accessTok) > 4 && accessTok[:4] == "mot_", "access token must carry the mot_ prefix, got %q", accessTok)
	require.NotEmpty(t, refreshTok)
	assert.Equal(t, "Bearer", tokens["token_type"])

	// AC2: GET /api/v1/agents/me under mot_ resolves to the connector agent.
	status, agentName := env.meAgentName(t, accessTok)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, agentName, "OAuth E2E Test Client")
	assert.Contains(t, agentName, username)

	// Refresh rotation: old refresh token dies, new pair works.
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshTok}}
	refreshResp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, refreshResp.StatusCode)
	var refreshed map[string]interface{}
	require.NoError(t, json.NewDecoder(refreshResp.Body).Decode(&refreshed))
	refreshResp.Body.Close()
	newAccess, _ := refreshed["access_token"].(string)
	newRefresh, _ := refreshed["refresh_token"].(string)
	assert.NotEqual(t, accessTok, newAccess)
	assert.NotEqual(t, refreshTok, newRefresh)

	statusAfterRefresh, _ := env.meAgentName(t, newAccess)
	assert.Equal(t, http.StatusOK, statusAfterRefresh, "the rotated access token must work")

	// RED CONTROL: reusing the OLD refresh token is a reuse-of-rotated-token
	// event — it must be denied AND kill the whole family, including the
	// brand-new pair issued right above.
	reuseResp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	defer reuseResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, reuseResp.StatusCode)
	var reuseErr map[string]string
	require.NoError(t, json.NewDecoder(reuseResp.Body).Decode(&reuseErr))
	assert.Equal(t, "invalid_grant", reuseErr["error"])

	statusAfterReuse, _ := env.meAgentName(t, newAccess)
	assert.Equal(t, http.StatusUnauthorized, statusAfterReuse, "refresh-token reuse must revoke the ENTIRE family, including tokens minted after the reused one")
}

// --- The end-to-end scenario, CIMD client ---

func TestOAuthEndToEnd_CIMDClient(t *testing.T) {
	env := newOAuthE2EEnv(t)
	accessJWT, _, workspaceID, username := env.registerTestUser(t, "cimd-user")

	redirectURI := "http://127.0.0.1/callback"
	// The CIMD document server: a second, independent httptest.Server whose
	// own URL becomes the client_id. Real cert, real TLS — the "SSRF-safe"
	// dial guard is bypassed ONLY for the AS's outbound fetch client, via
	// SetHTTPClientForTesting, not for anything else in the flow.
	var cimdURL string
	cimdServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"client_id":     cimdURL,
			"client_name":   "OAuth E2E CIMD Client",
			"redirect_uris": []string{redirectURI},
		})
	}))
	t.Cleanup(cimdServer.Close)
	cimdURL = cimdServer.URL + "/mcp-client.json"

	configurable, ok := env.oauthSvc.(service.OAuthServiceConfigurable)
	require.True(t, ok, "oauthService must implement OAuthServiceConfigurable")
	configurable.SetHTTPClientForTesting(cimdServer.Client())

	verifier, challenge := pkcePair()
	code := env.authorizeAndConsent(t, accessJWT, cimdURL, redirectURI, challenge, "state-cimd-1", workspaceID)

	tokens := env.exchangeCode(t, cimdURL, redirectURI, code, verifier)
	accessTok, _ := tokens["access_token"].(string)
	require.True(t, len(accessTok) > 4 && accessTok[:4] == "mot_")

	status, agentName := env.meAgentName(t, accessTok)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, agentName, "OAuth E2E CIMD Client")
	assert.Contains(t, agentName, username)
}

// --- Red controls (RFC 6749 acceptance criteria §3) ---

func TestOAuthRedControls(t *testing.T) {
	env := newOAuthE2EEnv(t)
	accessJWT, _, workspaceID, _ := env.registerTestUser(t, "red-controls-user")
	redirectURI := "http://localhost/callback"
	clientID := env.registerDCRClient(t, redirectURI)

	t.Run("wrong code_verifier is invalid_grant", func(t *testing.T) {
		_, challenge := pkcePair()
		code := env.authorizeAndConsent(t, accessJWT, clientID, redirectURI, challenge, "s1", workspaceID)
		form := url.Values{
			"grant_type": {"authorization_code"}, "client_id": {clientID},
			"redirect_uri": {redirectURI}, "code": {code}, "code_verifier": {"this-is-not-the-right-verifier-at-all-00000"},
		}
		resp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		var out map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		assert.Equal(t, "invalid_grant", out["error"])
	})

	t.Run("replayed code is invalid_grant on the second attempt", func(t *testing.T) {
		verifier, challenge := pkcePair()
		code := env.authorizeAndConsent(t, accessJWT, clientID, redirectURI, challenge, "s2", workspaceID)
		first := env.exchangeCode(t, clientID, redirectURI, code, verifier)
		require.NotEmpty(t, first["access_token"])

		form := url.Values{
			"grant_type": {"authorization_code"}, "client_id": {clientID},
			"redirect_uri": {redirectURI}, "code": {code}, "code_verifier": {verifier},
		}
		resp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		var out map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		assert.Equal(t, "invalid_grant", out["error"])
	})

	t.Run("redirect_uri not from registration never redirects", func(t *testing.T) {
		_, challenge := pkcePair()
		authorizeURL := env.server.URL + "/oauth/authorize?" + url.Values{
			"client_id": {clientID}, "redirect_uri": {"https://evil.example.com/steal"},
			"response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
			"state": {"s3"},
		}.Encode()
		resp, err := env.client.Get(authorizeURL)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.NotEqual(t, http.StatusFound, resp.StatusCode, "an unregistered redirect_uri must never produce a redirect")
		assert.True(t, resp.StatusCode >= 400, "must answer an error status directly")
	})

	t.Run("application/json on /oauth/token is a clean invalid_request, not a crash", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"grant_type": "authorization_code"})
		resp, err := env.client.Post(env.server.URL+"/oauth/token", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "must be a clean 4xx, never a 500")
		var out map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		assert.Equal(t, "invalid_request", out["error"])
	})
}

// --- Regression: existing auth forms are unaffected ---

func TestOAuthDoesNotBreakExistingAgentKeyAuth(t *testing.T) {
	env := newOAuthE2EEnv(t)
	_, _, workspaceID, _ := env.registerTestUser(t, "regression-user")

	reg, err := env.agentSvc.Register(context.Background(), service.RegisterAgentInput{
		WorkspaceID: workspaceID,
		Name:        "Plain Agent " + uuid.New().String()[:8],
		AgentType:   "custom",
	})
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/agents/me", http.NoBody)
	req.Header.Set("X-Agent-Key", reg.APIKey)
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "X-Agent-Key auth must keep working unmodified")
}

// TestOAuthServerMetadata is acceptance criterion §1: the RFC 8414 document
// carries every field the spec (Mesh Doc oauth-for-remote-mesh-mcp) requires.
func TestOAuthServerMetadata(t *testing.T) {
	env := newOAuthE2EEnv(t)
	resp, err := env.client.Get(env.server.URL + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var meta map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))

	assert.Equal(t, env.issuer, meta["issuer"])
	assert.Equal(t, env.issuer+"/oauth/authorize", meta["authorization_endpoint"])
	assert.Equal(t, env.issuer+"/oauth/token", meta["token_endpoint"])
	assert.Equal(t, env.issuer+"/oauth/register", meta["registration_endpoint"])
	assert.Equal(t, env.issuer+"/oauth/revoke", meta["revocation_endpoint"])
	assert.ElementsMatch(t, []interface{}{"S256"}, meta["code_challenge_methods_supported"])
	assert.Equal(t, true, meta["client_id_metadata_document_supported"])
	assert.ElementsMatch(t, []interface{}{"none"}, meta["token_endpoint_auth_methods_supported"])
	assert.ElementsMatch(t, []interface{}{"mesh", "offline_access"}, meta["scopes_supported"])
	assert.ElementsMatch(t, []interface{}{"authorization_code", "refresh_token"}, meta["grant_types_supported"])
	assert.ElementsMatch(t, []interface{}{"code"}, meta["response_types_supported"])
}

// ---------------------------------------------------------------------------
// MR !1008 review fixes (Garfield, 2026-09-24): B1 (connector perms <= the
// consenting user's role), B2 (redirect_uri scheme allow-list), M2 (agent
// name collision on a second connector for the same client_name).
// M1 (concurrent refresh) lives in oauth_service_test.go — it does not need
// a real DB, just the repo's RevokeToken race, so it is a plain unit test.
// ---------------------------------------------------------------------------

// addWorkspaceMember inserts a workspace_members row directly — the fastest
// way to put a second, non-owner user into an existing workspace for a test
// without going through the full invite-accept flow this file otherwise has
// no need for.
func (env *oauthE2EEnv) addWorkspaceMember(t *testing.T, workspaceID, userID uuid.UUID, role string) {
	t.Helper()
	_, err := env.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())`,
		uuid.New(), workspaceID, userID, role,
	)
	require.NoError(t, err)
}

func TestOAuthViewerCannotConsent(t *testing.T) {
	env := newOAuthE2EEnv(t)
	_, ownerID, workspaceID, _ := env.registerTestUser(t, "viewer-owner")
	viewerToken, viewerID, _, _ := env.registerTestUser(t, "viewer-user")
	require.NotEqual(t, ownerID, viewerID)
	env.addWorkspaceMember(t, workspaceID, viewerID, domain.RoleViewer)

	redirectURI := "http://localhost/callback"
	clientID := env.registerDCRClient(t, redirectURI)
	_, challenge := pkcePair()

	authorizeURL := env.server.URL + "/oauth/authorize?" + url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"vw1"},
	}.Encode()
	authResp, err := env.client.Get(authorizeURL)
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)

	decision, _ := json.Marshal(map[string]interface{}{
		"client_id": clientID, "redirect_uri": redirectURI, "response_type": "code",
		"code_challenge": challenge, "code_challenge_method": "S256", "state": "vw1",
		"workspace_id": workspaceID, "allow": true,
	})
	decReq, _ := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/oauth/consent", bytes.NewReader(decision))
	decReq.Header.Set("Authorization", "Bearer "+viewerToken)
	decReq.Header.Set("Content-Type", "application/json")
	decResp, err := env.client.Do(decReq)
	require.NoError(t, err)
	defer decResp.Body.Close()
	assert.Equal(t, http.StatusForbidden, decResp.StatusCode, "a viewer must never be able to connect an external application")

	var count int
	require.NoError(t, env.db.Get(&count, `SELECT count(*) FROM oauth_grants WHERE workspace_id = $1 AND user_id = $2`, workspaceID, viewerID))
	assert.Zero(t, count, "no grant, and therefore no connector agent, must have been created for the refused viewer")
}

func TestOAuthRedirectURISchemeRejected(t *testing.T) {
	env := newOAuthE2EEnv(t)

	cases := []struct {
		name string
		uri  string
	}{
		{"javascript scheme", "javascript://x/%0aalert(1)"},
		{"data scheme", "data:text/html,<script>alert(1)</script>"},
		{"http on a non-loopback host", "http://evil.example.com/callback"},
		{"fragment", "https://client.example.com/callback#frag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{
				"redirect_uris": []string{tc.uri},
				"client_name":   "Malicious Redirect Test Client",
			})
			resp, err := env.client.Post(env.server.URL+"/oauth/register", "application/json", bytes.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "must be refused at registration, before it could ever be redirected to")
			var out map[string]string
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
			assert.Equal(t, "invalid_client_metadata", out["error"])
		})
	}

	t.Run("https and loopback http are still accepted", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"redirect_uris": []string{"https://client.example.com/callback", "http://127.0.0.1:51000/callback"},
			"client_name":   "Legitimate Client",
		})
		resp, err := env.client.Post(env.server.URL+"/oauth/register", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
	})
}

func TestOAuthConnectorNameCollisionIsRetried(t *testing.T) {
	env := newOAuthE2EEnv(t)
	accessJWT, _, workspaceID, _ := env.registerTestUser(t, "collision-user")
	redirectURI := "http://localhost/callback"

	// Two separate DCR registrations sharing the same client_name — the
	// per-install-DCR shape M2 was filed against — must each be able to
	// consent successfully instead of the second one 500ing on
	// uq_agents_workspace_slug.
	client1 := env.registerDCRClient(t, redirectURI)
	client2 := env.registerDCRClient(t, redirectURI)
	require.NotEqual(t, client1, client2, "two independent DCR registrations must not collide on client_id")

	verifier1, challenge1 := pkcePair()
	code1 := env.authorizeAndConsent(t, accessJWT, client1, redirectURI, challenge1, "coll1", workspaceID)
	tr1 := env.exchangeCode(t, client1, redirectURI, code1, verifier1)
	require.NotEmpty(t, tr1["access_token"])

	verifier2, challenge2 := pkcePair()
	code2 := env.authorizeAndConsent(t, accessJWT, client2, redirectURI, challenge2, "coll2", workspaceID)
	tr2 := env.exchangeCode(t, client2, redirectURI, code2, verifier2)
	require.NotEmpty(t, tr2["access_token"], "the second connector of the same client_name must still succeed, not 500")

	assert.NotEqual(t, tr1["access_token"], tr2["access_token"])

	var agentCount int
	require.NoError(t, env.db.Get(&agentCount,
		`SELECT count(*) FROM oauth_grants WHERE workspace_id = $1 AND client_id IN ($2, $3)`,
		workspaceID, client1, client2))
	assert.Equal(t, 2, agentCount, "each DCR registration gets its own grant/connector agent, not a reused or failed one")
}

// TestOAuthConcurrentRefreshExactlyOneWins is M1: two requests racing to
// refresh the exact same token must not both succeed — that would fork the
// family without either being flagged as reuse. RevokeToken's RowsAffected
// check (oauth_repo.go) is what this exercises; the family-revoking fallback
// in RefreshTokenGrant is what makes the loser's response indistinguishable
// from a genuine reuse attempt rather than a bare server error.
func TestOAuthConcurrentRefreshExactlyOneWins(t *testing.T) {
	env := newOAuthE2EEnv(t)
	accessJWT, _, workspaceID, _ := env.registerTestUser(t, "race-user")
	redirectURI := "http://localhost/callback"
	clientID := env.registerDCRClient(t, redirectURI)
	verifier, challenge := pkcePair()
	code := env.authorizeAndConsent(t, accessJWT, clientID, redirectURI, challenge, "race1", workspaceID)
	tr := env.exchangeCode(t, clientID, redirectURI, code, verifier)
	refreshToken, _ := tr["refresh_token"].(string)
	require.NotEmpty(t, refreshToken)

	const concurrency = 8
	var wg sync.WaitGroup
	statuses := make([]int, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			form := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}}
			resp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
			if err != nil {
				statuses[idx] = -1
				return
			}
			defer resp.Body.Close()
			statuses[idx] = resp.StatusCode
		}(i)
	}
	wg.Wait()

	successes, failures := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			successes++
		case http.StatusBadRequest:
			failures++
		default:
			t.Fatalf("unexpected status %d from a concurrent refresh", s)
		}
	}
	assert.Equal(t, 1, successes, "exactly one concurrent refresh of the same token may succeed")
	assert.Equal(t, concurrency-1, failures, "every other concurrent refresh of the same token must be refused")
}

// TestOAuthRepo_RevokeToken_ConcurrentCallsExactlyOneSucceeds is M1 at the
// layer the bug actually lived in: OAuthRepo.RevokeToken's `revoked_at IS
// NULL` guard is what makes losing the race distinguishable from winning it
// — this drives that guard directly with true, simultaneous callers (all
// released by one WaitGroup) instead of through a full HTTP round trip,
// where each request's own upstream latency was enough to serialize them
// before ever reaching the contested UPDATE.
func TestOAuthRepo_RevokeToken_ConcurrentCallsExactlyOneSucceeds(t *testing.T) {
	env := newOAuthE2EEnv(t)
	accessJWT, _, workspaceID, _ := env.registerTestUser(t, "repo-race-user")
	redirectURI := "http://localhost/callback"
	clientID := env.registerDCRClient(t, redirectURI)
	verifier, challenge := pkcePair()
	code := env.authorizeAndConsent(t, accessJWT, clientID, redirectURI, challenge, "reporace1", workspaceID)
	tr := env.exchangeCode(t, clientID, redirectURI, code, verifier)
	refreshToken, _ := tr["refresh_token"].(string)
	require.NotEmpty(t, refreshToken)

	sum := sha256.Sum256([]byte(refreshToken))
	tokenHash := hex.EncodeToString(sum[:])
	var tokenID uuid.UUID
	require.NoError(t, env.db.Get(&tokenID, `SELECT id FROM oauth_tokens WHERE token_hash = $1`, tokenHash))

	repo := postgres.NewOAuthRepo(env.db)
	const concurrency = 16
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	oks := make([]bool, concurrency)
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start.Wait()
			ok, err := repo.RevokeToken(context.Background(), tokenID, time.Now())
			oks[idx], errs[idx] = ok, err
		}(i)
	}
	start.Done() // release every goroutine at once
	wg.Wait()

	successCount := 0
	for i, ok := range oks {
		require.NoError(t, errs[i])
		if ok {
			successCount++
		}
	}
	assert.Equal(t, 1, successCount, "exactly one of N truly concurrent RevokeToken calls on the same row must observe ok=true")
}
