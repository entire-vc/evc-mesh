package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OAuth hygiene (#23579e6b): the request-body cap on POST /oauth/register and
// the /oauth/token grant_type check, both driven through
// RegisterOAuthPublicRoutes — the routes production mounts.

func (r *limitedRoutes) postJSON(target, ip string, body io.Reader, contentLength int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, body)
	req.RemoteAddr = ip + ":40000"
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	// A negative length means "unknown" — a chunked upload.
	req.ContentLength = contentLength
	rec := httptest.NewRecorder()
	r.e.ServeHTTP(rec, req)
	return rec
}

func dcrBody(name string, padding int) string {
	doc := map[string]interface{}{
		"client_name":   name,
		"redirect_uris": []string{"https://client.example.test/cb"},
		"grant_types":   []string{"authorization_code", "refresh_token"},
	}
	if padding > 0 {
		doc["software_statement"] = strings.Repeat("A", padding) // unknown field: decoded, then ignored
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func TestOAuthRoutes_RegisterBodyIsCapped(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{Register: 1000, Authorize: 1000, AuthorizeNewClient: 1000, Token: 1000})
	const ip = "203.0.113.50"

	t.Run("a normal registration is accepted", func(t *testing.T) {
		body := dcrBody("Body Cap Control", 0)
		rec := r.postJSON("/oauth/register", ip, strings.NewReader(body), int64(len(body)))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		clientID, _ := out["client_id"].(string)
		require.NotEmpty(t, clientID)
		t.Cleanup(func() { _, _ = r.env.db.Exec(`DELETE FROM oauth_clients WHERE client_id=$1`, clientID) })
	})

	t.Run("declared length over the cap is refused without reading the body", func(t *testing.T) {
		body := dcrBody("Too Big", oauthRegisterMaxBodyBytes)
		rec := r.postJSON("/oauth/register", ip, strings.NewReader(body), int64(len(body)))
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "invalid_client_metadata")
	})

	t.Run("an undeclared (chunked) oversize body is cut off by the reader cap", func(t *testing.T) {
		body := dcrBody("Too Big Chunked", oauthRegisterMaxBodyBytes)
		rec := r.postJSON("/oauth/register", ip, strings.NewReader(body), -1)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	})

	t.Run("a Content-Length that lies low does not get past the reader cap", func(t *testing.T) {
		body := dcrBody("Liar", oauthRegisterMaxBodyBytes)
		rec := r.postJSON("/oauth/register", ip, strings.NewReader(body), 100)
		assert.NotEqual(t, http.StatusCreated, rec.Code, "an oversize document must never register")
	})

	t.Run("malformed JSON is still a plain 400", func(t *testing.T) {
		rec := r.postJSON("/oauth/register", ip, strings.NewReader("{not json"), 9)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestOAuthRoutes_TokenErrorsStayNeutralForAnAuthCodeOnlyClient(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{Register: 1000, Authorize: 1000, AuthorizeNewClient: 1000, Token: 1000})

	body := `{"client_name":"Auth Code Only","redirect_uris":["https://client.example.test/cb"],"grant_types":["authorization_code"]}`
	rec := r.postJSON("/oauth/register", "203.0.113.60", strings.NewReader(body), int64(len(body)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var reg map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reg))
	clientID := reg["client_id"].(string)
	t.Cleanup(func() { _, _ = r.env.db.Exec(`DELETE FROM oauth_clients WHERE client_id=$1`, clientID) })

	// Over the wire an unknown refresh token is a clean 400 invalid_grant, never
	// a 5xx. The unauthorized_client outcome needs a real token and is asserted
	// in the service tests (TestOAuthSvc_TokenEndpointEnforcesClientGrantTypes).
	rec = r.do(http.MethodPost, "/oauth/token", "203.0.113.61",
		url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {"mor_bogus"}})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_grant")

	// Nonexistent code: the description is the neutral one, not a PKCE message.
	rec = r.do(http.MethodPost, "/oauth/token", "203.0.113.62", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {"https://client.example.test/cb"},
		"code": {"nonexistent"}, "code_verifier": {strings.Repeat("a", 43)},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid or expired authorization code")
	assert.NotContains(t, rec.Body.String(), "code_verifier")
}
