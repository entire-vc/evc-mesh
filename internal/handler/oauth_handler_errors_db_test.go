package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Error-path coverage for OAuthHandler, over the same real stack as
// oauth_e2e_db_test.go (real echo router, real services, real Postgres).
// The one departure is newOAuthDeadDBServer below: it wires the real
// services over a real *sqlx.DB pool that has been closed, which is how a
// database outage actually presents to this code — every query fails at the
// driver — and is the only way to reach the server_error paths without
// mocking an internal layer.

// internalLeakMarkers are fragments that would only appear in a response
// body if a database/driver error string escaped to the caller.
var internalLeakMarkers = []string{"sql:", "pq:", "database is closed", "SELECT", "oauth_tokens", "oauth_clients", "dial tcp"}

func assertNoInternalLeak(t *testing.T, body string) {
	t.Helper()
	for _, m := range internalLeakMarkers {
		assert.NotContains(t, body, m, "response body leaks internal detail %q: %s", m, body)
	}
}

type oauthResp struct {
	status int
	header http.Header
	raw    string
	body   map[string]interface{}
}

func doOAuth(t *testing.T, client *http.Client, req *http.Request) oauthResp {
	t.Helper()
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := oauthResp{status: resp.StatusCode, header: resp.Header, raw: string(b)}
	if len(b) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(b, &out.body)
	}
	return out
}

func postForm(t *testing.T, env *oauthE2EEnv, path string, form url.Values) oauthResp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doOAuth(t, env.client, req)
}

func jsonReq(t *testing.T, method, target, bearer string, body interface{}) *http.Request {
	t.Helper()
	var r io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, target, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// assertOAuthError checks the RFC 6749 §5.2 shape: status, "error" code, and
// nothing internal in the body.
func assertOAuthError(t *testing.T, r oauthResp, status int, code string) {
	t.Helper()
	assert.Equal(t, status, r.status, "body: %s", r.raw)
	require.NotNil(t, r.body, "error body must be JSON: %s", r.raw)
	assert.Equal(t, code, r.body["error"], "body: %s", r.raw)
	assertNoInternalLeak(t, r.raw)
}

func assertNoStore(t *testing.T, r oauthResp) {
	t.Helper()
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"), "RFC 6749 §5.1/§5.2: /oauth/token responses must not be cached")
	assert.Equal(t, "no-cache", r.header.Get("Pragma"))
}

// ---------------------------------------------------------------------------
// /oauth/token
// ---------------------------------------------------------------------------

func TestOAuthHandler_Token_ErrorResponses(t *testing.T) {
	env := newOAuthE2EEnv(t)
	clientID := env.registerDCRClient(t, "http://localhost/cb-token-errors")

	cases := []struct {
		name   string
		form   url.Values
		status int
		code   string
	}{
		{"missing grant_type", url.Values{"client_id": {clientID}}, http.StatusBadRequest, "invalid_request"},
		{"unsupported grant_type", url.Values{"grant_type": {"password"}, "client_id": {clientID}}, http.StatusBadRequest, "unsupported_grant_type"},
		{"client_credentials not offered", url.Values{"grant_type": {"client_credentials"}}, http.StatusBadRequest, "unsupported_grant_type"},
		{"unknown authorization code", url.Values{
			"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {"http://localhost/cb-token-errors"},
			"code": {"not-a-real-code"}, "code_verifier": {strings.Repeat("v", 64)},
		}, http.StatusBadRequest, "invalid_grant"},
		{"unknown refresh token", url.Values{
			"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {"not-a-real-refresh-token"},
		}, http.StatusBadRequest, "invalid_grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := postForm(t, env, "/oauth/token", tc.form)
			assertOAuthError(t, r, tc.status, tc.code)
			assertNoStore(t, r)
			assert.Nil(t, r.body["access_token"])
		})
	}
}

