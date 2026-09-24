package handler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
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
	return r.doXFF(method, target, ip, "", form)
}

// doXFF is do with a client-supplied X-Forwarded-For.
func (r *limitedRoutes) doXFF(method, target, ip, xff string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = ip + ":40000"
	if xff != "" {
		req.Header.Set(echo.HeaderXForwardedFor, xff)
	}
	if form != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	}
	rec := httptest.NewRecorder()
	r.e.ServeHTTP(rec, req)
	return rec
}

func TestOAuthRoutes_TokenAndRevokeAreRateLimitedPerIP(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: true, Register: 1000, Authorize: 1000, AuthorizeNewClient: 1000, Token: 3})

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
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: true, Register: 1000, Authorize: 2, AuthorizeNewClient: 1000, Token: 1000})
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
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: true, Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000})

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
		Enabled: true, IPTrusted: true, Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000,
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

// freshCIMDTarget is an authorize URL for a first-seen https client_id.
// 127.0.0.1:1 is refused by the SSRF guard before any connect — fast and
// offline — but it is still a first-seen https client_id.
func freshCIMDTarget() string {
	return "/oauth/authorize?response_type=code&client_id=" + url.QueryEscape("https://127.0.0.1:1/c-"+uuid.New().String())
}

// TestOAuthRoutes_UntrustedIP_SpoofedXFFCannotResetFirstSeenCIMDBudget is the
// #83cc58ef defect: with MESH_TRUSTED_PROXIES unset e.IPExtractor is nil, so
// c.RealIP() returns the leftmost client-supplied X-Forwarded-For — a fresh
// random value per request reset the per-IP "5/min for a new CIMD client_id"
// budget every time, leaving the outbound fetch + oauth_clients row per
// request unbounded. Untrusted, the budget must not be keyed on the header.
func TestOAuthRoutes_UntrustedIP_SpoofedXFFCannotResetFirstSeenCIMDBudget(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: false, Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000})

	for i := 1; i <= 2; i++ {
		rec := r.doXFF(http.MethodGet, freshCIMDTarget(), "203.0.113.60", "198.51.100."+strconv.Itoa(i), nil)
		assert.NotEqual(t, http.StatusTooManyRequests, rec.Code, "request %d is inside the budget", i)
	}
	// Different spoofed XFF each time, and even a different peer: still one bucket.
	rec := r.doXFF(http.MethodGet, freshCIMDTarget(), "203.0.113.61", "198.51.100.99", nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "request 3 must be refused however X-Forwarded-For varies")
}

// TestOAuthRoutes_UntrustedIP_DoesNotThrottleUnrelatedTraffic pins the other
// half of the trade-off: the shared bucket must be confined to the two
// resource-cost endpoints. Behind a proxy that overwrites XFF every client
// looks like one address, so a per-IP limiter on token/revoke/authorize
// would let anyone lock every user out.
func TestOAuthRoutes_UntrustedIP_DoesNotThrottleUnrelatedTraffic(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: false, Register: 1, Authorize: 1, AuthorizeNewClient: 1, Token: 1})

	for i := 1; i <= 10; i++ {
		for _, path := range []string{"/oauth/token", "/oauth/revoke"} {
			rec := r.do(http.MethodPost, path, "203.0.113.70", url.Values{"grant_type": {"refresh_token"}})
			require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "%s request %d", path, i)
		}
		rec := r.do(http.MethodGet, "/oauth/authorize?response_type=code&client_id=mcpc_"+uuid.New().String()[:8], "203.0.113.70", nil)
		require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "authorize (non-https client_id) request %d", i)
	}
}

// TestOAuthRoutes_UntrustedIP_DCRIsBounded: /oauth/register writes a row per
// call; untrusted, it is bounded by the same shared bucket, not left open.
func TestOAuthRoutes_UntrustedIP_DCRIsBounded(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: false, Register: 2, Authorize: 1000, AuthorizeNewClient: 1000, Token: 1000})
	post := func(xff string) int {
		return r.doXFF(http.MethodPost, "/oauth/register", "203.0.113.80", xff, url.Values{}).Code
	}
	assert.NotEqual(t, http.StatusTooManyRequests, post("198.51.100.1"))
	assert.NotEqual(t, http.StatusTooManyRequests, post("198.51.100.2"))
	assert.Equal(t, http.StatusTooManyRequests, post("198.51.100.3"))
}

// TestOAuthRoutes_TrustedProxy_UsesRealClientIPFromXFF: with
// MESH_TRUSTED_PROXIES set (e.IPExtractor built from it, as cmd/api does), the
// client IP a trusted hop relays in X-Forwarded-For is what is counted — one
// bucket per real client, not one shared bucket for everything behind the hop.
func TestOAuthRoutes_TrustedProxy_UsesRealClientIPFromXFF(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: true, Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000})
	_, hop, err := net.ParseCIDR("203.0.113.0/24")
	require.NoError(t, err)
	r.e.IPExtractor = echo.ExtractIPFromXFFHeader(echo.TrustIPRange(hop))

	const proxy = "203.0.113.5" // the trusted hop's own address (RemoteAddr)
	clientA, clientB := "198.51.100.10", "198.51.100.11"

	assert.NotEqual(t, http.StatusTooManyRequests, r.doXFF(http.MethodGet, freshCIMDTarget(), proxy, clientA, nil).Code)
	assert.NotEqual(t, http.StatusTooManyRequests, r.doXFF(http.MethodGet, freshCIMDTarget(), proxy, clientA, nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, r.doXFF(http.MethodGet, freshCIMDTarget(), proxy, clientA, nil).Code,
		"client A's third first-seen request is refused")
	assert.NotEqual(t, http.StatusTooManyRequests, r.doXFF(http.MethodGet, freshCIMDTarget(), proxy, clientB, nil).Code,
		"client B, behind the same trusted hop, has its own budget")
}

// TestOAuthRoutes_TrustedProxy_UntrustedPeerCannotSpoofXFF is the half of the
// trusted-proxy contract the test above cannot see: without an IPExtractor
// Echo's RealIP() takes the leftmost X-Forwarded-For from ANY peer, so that
// test passes with or without the extractor. Here the peer is OUTSIDE the
// trusted CIDR, so its forged X-Forwarded-For must be ignored and the budget
// keyed on its own address — it goes red if the extractor is not wired.
func TestOAuthRoutes_TrustedProxy_UntrustedPeerCannotSpoofXFF(t *testing.T) {
	r := newLimitedRoutes(t, OAuthRateLimits{IPTrusted: true, Register: 1000, Authorize: 1000, AuthorizeNewClient: 2, Token: 1000})
	_, hop, err := net.ParseCIDR("203.0.113.0/24")
	require.NoError(t, err)
	r.e.IPExtractor = echo.ExtractIPFromXFFHeader(echo.TrustIPRange(hop))

	const outsider = "198.51.100.200" // not the trusted hop
	for i := 1; i <= 2; i++ {
		rec := r.doXFF(http.MethodGet, freshCIMDTarget(), outsider, "192.0.2."+strconv.Itoa(i), nil)
		assert.NotEqual(t, http.StatusTooManyRequests, rec.Code, "request %d is inside the budget", i)
	}
	rec := r.doXFF(http.MethodGet, freshCIMDTarget(), outsider, "192.0.2.99", nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "an untrusted peer's forged X-Forwarded-For must not buy a fresh budget")
}
