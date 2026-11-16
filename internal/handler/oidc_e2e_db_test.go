package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/service"
)

// OpenID Connect identity flow, end to end over real HTTP on
// the production routes, real Postgres, real services — the oauth_e2e
// environment with OIDC configured the way cmd/api configures it. Skips
// without a reachable Postgres, like every *_db_test.go here.

type oidcE2E struct {
	*oauthE2EEnv
	keys        *service.OIDCKeySet
	clientID    string
	redirectURI string
}

func newOIDCE2E(t *testing.T, keys *service.OIDCKeySet) *oidcE2E {
	t.Helper()
	env := newOAuthE2EEnv(t)
	clientID := "keep-console-" + uuid.New().String()[:8]
	redirectURI := "https://keep.example.com/console/login/oidc/callback"
	settings := service.OIDCSettings{
		Issuer: env.issuer,
		Keys:   keys,
		Clients: []service.OIDCStaticClient{{
			ClientID:     clientID,
			ClientName:   "Keep",
			RedirectURIs: []string{redirectURI, "http://localhost:3000/cb"},
		}},
	}
	require.NoError(t, env.oauthSvc.(service.OIDCConfigurable).ConfigureOIDC(context.Background(), settings))
	t.Cleanup(func() { _, _ = env.db.Exec(`DELETE FROM oauth_clients WHERE client_id = $1`, clientID) })
	return &oidcE2E{oauthE2EEnv: env, keys: keys, clientID: clientID, redirectURI: redirectURI}
}

func newOIDCKeys(t *testing.T) *service.OIDCKeySet {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks, err := service.NewOIDCKeySet(k)
	require.NoError(t, err)
	return ks
}

func (env *oauthE2EEnv) getJSON(t *testing.T, path string) (status int, body map[string]interface{}, header http.Header) {
	t.Helper()
	resp, err := env.client.Get(env.server.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out), "%s must answer JSON", path)
	return resp.StatusCode, out, resp.Header
}

// authorizeRedirect runs GET /oauth/authorize and returns the status and the
// Location (empty when the server rendered instead of redirecting).
func (env *oauthE2EEnv) authorizeRedirect(t *testing.T, q url.Values) (int, *url.URL) {
	t.Helper()
	resp, err := env.client.Get(env.server.URL + "/oauth/authorize?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return resp.StatusCode, nil
	}
	u, err := url.Parse(loc)
	require.NoError(t, err)
	return resp.StatusCode, u
}

func oidcAuthorizeQuery(clientID, redirectURI, challenge, scope, nonce string) url.Values {
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {scope},
		"state":                 {"st-1"},
	}
	if nonce != "" {
		q.Set("nonce", nonce)
	}
	return q
}