func TestOAuthHandler_Token_UnsupportedGrantTypeIsEchoedSafely(t *testing.T) {
	env := newOAuthE2EEnv(t)
	r := postForm(t, env, "/oauth/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
	assertOAuthError(t, r, http.StatusBadRequest, "unsupported_grant_type")
	assert.Contains(t, r.body["error_description"], "device_code", "the description names what was rejected")
}

// Token reads the POST body only (PostFormValue): grant_type smuggled via the
// query string must be ignored, not honoured.
func TestOAuthHandler_Token_QueryStringParamsIgnored(t *testing.T) {
	env := newOAuthE2EEnv(t)
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/oauth/token?grant_type=refresh_token&refresh_token=x", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r := doOAuth(t, env.client, req)
	assertOAuthError(t, r, http.StatusBadRequest, "invalid_request")
	assertNoStore(t, r)
}

// ---------------------------------------------------------------------------
// /oauth/register
// ---------------------------------------------------------------------------

func TestOAuthHandler_Register_Errors(t *testing.T) {
	env := newOAuthE2EEnv(t)
	t.Run("malformed JSON", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, env.server.URL+"/oauth/register", "", `{"redirect_uris": [`))
		assertOAuthError(t, r, http.StatusBadRequest, "invalid_client_metadata")
	})
	t.Run("no redirect_uris", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, env.server.URL+"/oauth/register", "", map[string]interface{}{"client_name": "x"}))
		assertOAuthError(t, r, http.StatusBadRequest, "invalid_client_metadata")
	})
	t.Run("success shape", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, env.server.URL+"/oauth/register", "", map[string]interface{}{
			"client_name": "Shape Check", "redirect_uris": []string{"https://shape.example.com/cb"},
		}))
		require.Equal(t, http.StatusCreated, r.status, r.raw)
		assert.Equal(t, "none", r.body["token_endpoint_auth_method"])
		assert.Equal(t, "Shape Check", r.body["client_name"])
		assert.NotEmpty(t, r.body["client_id_issued_at"])
		assert.Nil(t, r.body["client_secret"], "public clients only — no secret is ever issued")
	})
}

// ---------------------------------------------------------------------------
// /oauth/authorize
// ---------------------------------------------------------------------------

func authorizeURL(env *oauthE2EEnv, v url.Values) string {
	return env.server.URL + "/oauth/authorize?" + v.Encode()
}

func TestOAuthHandler_Authorize_UntrustedClientOrRedirectIsRenderedNotRedirected(t *testing.T) {
	env := newOAuthE2EEnv(t)
	clientID := env.registerDCRClient(t, "http://localhost/cb-authz")
	_, challenge := pkcePair()

	cases := []struct {
		name  string
		query url.Values
		code  string
	}{
		{"missing client_id", url.Values{"redirect_uri": {"http://localhost/cb-authz"}}, "invalid_request"},
		{"unknown client_id", url.Values{"client_id": {"mcpc_does_not_exist_" + uuid.NewString()}, "redirect_uri": {"https://evil.example.com/cb"}}, "invalid_client"},
		{"unregistered redirect_uri", url.Values{
			"client_id": {clientID}, "redirect_uri": {"https://evil.example.com/cb"}, "response_type": {"code"},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		}, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := doOAuth(t, env.client, jsonReq(t, http.MethodGet, authorizeURL(env, tc.query), "", nil))
			assert.Empty(t, r.header.Get("Location"), "RFC 6749 §4.1.2.1: never redirect to an unverified redirect_uri")
			assert.NotEqual(t, http.StatusFound, r.status)
			require.NotNil(t, r.body, r.raw)
			assert.Equal(t, tc.code, r.body["error"])
			assertNoInternalLeak(t, r.raw)
		})
	}
}

func TestOAuthHandler_Authorize_RequestErrorRedirectsBackWithStateAndErrorCode(t *testing.T) {
	env := newOAuthE2EEnv(t)
	redirectURI := "http://localhost/cb-authz-redir"
	clientID := env.registerDCRClient(t, redirectURI)
	_, challenge := pkcePair()

	t.Run("unsupported response_type, with state", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodGet, authorizeURL(env, url.Values{
			"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"token"},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"s t&ate"},
		}), "", nil))
		require.Equal(t, http.StatusFound, r.status)
		loc, err := url.Parse(r.header.Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "localhost", loc.Host)
		assert.Equal(t, "/cb-authz-redir", loc.Path)
		assert.Equal(t, "unsupported_response_type", loc.Query().Get("error"))
		assert.Equal(t, "s t&ate", loc.Query().Get("state"), "state must round-trip, correctly escaped")
		assert.Empty(t, loc.Query().Get("code"))
	})

	t.Run("missing PKCE, no state", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodGet, authorizeURL(env, url.Values{
			"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		}), "", nil))
		require.Equal(t, http.StatusFound, r.status)
		loc, err := url.Parse(r.header.Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "invalid_request", loc.Query().Get("error"))
		_, hasState := loc.Query()["state"]
		assert.False(t, hasState, "no state param when the client sent none")
	})
}

