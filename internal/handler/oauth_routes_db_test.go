package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// The public OAuth endpoints' limiters, exercised through
// RegisterOAuthPublicRoutes — the one function cmd/api also mounts them with.
// Budgets are set deliberately tiny so "request N+1 is refused" is a
// three-request test.

type limitedRoutes struct {
	env *oauthE2EEnv
	e   *echo.Echo
}

func newLimitedRoutes(t *testing.T, limits OAuthRateLimits) *limitedRoutes {
	t.Helper()
	env := newOAuthE2EEnv(t) // real DB + services; its own server is not used for these requests
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	limits.Enabled = true
	RegisterOAuthPublicRoutes(e, env.oauthHandler, postgres.NewOAuthRepo(env.db), limits)
	return &limitedRoutes{env: env, e: e}
}

func (r *limitedRoutes) do(method, target, ip string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = ip + ":40000"
	if form != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	}
	rec := httptest.NewRecorder()
	r.e.ServeHTTP(rec, req)
	return rec
}

func TestOAuthRoutes_TokenAndRevokeAreRateLimitedPerIP(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{Register: 1000, Authorize: 1000, AuthorizeNewClient: 1000, Token: 3})

	for _, path := range []string{"/oauth/token", "/oauth/revoke"} {
		ip := "203.0.113." + map[string]string{"/oauth/token": "10", "/oauth/revoke": "11"}[path]
		for i := 1; i <= 3; i++ {
			rec := r.do(http.MethodPost, path, ip, url.Values{"grant_type": {"refresh_token"}})
			assert.NotEqual(t, http.StatusTooManyRequests, rec.Code, "%s request %d is inside the budget", path, i)
		}
		rec := r.do(http.MethodPost, path, ip, url.Values{"grant_type": {"refresh_token"}})
		assert.Equal(t, http.StatusTooManyRequests, rec.Code, "%s request 4 must be refused", path)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))

		other := r.do(http.MethodPost, path, "203.0.113.99", url.Values{"grant_type": {"refresh_token"}})
		assert.NotEqual(t, http.StatusTooManyRequests, other.Code, "another IP has its own budget")
	}
}

func TestOAuthRoutes_AuthorizeGeneralBudget(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{Register: 1000, Authorize: 2, AuthorizeNewClient: 1000, Token: 1000})
	target := "/oauth/authorize?client_id=mcpc_nope&response_type=code"
	assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, target, "203.0.113.20", nil).Code)
	assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, target, "203.0.113.20", nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, r.do(http.MethodGet, target, "203.0.113.20", nil).Code, "authorize request 3 must be refused")
}

// TestOAuthRoutes_AuthorizeNewCIMDClientBudget: the loop
// authorize?client_id=https://attacker.example/?n=<i> makes the server fetch
// a URL of the attacker's choosing and write an oauth_clients row per
// request. Only NEW https client_ids may be throttled that hard.
func TestOAuthRoutes_AuthorizeNewCIMDClientBudget(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000})

	fresh := func() string {
		// 127.0.0.1:1 is refused by the SSRF guard before any connect — fast
		// and offline — but it is still a first-seen https client_id.
		return "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape("https://127.0.0.1:1/c-"+uuid.New().String())
	}

	t.Run("new https client_ids: the third from one IP is refused", func(t *testing.T) {
		ip := "203.0.113.30"
		assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, fresh(), ip, nil).Code)
		assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, fresh(), ip, nil).Code)
		assert.Equal(t, http.StatusTooManyRequests, r.do(http.MethodGet, fresh(), ip, nil).Code)
		assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, fresh(), "203.0.113.31", nil).Code, "another IP is unaffected")
	})

	t.Run("an already-known client is not counted", func(t *testing.T) {
		known := "https://known-" + uuid.New().String()[:8] + ".example.test/client.json"
		_, err := r.env.db.Exec(
			`INSERT INTO oauth_clients (client_id, registration_type, client_name, redirect_uris, token_endpoint_auth_method, grant_types, metadata, metadata_fetched_at)
			 VALUES ($1, 'cimd', 'Known', ARRAY['https://known.example.test/cb'], 'none', ARRAY['authorization_code'], '{}', NOW())`, known)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = r.env.db.Exec(`DELETE FROM oauth_clients WHERE client_id=$1`, known) })

		for i := 0; i < 10; i++ {
			rec := r.do(http.MethodGet, "/oauth/authorize?response_type=code&client_id="+url.QueryEscape(known), "203.0.113.40", nil)
			require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "request %d for a known client", i+1)
		}
	})

	t.Run("a non-https client_id never touches that budget", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			rec := r.do(http.MethodGet, "/oauth/authorize?response_type=code&client_id=mcpc_"+uuid.New().String()[:8], "203.0.113.50", nil)
			require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "request %d", i+1)
		}
	})
}

// TestOAuthRoutes_ClientLookupFailureCountsAsNewClient: when the known-client
// lookup itself fails, an https client_id must fall into the TIGHT bucket
// (fail toward the stricter limit), not slip past it.
func TestOAuthRoutes_ClientLookupFailureCountsAsNewClient(t *testing.T) {
	env := newOAuthE2EEnv(t)
	dead := closedOAuthDB(t)
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	RegisterOAuthPublicRoutes(e, env.oauthHandler, postgres.NewOAuthRepo(dead), OAuthRateLimits{
		Enabled: true, Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000,
	})
	r := &limitedRoutes{env: env, e: e}

	target := func() string {
		return "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape("https://127.0.0.1:1/c-"+uuid.New().String())
	}
	assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, target(), "203.0.113.60", nil).Code)
	assert.NotEqual(t, http.StatusTooManyRequests, r.do(http.MethodGet, target(), "203.0.113.60", nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, r.do(http.MethodGet, target(), "203.0.113.60", nil).Code)
}

// closedOAuthDB is a real pool that has been closed: every query fails at the
// driver, which is how a database outage presents.
func closedOAuthDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	dead, err := sqlx.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, dead.Close())
	return dead
}