// consent drives GET + POST /api/v1/oauth/consent for the identity flow (no
// workspace_id) and returns the redirect the frontend would follow.
func (e *oidcE2E) consent(t *testing.T, jwtToken string, q url.Values, allow bool) *url.URL {
	t.Helper()
	infoReq, _ := http.NewRequest(http.MethodGet, e.server.URL+"/api/v1/oauth/consent?"+q.Encode(), http.NoBody)
	infoReq.Header.Set("Authorization", "Bearer "+jwtToken)
	infoResp, err := e.client.Do(infoReq)
	require.NoError(t, err)
	defer infoResp.Body.Close()
	require.Equal(t, http.StatusOK, infoResp.StatusCode)
	var info map[string]interface{}
	require.NoError(t, json.NewDecoder(infoResp.Body).Decode(&info))
	assert.Equal(t, true, info["identity_only"])
	assert.Equal(t, "Keep", info["client_name"])
	assert.Equal(t, "openid", info["scope"])
	assert.Empty(t, info["workspaces"], "the identity flow offers no workspace")

	body := map[string]interface{}{"allow": allow}
	for k := range q {
		body[k] = q.Get(k)
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.server.URL+"/api/v1/oauth/consent", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	u, err := url.Parse(out["redirect_uri"])
	require.NoError(t, err)
	return u
}

func (env *oauthE2EEnv) postToken(t *testing.T, form url.Values) (status int, body map[string]interface{}) {
	t.Helper()
	resp, err := env.client.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// jwksKeyFunc verifies a token against the keys GET /oauth/jwks publishes,
// selected by the token's kid — what Keep does.
func jwksKeyFunc(t *testing.T, jwks map[string]interface{}) jwt.Keyfunc {
	t.Helper()
	keys := map[string]*rsa.PublicKey{}
	list, _ := jwks["keys"].([]interface{})
	for _, raw := range list {
		k := raw.(map[string]interface{})
		nb, err := base64.RawURLEncoding.DecodeString(k["n"].(string))
		require.NoError(t, err)
		eb, err := base64.RawURLEncoding.DecodeString(k["e"].(string))
		require.NoError(t, err)
		keys[k["kid"].(string)] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	return func(tok *jwt.Token) (interface{}, error) {
		kid, _ := tok.Header["kid"].(string)
		if k, ok := keys[kid]; ok {
			return k, nil
		}
		return nil, jwt.ErrTokenUnverifiable
	}
}

func TestOIDCEndToEnd_IdentityFlow(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	jwtToken, userID, _, _ := e.registerTestUser(t, "oidc-user")

	// Discovery.
	status, disc, _ := e.getJSON(t, "/.well-known/openid-configuration")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, e.issuer, disc["issuer"])
	assert.Equal(t, e.issuer+"/oauth/authorize", disc["authorization_endpoint"])
	assert.Equal(t, e.issuer+"/oauth/token", disc["token_endpoint"])
	assert.Equal(t, e.issuer+"/oauth/jwks", disc["jwks_uri"])
	assert.Equal(t, []interface{}{"RS256"}, disc["id_token_signing_alg_values_supported"])
	assert.Equal(t, []interface{}{"public"}, disc["subject_types_supported"])
	assert.Equal(t, []interface{}{"S256"}, disc["code_challenge_methods_supported"])
	assert.Equal(t, []interface{}{"iss", "sub", "aud", "exp", "iat", "nonce"}, disc["claims_supported"])

	// RFC 8414 metadata advertises openid and the JWKS while OIDC is on.
	status, md, _ := e.getJSON(t, "/.well-known/oauth-authorization-server")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, md["scopes_supported"], "openid")
	assert.Equal(t, e.issuer+"/oauth/jwks", md["jwks_uri"])
	assert.Equal(t, disc["issuer"], md["issuer"], "one issuer for both documents")

	status, jwks, hdr := e.getJSON(t, "/oauth/jwks")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, hdr.Get("Cache-Control"), "max-age")

	// authorize(openid, PKCE, nonce) -> consent screen.
	verifier, challenge := pkcePair()
	nonce := "n-" + uuid.New().String()
	q := oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", nonce)
	status, loc := e.authorizeRedirect(t, q)
	require.Equal(t, http.StatusFound, status)
	require.NotNil(t, loc)
	assert.Equal(t, e.issuer+oauthConsentPath, loc.Scheme+"://"+loc.Host+loc.Path)

	// consent -> code back on the registered redirect_uri.
	back := e.consent(t, jwtToken, q, true)
	assert.Equal(t, e.redirectURI, back.Scheme+"://"+back.Host+back.Path)
	assert.Equal(t, "st-1", back.Query().Get("state"))
	code := back.Query().Get("code")
	require.True(t, strings.HasPrefix(code, "mic_"), "identity code, got %q", code)

	// token -> id_token verified by the JWKS kid.
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {e.clientID},
		"redirect_uri": {e.redirectURI}, "code": {code}, "code_verifier": {verifier},
	}
	status, tok := e.postToken(t, form)
	require.Equal(t, http.StatusOK, status, "token exchange: %v", tok)
	assert.Equal(t, "Bearer", tok["token_type"])
	assert.Equal(t, "openid", tok["scope"])
	assert.NotContains(t, tok, "refresh_token", "the identity flow issues no refresh token")
	access, _ := tok["access_token"].(string)
	require.True(t, strings.HasPrefix(access, "moi_"), "got %q", access)

	idToken, _ := tok["id_token"].(string)
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(idToken, claims, jwksKeyFunc(t, jwks),
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(e.issuer), jwt.WithAudience(e.clientID))
	require.NoError(t, err, "id_token must verify against the published JWKS")
	assert.Equal(t, e.keys.ActiveKID(), parsed.Header["kid"])
	assert.Equal(t, userID.String(), claims["sub"], "sub is the user's Mesh UUID")
	assert.Equal(t, nonce, claims["nonce"])
	assert.NotContains(t, claims, "email")
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	assert.InDelta(t, 300, exp-iat, 0.5, "id_token lives 5 minutes")

	// Replay of the code -> invalid_grant.
	status, replay := e.postToken(t, form)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", replay["error"])

	// moi_ is no Mesh API credential.
	req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/api/v1/agents/me", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "moi_ token must not authenticate on /api/v1")
	// Positive control: the same route answers the user's own JWT.
	req2, _ := http.NewRequest(http.MethodGet, e.server.URL+"/api/v1/oauth/grants", http.NoBody)
	req2.Header.Set("Authorization", "Bearer "+jwtToken)
	resp2, err := e.client.Do(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	// The identity flow created no grant and no connector agent.
	var grants int
	require.NoError(t, e.db.Get(&grants, `SELECT COUNT(*) FROM oauth_grants WHERE user_id = $1`, userID))
	assert.Zero(t, grants)
}