// The redirect_uri may itself carry a query string — the error must be
// appended with "&", preserving the client's own parameters.
func TestAppendErrorParam(t *testing.T) {
	assert.Equal(t, "https://c.example/cb?error=access_denied&state=xyz",
		appendErrorParam("https://c.example/cb", "access_denied", "xyz"))
	assert.Equal(t, "https://c.example/cb?a=1&error=invalid_request",
		appendErrorParam("https://c.example/cb?a=1", "invalid_request", ""))
	assert.Equal(t, "https://c.example/cb?error=x&state=a%26b%3Dc",
		appendErrorParam("https://c.example/cb", "x", "a&b=c"))
}

// ---------------------------------------------------------------------------
// Consent API
// ---------------------------------------------------------------------------

func TestOAuthHandler_ConsentInfo_InvalidRequestIsOAuthError(t *testing.T) {
	env := newOAuthE2EEnv(t)
	jwt, _, _, _ := env.registerTestUser(t, "consentinfo-err")
	redirectURI := "http://localhost/cb-consentinfo"
	clientID := env.registerDCRClient(t, redirectURI)

	t.Run("unknown client", func(t *testing.T) {
		q := url.Values{"client_id": {"mcpc_nope_" + uuid.NewString()}, "redirect_uri": {redirectURI}}
		r := doOAuth(t, env.client, jsonReq(t, http.MethodGet, env.server.URL+"/api/v1/oauth/consent?"+q.Encode(), jwt, nil))
		assertOAuthError(t, r, http.StatusUnauthorized, "invalid_client")
	})
	t.Run("no PKCE", func(t *testing.T) {
		q := url.Values{"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}}
		r := doOAuth(t, env.client, jsonReq(t, http.MethodGet, env.server.URL+"/api/v1/oauth/consent?"+q.Encode(), jwt, nil))
		assertOAuthError(t, r, http.StatusBadRequest, "invalid_request")
	})
}

func TestOAuthHandler_Decide_Errors(t *testing.T) {
	env := newOAuthE2EEnv(t)
	jwt, _, wsID, _ := env.registerTestUser(t, "decide-err")
	redirectURI := "http://localhost/cb-decide"
	clientID := env.registerDCRClient(t, redirectURI)
	_, challenge := pkcePair()
	consentURL := env.server.URL + "/api/v1/oauth/consent"

	t.Run("malformed body", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, consentURL, jwt, `{"allow": tru`))
		assert.Equal(t, http.StatusBadRequest, r.status, r.raw)
		assertNoInternalLeak(t, r.raw)
	})
	t.Run("unknown client is an OAuth error, not a redirect", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, consentURL, jwt, map[string]interface{}{
			"client_id": "mcpc_nope_" + uuid.NewString(), "redirect_uri": redirectURI, "response_type": "code",
			"code_challenge": challenge, "code_challenge_method": "S256", "workspace_id": wsID, "allow": true,
		}))
		assertOAuthError(t, r, http.StatusUnauthorized, "invalid_client")
		assert.Nil(t, r.body["redirect_uri"])
	})
	t.Run("deny redirects back with access_denied and no code", func(t *testing.T) {
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, consentURL, jwt, map[string]interface{}{
			"client_id": clientID, "redirect_uri": redirectURI, "response_type": "code",
			"code_challenge": challenge, "code_challenge_method": "S256", "state": "deny-1",
			"workspace_id": wsID, "allow": false,
		}))
		require.Equal(t, http.StatusOK, r.status, r.raw)
		back, err := url.Parse(r.body["redirect_uri"].(string))
		require.NoError(t, err)
		assert.Equal(t, "access_denied", back.Query().Get("error"))
		assert.Equal(t, "deny-1", back.Query().Get("state"))
		assert.Empty(t, back.Query().Get("code"))
	})
	t.Run("workspace the user is not a member of is 403", func(t *testing.T) {
		_, _, otherWS, _ := env.registerTestUser(t, "decide-other")
		r := doOAuth(t, env.client, jsonReq(t, http.MethodPost, consentURL, jwt, map[string]interface{}{
			"client_id": clientID, "redirect_uri": redirectURI, "response_type": "code",
			"code_challenge": challenge, "code_challenge_method": "S256",
			"workspace_id": otherWS, "allow": true,
		}))
		assert.Equal(t, http.StatusForbidden, r.status, r.raw)
	})
}

// ---------------------------------------------------------------------------
// Grants API
// ---------------------------------------------------------------------------

func TestOAuthHandler_Grants_ListRevokeScopedToUser(t *testing.T) {
	env := newOAuthE2EEnv(t)
	aliceJWT, aliceID, aliceWS, _ := env.registerTestUser(t, "grants-alice")
	bobJWT, _, _, _ := env.registerTestUser(t, "grants-bob")
	redirectURI := "http://localhost/cb-grants"
	clientID := env.registerDCRClient(t, redirectURI)
	verifier, challenge := pkcePair()

	code := env.authorizeAndConsent(t, aliceJWT, clientID, redirectURI, challenge, "g-1", aliceWS)
	tokens := env.exchangeCode(t, clientID, redirectURI, code, verifier)
	access := tokens["access_token"].(string)
	refresh := tokens["refresh_token"].(string)
	status, _ := env.meAgentName(t, access)
	require.Equal(t, http.StatusOK, status)

	grantsURL := env.server.URL + "/api/v1/oauth/grants"
	list := doOAuth(t, env.client, jsonReq(t, http.MethodGet, grantsURL, aliceJWT, nil))
	require.Equal(t, http.StatusOK, list.status, list.raw)
	grants := list.body["grants"].([]interface{})
	require.Len(t, grants, 1)
	g := grants[0].(map[string]interface{})
	assert.Equal(t, clientID, g["client_id"])
	assert.Equal(t, aliceID.String(), g["user_id"])
	assert.Equal(t, "OAuth E2E Test Client", g["client_name"])
	assert.Nil(t, g["revoked_at"])
	grantID := g["id"].(string)

	bobList := doOAuth(t, env.client, jsonReq(t, http.MethodGet, grantsURL, bobJWT, nil))
	require.Equal(t, http.StatusOK, bobList.status)
	assert.Empty(t, bobList.body["grants"], "another user must not see alice's grant")

	// Bob cannot revoke alice's grant — and the answer is 404, not 403, so it
	// doesn't confirm the grant exists.
	r := doOAuth(t, env.client, jsonReq(t, http.MethodDelete, grantsURL+"/"+grantID, bobJWT, nil))
	assert.Equal(t, http.StatusNotFound, r.status, r.raw)
	status, _ = env.meAgentName(t, access)
	assert.Equal(t, http.StatusOK, status, "a refused revoke must leave the token working")

	r = doOAuth(t, env.client, jsonReq(t, http.MethodDelete, grantsURL+"/not-a-uuid", aliceJWT, nil))
	assert.Equal(t, http.StatusBadRequest, r.status, r.raw)

	r = doOAuth(t, env.client, jsonReq(t, http.MethodDelete, grantsURL+"/"+uuid.NewString(), aliceJWT, nil))
	assert.Equal(t, http.StatusNotFound, r.status, r.raw)

	r = doOAuth(t, env.client, jsonReq(t, http.MethodDelete, grantsURL+"/"+grantID, aliceJWT, nil))
	require.Equal(t, http.StatusNoContent, r.status, r.raw)

	// Revoking the grant kills its tokens: access 401, refresh invalid_grant.
	status, _ = env.meAgentName(t, access)
	assert.Equal(t, http.StatusUnauthorized, status)
	rr := postForm(t, env, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh}})
	assertOAuthError(t, rr, http.StatusBadRequest, "invalid_grant")

	list = doOAuth(t, env.client, jsonReq(t, http.MethodGet, grantsURL, aliceJWT, nil))
	require.Equal(t, http.StatusOK, list.status)
	g = list.body["grants"].([]interface{})[0].(map[string]interface{})
	assert.NotNil(t, g["revoked_at"], "a revoked grant stays listed, marked revoked")
}