func TestOIDCEndToEnd_WrongVerifierAndClientRefused(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	jwtToken, _, _, _ := e.registerTestUser(t, "oidc-pkce")
	verifier, challenge := pkcePair()
	q := oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", "")
	code := e.consent(t, jwtToken, q, true).Query().Get("code")

	other, _ := pkcePair()
	status, out := e.postToken(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {e.clientID},
		"redirect_uri": {e.redirectURI}, "code": {code}, "code_verifier": {other}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", out["error"], "wrong PKCE verifier")

	status, out = e.postToken(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {"someone-else"},
		"redirect_uri": {e.redirectURI}, "code": {code}, "code_verifier": {verifier}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", out["error"], "another client_id")

	// The failed attempts did not burn the code: the right one still works,
	// and without a nonce in authorize there is none in the token.
	status, out = e.postToken(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {e.clientID},
		"redirect_uri": {e.redirectURI}, "code": {code}, "code_verifier": {verifier}})
	require.Equal(t, http.StatusOK, status, "%v", out)
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(out["id_token"].(string), claims)
	require.NoError(t, err)
	assert.NotContains(t, claims, "nonce")
}

func TestOIDCEndToEnd_DenyReturnsAccessDenied(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	jwtToken, _, _, _ := e.registerTestUser(t, "oidc-deny")
	_, challenge := pkcePair()
	back := e.consent(t, jwtToken, oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", "n"), false)
	assert.Equal(t, "access_denied", back.Query().Get("error"))
	assert.Empty(t, back.Query().Get("code"))
}

func TestOIDCEndToEnd_ScopeAndRedirectRules(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	_, challenge := pkcePair()
	dcrRedirect := "http://localhost/oidc-dcr-cb"
	dcrClient := e.registerDCRClient(t, dcrRedirect)

	cases := []struct {
		name, clientID, redirect, scope string
	}{
		{"DCR client with openid", dcrClient, dcrRedirect, "openid"},
		{"DCR client with openid+mesh", dcrClient, dcrRedirect, "openid mesh"},
		{"static client with openid+mesh", e.clientID, e.redirectURI, "openid mesh"},
		{"static client with openid+profile", e.clientID, e.redirectURI, "openid profile"},
		{"static client with openid+email", e.clientID, e.redirectURI, "openid email"},
		{"static client with openid+offline_access", e.clientID, e.redirectURI, "openid offline_access"},
		{"static client with openid+unknown", e.clientID, e.redirectURI, "openid unknown"},
		{"static client with mesh only", e.clientID, e.redirectURI, "mesh offline_access"},
		{"static client with no scope", e.clientID, e.redirectURI, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, loc := e.authorizeRedirect(t, oidcAuthorizeQuery(c.clientID, c.redirect, challenge, c.scope, ""))
			require.Equal(t, http.StatusFound, status)
			require.NotNil(t, loc)
			assert.Equal(t, c.redirect, loc.Scheme+"://"+loc.Host+loc.Path, "the error goes back to the client")
			assert.Equal(t, "invalid_scope", loc.Query().Get("error"))
		})
	}

	// Positive control: the same DCR client without openid still goes to the
	// MCP consent screen, unchanged.
	status, loc := e.authorizeRedirect(t, oidcAuthorizeQuery(dcrClient, dcrRedirect, challenge, "mesh offline_access", ""))
	require.Equal(t, http.StatusFound, status)
	assert.Equal(t, oauthConsentPath, loc.Path)

	// A foreign redirect_uri is never redirected to: rendered error.
	for _, bad := range []string{
		"https://evil.example/console/login/oidc/callback",
		"http://localhost:4000/cb", // registered as :3000; static clients get no loopback port leeway
	} {
		badStatus, badLoc := e.authorizeRedirect(t, oidcAuthorizeQuery(e.clientID, bad, challenge, "openid", ""))
		assert.Equal(t, http.StatusBadRequest, badStatus, bad)
		assert.Nil(t, badLoc, "no redirect to an unregistered redirect_uri: %s", bad)
	}

	// The exact loopback URI is accepted.
	status, loc = e.authorizeRedirect(t, oidcAuthorizeQuery(e.clientID, "http://localhost:3000/cb", challenge, "openid", ""))
	require.Equal(t, http.StatusFound, status)
	assert.Equal(t, oauthConsentPath, loc.Path)

	// Over-long nonce.
	status, loc = e.authorizeRedirect(t, oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", strings.Repeat("n", 513)))
	require.Equal(t, http.StatusFound, status)
	assert.Equal(t, "invalid_request", loc.Query().Get("error"))
}

func TestOIDCEndToEnd_RemovedStaticClientRefused(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	// Restart with the client dropped from MESH_OIDC_CLIENTS: its row stays,
	// but it is no longer trusted.
	require.NoError(t, e.oauthSvc.(service.OIDCConfigurable).ConfigureOIDC(context.Background(),
		service.OIDCSettings{Issuer: e.issuer, Keys: e.keys}))
	_, challenge := pkcePair()
	status, loc := e.authorizeRedirect(t, oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", ""))
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Nil(t, loc)
}

func TestOIDCEndToEnd_StaticClientCannotTakeOverDCRClient(t *testing.T) {
	env := newOAuthE2EEnv(t)
	dcrClient := env.registerDCRClient(t, "http://localhost/takeover-cb")
	err := env.oauthSvc.(service.OIDCConfigurable).ConfigureOIDC(context.Background(), service.OIDCSettings{
		Issuer: env.issuer, Keys: newOIDCKeys(t),
		Clients: []service.OIDCStaticClient{{ClientID: dcrClient, ClientName: "x", RedirectURIs: []string{"https://evil.example/cb"}}},
	})
	require.Error(t, err)
	var regType string
	require.NoError(t, env.db.Get(&regType, `SELECT registration_type FROM oauth_clients WHERE client_id = $1`, dcrClient))
	assert.Equal(t, "dcr", regType, "the DCR row is untouched")
}

// OIDC off (no signing key): discovery and JWKS answer JSON 404, openid is
// invalid_scope, the RFC 8414 metadata is the pre-OIDC document.
func TestOIDCEndToEnd_DisabledWithoutKey(t *testing.T) {
	e := newOIDCE2E(t, nil) // static client configured, but no key
	_, challenge := pkcePair()

	for _, path := range []string{"/.well-known/openid-configuration", "/oauth/jwks"} {
		status, body, hdr := e.getJSON(t, path)
		assert.Equal(t, http.StatusNotFound, status, path)
		assert.Contains(t, hdr.Get("Content-Type"), "application/json", path)
		assert.NotEmpty(t, body, path)
	}

	status, md, _ := e.getJSON(t, "/.well-known/oauth-authorization-server")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []interface{}{"mesh", "offline_access"}, md["scopes_supported"])
	assert.NotContains(t, md, "jwks_uri")

	status, loc := e.authorizeRedirect(t, oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", "n"))
	require.Equal(t, http.StatusFound, status)
	assert.Equal(t, "invalid_scope", loc.Query().Get("error"))

	dcrRedirect := "http://localhost/oidc-off-cb"
	dcrClient := e.registerDCRClient(t, dcrRedirect)
	status, loc = e.authorizeRedirect(t, oidcAuthorizeQuery(dcrClient, dcrRedirect, challenge, "openid", ""))
	require.Equal(t, http.StatusFound, status)
	assert.Equal(t, "invalid_scope", loc.Query().Get("error"))

	// A forged identity code is refused like any unknown code.
	status, out := e.postToken(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {e.clientID},
		"redirect_uri": {e.redirectURI}, "code": {"mic_" + strings.Repeat("0", 64)}, "code_verifier": {strings.Repeat("v", 43)}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", out["error"])
}

// The MCP consent info keeps its pre-OIDC shape: no identity_only key at all.
func TestOIDCEndToEnd_MCPConsentInfoUnchanged(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	jwtToken, _, _, _ := e.registerTestUser(t, "oidc-mcp")
	redirect := "http://localhost/oidc-mcp-cb"
	dcrClient := e.registerDCRClient(t, redirect)
	_, challenge := pkcePair()
	q := oidcAuthorizeQuery(dcrClient, redirect, challenge, "mesh offline_access", "")
	req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/api/v1/oauth/consent?"+q.Encode(), http.NoBody)
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var info map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	assert.NotContains(t, info, "identity_only")
	assert.NotEmpty(t, info["workspaces"])
}

// Identity codes expire after 60 s.
func TestOIDCEndToEnd_IdentityCodeExpires(t *testing.T) {
	e := newOIDCE2E(t, newOIDCKeys(t))
	jwtToken, _, _, _ := e.registerTestUser(t, "oidc-expiry")
	verifier, challenge := pkcePair()
	code := e.consent(t, jwtToken, oidcAuthorizeQuery(e.clientID, e.redirectURI, challenge, "openid", ""), true).Query().Get("code")
	_, err := e.db.Exec(`UPDATE oauth_identity_codes SET expires_at = $1 WHERE client_id = $2`, time.Now().Add(-time.Second), e.clientID)
	require.NoError(t, err)
	status, out := e.postToken(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {e.clientID},
		"redirect_uri": {e.redirectURI}, "code": {code}, "code_verifier": {verifier}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", out["error"])
}