// The consent/grants API is user-only. A mot_ token (the connector itself)
// must not be able to list or revoke grants.
func TestOAuthHandler_Grants_RejectsConnectorToken(t *testing.T) {
	env := newOAuthE2EEnv(t)
	jwt, _, ws, _ := env.registerTestUser(t, "grants-mot")
	redirectURI := "http://localhost/cb-grants-mot"
	clientID := env.registerDCRClient(t, redirectURI)
	verifier, challenge := pkcePair()
	code := env.authorizeAndConsent(t, jwt, clientID, redirectURI, challenge, "m-1", ws)
	access := env.exchangeCode(t, clientID, redirectURI, code, verifier)["access_token"].(string)

	r := doOAuth(t, env.client, jsonReq(t, http.MethodGet, env.server.URL+"/api/v1/oauth/grants", access, nil))
	assert.Equal(t, http.StatusUnauthorized, r.status, r.raw)
}

// Handlers must refuse on their own if ever mounted without the user-auth
// middleware — defense in depth for a routing mistake.
func TestOAuthHandler_UserEndpoints_RequireUserInContext(t *testing.T) {
	env := newOAuthE2EEnv(t)
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	e.GET("/consent", env.oauthHandler.ConsentInfo)
	e.POST("/consent", env.oauthHandler.Decide)
	e.GET("/grants", env.oauthHandler.ListGrants)
	e.DELETE("/grants/:oauth_grant_id", env.oauthHandler.RevokeGrant)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/consent"},
		{http.MethodPost, "/consent"},
		{http.MethodGet, "/grants"},
		{http.MethodDelete, "/grants/" + uuid.NewString()},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
			assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Database outage: server_error paths
// ---------------------------------------------------------------------------

// syncBuffer is a goroutine-safe log sink (the server writes from its own
// goroutines; the test reads afterwards).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type deadDBServer struct {
	url    string
	client *http.Client
	logs   *syncBuffer
}

// newOAuthDeadDBServer serves the OAuth routes from real services whose
// repositories sit on a closed connection pool. JWTs are validated by the
// live env's auth service (stateless signature check), so user-authenticated
// routes get past auth and then fail at the database.
func newOAuthDeadDBServer(t *testing.T, live *oauthE2EEnv) *deadDBServer {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	dead, err := sqlx.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, dead.Close())

	agentSvc := service.NewAgentService(postgres.NewAgentRepo(dead), postgres.NewActivityLogRepo(dead), postgres.NewWorkspaceRepo(dead), postgres.NewUserRepo(dead))
	oauthSvc := service.NewOAuthService(postgres.NewOAuthRepo(dead), agentSvc, postgres.NewUserRepo(dead), postgres.NewWorkspaceRepo(dead), postgres.NewWorkspaceMemberRepo(dead), postgres.NewAgentWorkspaceGrantRepo(dead))
	h := NewOAuthHandler(oauthSvc, "https://mesh.example.test")

	logs := &syncBuffer{}
	e := echo.New()
	e.Logger.SetOutput(logs)
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	e.POST("/oauth/register", h.Register)
	e.GET("/oauth/authorize", h.Authorize)
	e.POST("/oauth/token", h.Token)
	e.POST("/oauth/revoke", h.Revoke)
	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(live.authSvc, live.agentSvc, nil))
	api.GET("/oauth/consent", h.ConsentInfo, mw.RequireUserAuth())
	api.POST("/oauth/consent", h.Decide, mw.RequireUserAuth())
	api.GET("/oauth/grants", h.ListGrants, mw.RequireUserAuth())
	api.DELETE("/oauth/grants/:oauth_grant_id", h.RevokeGrant, mw.RequireUserAuth())

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return &deadDBServer{url: srv.URL, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, logs: logs}
}

func TestOAuthHandler_DatabaseDown_ServerErrorIsRedacted(t *testing.T) {
	live := newOAuthE2EEnv(t)
	jwt, _, ws, _ := live.registerTestUser(t, "deaddb")
	dead := newOAuthDeadDBServer(t, live)
	_, challenge := pkcePair()
	authz := url.Values{
		"client_id": {"mcpc_any_" + uuid.NewString()}, "redirect_uri": {"http://localhost/cb"}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}

	form := func(path string, v url.Values) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, dead.url+path, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}

	cases := []struct {
		name string
		req  *http.Request
	}{
		{"revoke", form("/oauth/revoke", url.Values{"token": {"mot_whatever"}})},
		{"token authorization_code", form("/oauth/token", url.Values{
			"grant_type": {"authorization_code"}, "client_id": {"mcpc_x"}, "redirect_uri": {"http://localhost/cb"},
			"code": {"c"}, "code_verifier": {strings.Repeat("v", 64)},
		})},
		{"token refresh_token", form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {"mcpc_x"}, "refresh_token": {"mor_well-formed-but-unverifiable"}})},
		{"register", jsonReq(t, http.MethodPost, dead.url+"/oauth/register", "", map[string]interface{}{"redirect_uris": []string{"https://ok.example.com/cb"}})},
		{"authorize", jsonReq(t, http.MethodGet, dead.url+"/oauth/authorize?"+authz.Encode(), "", nil)},
		{"consent info", jsonReq(t, http.MethodGet, dead.url+"/api/v1/oauth/consent?"+authz.Encode(), jwt, nil)},
		{"consent decide", jsonReq(t, http.MethodPost, dead.url+"/api/v1/oauth/consent", jwt, map[string]interface{}{
			"client_id": authz.Get("client_id"), "redirect_uri": "http://localhost/cb", "response_type": "code",
			"code_challenge": challenge, "code_challenge_method": "S256", "workspace_id": ws, "allow": true,
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := doOAuth(t, dead.client, tc.req)
			assert.Equal(t, http.StatusInternalServerError, r.status, r.raw)
			require.NotNil(t, r.body, r.raw)
			assert.Equal(t, "server_error", r.body["error"])
			assert.Equal(t, "internal server error", r.body["error_description"])
			assertNoInternalLeak(t, r.raw)
			assert.Empty(t, r.header.Get("Location"), "an outage must never turn into a redirect")
		})
	}

	// The real cause is not lost — it goes to the server log instead.
	logged := dead.logs.String()
	assert.Contains(t, logged, "server_error")
	assert.Contains(t, logged, "database is closed", "the internal description must be logged server-side")
	assert.Contains(t, logged, "/oauth/revoke")
}

func TestOAuthHandler_DatabaseDown_RevokeWithEmptyTokenStill200(t *testing.T) {
	// RFC 7009 §2.2: an empty/unknown token is not an error. With no token
	// there is nothing to look up, so even a dead database answers 200.
	live := newOAuthE2EEnv(t)
	dead := newOAuthDeadDBServer(t, live)
	req, _ := http.NewRequest(http.MethodPost, dead.url+"/oauth/revoke", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r := doOAuth(t, dead.client, req)
	assert.Equal(t, http.StatusOK, r.status, r.raw)
}

func TestOAuthHandler_DatabaseDown_GrantsAPI500(t *testing.T) {
	live := newOAuthE2EEnv(t)
	jwt, _, _, _ := live.registerTestUser(t, "deaddb-grants")
	dead := newOAuthDeadDBServer(t, live)

	list := doOAuth(t, dead.client, jsonReq(t, http.MethodGet, dead.url+"/api/v1/oauth/grants", jwt, nil))
	assert.Equal(t, http.StatusInternalServerError, list.status, list.raw)
	assert.Equal(t, "failed to list connected apps", list.body["message"])
	assertNoInternalLeak(t, list.raw)

	del := doOAuth(t, dead.client, jsonReq(t, http.MethodDelete, dead.url+"/api/v1/oauth/grants/"+uuid.NewString(), jwt, nil))
	assert.Equal(t, http.StatusInternalServerError, del.status, del.raw)
	assertNoInternalLeak(t, del.raw)
}
