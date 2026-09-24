package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/oautherror"
)

// These tests drive oauthService against the REAL Postgres repositories and
// the real agentService. The only substitution is the outbound CIMD fetch
// transport (via SetHTTPClientForTesting), pointed at a loopback
// httptest.Server — the production transport's SSRF guard correctly refuses
// loopback, which is itself asserted in oauth_service_validation_test.go.
//
// No build tag, same convention as the other *_db_test.go files in this
// package: CI runs an untagged `go test` against a migrated DATABASE_URL;
// locally the tests skip when no database is reachable. Every fixture uses
// random slugs/emails/client names so parallel packages sharing one database
// never collide.

func oauthSvcTestDB(t *testing.T) *sqlx.DB {
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

type oauthSvcEnv struct {
	db    *sqlx.DB
	svc   *oauthService
	clock time.Duration // offset added to real time by timeNow
	mu    sync.Mutex
}

// newOAuthSvcEnv wires the service exactly as cmd/api/main.go does. timeNow
// is a package-level seam that other (mock-based) tests in this package
// freeze and never restore, so every test here pins it back to real time
// (plus an adjustable offset) and restores whatever it found afterwards.
func newOAuthSvcEnv(t *testing.T) *oauthSvcEnv {
	t.Helper()
	db := oauthSvcTestDB(t)

	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	agentSvc := NewAgentService(postgres.NewAgentRepo(db), postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	if c, ok := agentSvc.(AgentServiceConfigurable); ok {
		c.SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	}
	svc := NewOAuthService(postgres.NewOAuthRepo(db), agentSvc, userRepo, workspaceRepo, postgres.NewWorkspaceMemberRepo(db), postgres.NewAgentWorkspaceGrantRepo(db))

	env := &oauthSvcEnv{db: db, svc: svc.(*oauthService)}
	prev := timeNow
	timeNow = func() time.Time {
		env.mu.Lock()
		defer env.mu.Unlock()
		return time.Now().Add(env.clock)
	}
	t.Cleanup(func() { timeNow = prev })
	return env
}

func (env *oauthSvcEnv) advance(d time.Duration) {
	env.mu.Lock()
	env.clock += d
	env.mu.Unlock()
}

func (env *oauthSvcEnv) createUser(t *testing.T, label string) (id uuid.UUID, handle string) {
	t.Helper()
	id = uuid.New()
	handle = "oa-" + label + "-" + id.String()[:8]
	_, err := env.db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		 VALUES ($1, $2, 'not-a-real-hash', $3, $4, true)`,
		id, handle+"@oauth-svc.example.test", label, handle,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = env.db.Exec(`DELETE FROM users WHERE id = $1`, id) })
	return id, handle
}

func (env *oauthSvcEnv) createWorkspace(t *testing.T, owner uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, $2, $3, $4)`,
		id, "OAuth Svc Test "+id.String()[:8], "oauth-svc-"+id.String()[:8], owner,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = env.db.Exec(`DELETE FROM oauth_grants WHERE workspace_id = $1`, id)
		_, _ = env.db.Exec(`DELETE FROM agents WHERE workspace_id = $1`, id)
		_, _ = env.db.Exec(`DELETE FROM workspaces WHERE id = $1`, id)
	})
	return id
}

func (env *oauthSvcEnv) addMember(t *testing.T, ws, user uuid.UUID, role string) {
	t.Helper()
	_, err := env.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), ws, user, role,
	)
	require.NoError(t, err)
}

func (env *oauthSvcEnv) cleanupClient(t *testing.T, clientID string) {
	t.Helper()
	t.Cleanup(func() { _, _ = env.db.Exec(`DELETE FROM oauth_clients WHERE client_id = $1`, clientID) })
}

func (env *oauthSvcEnv) registerDCR(t *testing.T, name string, redirects ...string) *domain.OAuthClient {
	t.Helper()
	c, oerr := env.svc.RegisterClientDCR(context.Background(), DCRRegisterInput{RedirectURIs: redirects, ClientName: name})
	require.Nil(t, oerr)
	env.cleanupClient(t, c.ClientID)
	return c
}

func svcPKCE() (verifier, challenge string) {
	verifier = uuid.New().String() + uuid.New().String()
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func authParams(clientID, redirect, challenge, state string) AuthorizeParams {
	return AuthorizeParams{
		ClientID: clientID, RedirectURI: redirect, ResponseType: "code",
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: state,
	}
}

// consent runs Decide(allow) and returns the raw authorization code.
func (env *oauthSvcEnv) consent(t *testing.T, clientID, redirect, challenge string, user, ws uuid.UUID) string {
	t.Helper()
	dest, err := env.svc.Decide(context.Background(), ConsentDecisionInput{
		AuthorizeParams: authParams(clientID, redirect, challenge, "st-"+uuid.New().String()[:6]),
		UserID:          user, WorkspaceID: ws, Allow: true,
	})
	require.NoError(t, err)
	u, perr := url.Parse(dest)
	require.NoError(t, perr)
	code := u.Query().Get("code")
	require.NotEmpty(t, code, "Decide(allow) must redirect with a code: %s", dest)
	return code
}

// flow: fresh user+workspace owner, DCR client, consent, exchange.
type oauthFlow struct {
	user     uuid.UUID
	username string
	ws       uuid.UUID
	client   *domain.OAuthClient
	redirect string
	tokens   *TokenResponse
}

func (env *oauthSvcEnv) fullFlow(t *testing.T) *oauthFlow {
	t.Helper()
	user, username := env.createUser(t, "owner")
	ws := env.createWorkspace(t, user)
	redirect := "http://127.0.0.1/cb"
	client := env.registerDCR(t, "Flow Client "+uuid.New().String()[:8], redirect)
	verifier, challenge := svcPKCE()
	code := env.consent(t, client.ClientID, redirect, challenge, user, ws)
	tok, oerr := env.svc.ExchangeCode(context.Background(), client.ClientID, redirect, code, verifier)
	require.Nil(t, oerr)
	return &oauthFlow{user: user, username: username, ws: ws, client: client, redirect: redirect, tokens: tok}
}

func (env *oauthSvcEnv) tokenRow(t *testing.T, raw string) domain.OAuthToken {
	t.Helper()
	var row domain.OAuthToken
	require.NoError(t, env.db.Get(&row,
		`SELECT id, grant_id, token_type, token_hash, family_id, parent_token_id, expires_at, revoked_at, created_at
		 FROM oauth_tokens WHERE token_hash = $1`, sha256Hex(raw)))
	return row
}

func requireOAuthCode(t *testing.T, oerr *oautherror.Error, code string) {
	t.Helper()
	require.NotNil(t, oerr, "expected oauth error %q, got success", code)
	assert.Equal(t, code, oerr.Code, "description: %s", oerr.Description)
}

func requireAPIStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "want *apierror.Error, got %T: %v", err, err)
	assert.Equal(t, status, ae.Code, ae.Error())
}

// cimdServer is a loopback TLS server serving a CIMD document built by fn
// (called with the document's own URL). The service's fetch client is
// swapped for the server's own client — the one allowed substitution.
func (env *oauthSvcEnv) cimdServer(t *testing.T, fn func(w http.ResponseWriter, selfURL string)) (clientID string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	var self string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fn(w, self)
	}))
	t.Cleanup(srv.Close)
	self = srv.URL + "/client-" + uuid.New().String()[:8] + ".json"
	env.svc.SetHTTPClientForTesting(srv.Client())
	env.cleanupClient(t, self)
	return self, hits
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// DCR registration
// ---------------------------------------------------------------------------

func TestOAuthSvc_RegisterClientDCR(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()

	t.Run("valid registration is persisted as a public dcr client with default grants", func(t *testing.T) {
		c := env.registerDCR(t, "DCR OK", "https://app.example.com/cb", "http://localhost/cb")
		assert.True(t, strings.HasPrefix(c.ClientID, dcrClientIDPrefix))
		assert.Equal(t, "none", c.TokenEndpointAuthMethod)
		assert.ElementsMatch(t, oauthDefaultGrantTypes, c.GrantTypes)

		var regType, method string
		require.NoError(t, env.db.QueryRow(`SELECT registration_type, token_endpoint_auth_method FROM oauth_clients WHERE client_id=$1`, c.ClientID).Scan(&regType, &method))
		assert.Equal(t, "dcr", regType)
		assert.Equal(t, "none", method)

		got, oerr := env.svc.ResolveClient(ctx, c.ClientID)
		require.Nil(t, oerr)
		assert.Equal(t, c.ID, got.ID)
	})

	t.Run("explicit grant_types are kept", func(t *testing.T) {
		c, oerr := env.svc.RegisterClientDCR(ctx, DCRRegisterInput{
			RedirectURIs: []string{"https://a.example.com/cb"}, GrantTypes: []string{"authorization_code"}, TokenEndpointAuthMethod: "none",
		})
		require.Nil(t, oerr)
		env.cleanupClient(t, c.ClientID)
		assert.ElementsMatch(t, []string{"authorization_code"}, c.GrantTypes)
	})

	rejects := map[string]DCRRegisterInput{
		"no redirect_uris":             {ClientName: "x"},
		"http off-loopback":            {RedirectURIs: []string{"http://evil.example.com/cb"}},
		"javascript scheme":            {RedirectURIs: []string{"javascript:alert(1)"}},
		"custom scheme":                {RedirectURIs: []string{"myapp://cb"}},
		"fragment":                     {RedirectURIs: []string{"https://app.example.com/cb#x"}},
		"one bad among good":           {RedirectURIs: []string{"https://ok.example.com/cb", "data:text/html,x"}},
		"confidential client":          {RedirectURIs: []string{"https://ok.example.com/cb"}, TokenEndpointAuthMethod: "client_secret_basic"},
		"client_name over 200 runes":   {ClientName: uniqueName(maxClientNameRunes + 1), RedirectURIs: []string{"https://ok.example.com/cb"}},
		"more than 10 redirect_uris":   {RedirectURIs: nRedirects(maxRedirectURIs + 1)},
		"redirect_uri over 2048 bytes": {RedirectURIs: []string{longRedirect(maxRedirectURILength + 1)}},
	}
	for name, in := range rejects {
		t.Run("rejects "+name, func(t *testing.T) {
			var before int
			require.NoError(t, env.db.Get(&before, `SELECT count(*) FROM oauth_clients`))
			c, oerr := env.svc.RegisterClientDCR(ctx, in)
			assert.Nil(t, c)
			requireOAuthCode(t, oerr, "invalid_client_metadata")
			// Nothing written. (Count can only grow from concurrent packages;
			// we assert our specific client never appeared by name below.)
			if in.ClientName != "" {
				var n int
				require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_clients WHERE client_name=$1`, in.ClientName))
				assert.Zero(t, n)
			}
		})
	}

	t.Run("exactly-at-limit metadata is accepted", func(t *testing.T) {
		name := uniqueName(maxClientNameRunes) // exactly 200 runes
		uris := nRedirects(maxRedirectURIs)
		uris[0] = longRedirect(maxRedirectURILength)
		c, oerr := env.svc.RegisterClientDCR(ctx, DCRRegisterInput{ClientName: name, RedirectURIs: uris})
		require.Nil(t, oerr)
		env.cleanupClient(t, c.ClientID)
		assert.Len(t, c.RedirectURIs, maxRedirectURIs)
		assert.Len(t, c.RedirectURIs[0], maxRedirectURILength)
	})
}

// uniqueName returns a client_name of exactly n runes (multi-byte, so rune
// vs byte counting matters) that no other test run — in this package or a
// parallel one sharing the database — will produce.
func uniqueName(n int) string {
	prefix := uuid.New().String()[:8]
	return prefix + strings.Repeat("я", n-len(prefix))
}

func nRedirects(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("https://app%d.example.com/cb", i)
	}
	return out
}

// longRedirect returns a valid https redirect_uri of exactly n bytes.
func longRedirect(n int) string {
	base := "https://app.example.com/"
	return base + strings.Repeat("p", n-len(base))
}

// ---------------------------------------------------------------------------
// CIMD resolution + fetch
// ---------------------------------------------------------------------------

func TestOAuthSvc_ResolveClient_UnknownAndEmpty(t *testing.T) {
	env := newOAuthSvcEnv(t)
	_, oerr := env.svc.ResolveClient(context.Background(), "")
	requireOAuthCode(t, oerr, "invalid_request")
	_, oerr = env.svc.ResolveClient(context.Background(), "mcpc_doesnotexist"+uuid.New().String())
	requireOAuthCode(t, oerr, "invalid_client")
}

func TestOAuthSvc_ResolveClient_CIMDFirstSightCachesAndRefreshes(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	var name atomic.Value
	name.Store("CIMD v1")
	var fail atomic.Bool
	clientID, hits := env.cimdServer(t, func(w http.ResponseWriter, self string) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]interface{}{
			"client_id": self, "client_name": name.Load(), "redirect_uris": []string{"http://127.0.0.1/cb"},
		})
	})

	c, oerr := env.svc.ResolveClient(ctx, clientID)
	require.Nil(t, oerr)
	assert.Equal(t, "cimd", c.RegistrationType)
	assert.Equal(t, "CIMD v1", c.ClientName)
	assert.ElementsMatch(t, oauthDefaultGrantTypes, c.GrantTypes)
	assert.Equal(t, int32(1), hits.Load())

	// Within the cache TTL: served from the DB, no refetch.
	name.Store("CIMD v2")
	c, oerr = env.svc.ResolveClient(ctx, clientID)
	require.Nil(t, oerr)
	assert.Equal(t, "CIMD v1", c.ClientName)
	assert.Equal(t, int32(1), hits.Load(), "fresh cache must not refetch")

	// Past the TTL: refetched and the new metadata is persisted.
	env.advance(oauthClientCacheTTL + time.Minute)
	c, oerr = env.svc.ResolveClient(ctx, clientID)
	require.Nil(t, oerr)
	assert.Equal(t, "CIMD v2", c.ClientName)
	assert.Equal(t, int32(2), hits.Load())
	var stored string
	require.NoError(t, env.db.Get(&stored, `SELECT client_name FROM oauth_clients WHERE client_id=$1`, clientID))
	assert.Equal(t, "CIMD v2", stored)

	// Stale AND the document can no longer be fetched: fail closed rather
	// than trusting the cached copy.
	env.advance(oauthClientCacheTTL + time.Minute)
	fail.Store(true)
	c, oerr = env.svc.ResolveClient(ctx, clientID)
	assert.Nil(t, c)
	requireOAuthCode(t, oerr, "invalid_client")
}

func TestOAuthSvc_ResolveClient_CIMDRefreshKeepsGrantTypesWhenDocOmitsThem(t *testing.T) {
	env := newOAuthSvcEnv(t)
	var withGrants atomic.Bool
	withGrants.Store(true)
	clientID, _ := env.cimdServer(t, func(w http.ResponseWriter, self string) {
		doc := map[string]interface{}{"client_id": self, "client_name": "G", "redirect_uris": []string{"https://g.example.com/cb"}}
		if withGrants.Load() {
			doc["grant_types"] = []string{"authorization_code"}
		}
		writeJSON(w, doc)
	})
	c, oerr := env.svc.ResolveClient(context.Background(), clientID)
	require.Nil(t, oerr)
	assert.ElementsMatch(t, []string{"authorization_code"}, c.GrantTypes)

	withGrants.Store(false)
	env.advance(2 * oauthClientCacheTTL)
	c, oerr = env.svc.ResolveClient(context.Background(), clientID)
	require.Nil(t, oerr)
	assert.ElementsMatch(t, []string{"authorization_code"}, c.GrantTypes, "a refresh without grant_types must not wipe the stored ones")
}

// TestOAuthSvc_ResolveClient_CIMDConcurrentFirstSight: while our fetch is in
// flight another writer registers the same client_id (the unique index then
// rejects our insert). ResolveClient must serve the winner's row, not 500.
func TestOAuthSvc_ResolveClient_CIMDConcurrentFirstSight(t *testing.T) {
	env := newOAuthSvcEnv(t)
	clientID, _ := env.cimdServer(t, func(w http.ResponseWriter, self string) {
		_, err := env.db.Exec(
			`INSERT INTO oauth_clients (client_id, registration_type, client_name, redirect_uris, metadata_fetched_at)
			 VALUES ($1, 'cimd', 'winner', $2, NOW()) ON CONFLICT DO NOTHING`,
			self, pq.StringArray{"https://w.example.com/cb"})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"client_id": self, "client_name": "loser", "redirect_uris": []string{"https://w.example.com/cb"}})
	})
	c, oerr := env.svc.ResolveClient(context.Background(), clientID)
	require.Nil(t, oerr)
	assert.Equal(t, "winner", c.ClientName, "must serve the row the concurrent writer created")
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_clients WHERE client_id=$1`, clientID))
	assert.Equal(t, 1, n)
}

func TestOAuthSvc_ResolveClient_CIMDBadDocuments(t *testing.T) {
	cases := []struct {
		name string
		code string
		fn   func(w http.ResponseWriter, self string)
	}{
		{"client_id mismatch", "invalid_client_metadata", func(w http.ResponseWriter, _ string) {
			writeJSON(w, map[string]interface{}{"client_id": "https://someone-else.example.com/c.json", "redirect_uris": []string{"https://x.example.com/cb"}})
		}},
		{"client_id missing", "invalid_client_metadata", func(w http.ResponseWriter, _ string) {
			writeJSON(w, map[string]interface{}{"redirect_uris": []string{"https://x.example.com/cb"}})
		}},
		{"no redirect_uris", "invalid_client_metadata", func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self})
		}},
		{"invalid redirect_uri", "invalid_client_metadata", func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self, "redirect_uris": []string{"http://evil.example.com/cb"}})
		}},
		{"client_name over 200 runes", "invalid_client_metadata", func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self, "client_name": uniqueName(maxClientNameRunes + 1), "redirect_uris": []string{"https://x.example.com/cb"}})
		}},
		{"more than 10 redirect_uris", "invalid_client_metadata", func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self, "redirect_uris": nRedirects(maxRedirectURIs + 1)})
		}},
		{"redirect_uri over 2048 bytes", "invalid_client_metadata", func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self, "redirect_uris": []string{longRedirect(maxRedirectURILength + 1)}})
		}},
		{"not JSON", "invalid_client_metadata", func(w http.ResponseWriter, _ string) {
			_, _ = w.Write([]byte("<html>nope</html>"))
		}},
		{"over size limit", "invalid_client_metadata", func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self, "redirect_uris": []string{"https://x.example.com/cb"}, "pad": strings.Repeat("x", oauthCIMDMaxBodyBytes)})
		}},
		{"non-200", "invalid_client", func(w http.ResponseWriter, _ string) {
			w.WriteHeader(http.StatusNotFound)
		}},
		{"redirect is not followed", "invalid_client", func(w http.ResponseWriter, _ string) {
			w.Header().Set("Location", "https://127.0.0.1/elsewhere")
			w.WriteHeader(http.StatusFound)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newOAuthSvcEnv(t)
			clientID, hits := env.cimdServer(t, tc.fn)
			c, oerr := env.svc.ResolveClient(context.Background(), clientID)
			assert.Nil(t, c)
			requireOAuthCode(t, oerr, tc.code)
			assert.Equal(t, int32(1), hits.Load())
			var n int
			require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_clients WHERE client_id=$1`, clientID))
			assert.Zero(t, n, "a rejected document must not be cached as a client")
		})
	}
}

func TestOAuthSvc_ResolveClient_CIMDAtLimitAccepted(t *testing.T) {
	env := newOAuthSvcEnv(t)
	uris := nRedirects(maxRedirectURIs)
	uris[3] = longRedirect(maxRedirectURILength)
	clientID, _ := env.cimdServer(t, func(w http.ResponseWriter, self string) {
		writeJSON(w, map[string]interface{}{"client_id": self, "client_name": uniqueName(maxClientNameRunes), "redirect_uris": uris})
	})
	c, oerr := env.svc.ResolveClient(context.Background(), clientID)
	require.Nil(t, oerr)
	assert.Len(t, c.RedirectURIs, maxRedirectURIs)
	assert.Equal(t, maxClientNameRunes, len([]rune(c.ClientName)))
}

func TestOAuthSvc_ResolveClient_CIMDUnreachableHost(t *testing.T) {
	env := newOAuthSvcEnv(t)
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	clientID := srv.URL + "/gone.json"
	env.svc.SetHTTPClientForTesting(srv.Client())
	srv.Close() // connection refused from here on
	_, oerr := env.svc.ResolveClient(context.Background(), clientID)
	requireOAuthCode(t, oerr, "invalid_client")
	assert.Contains(t, oerr.Description, "failed to fetch")
}

// ---------------------------------------------------------------------------
// Authorize validation + consent info
// ---------------------------------------------------------------------------

func TestOAuthSvc_ValidateAuthorize(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	c := env.registerDCR(t, "VA", "http://localhost/cb", "https://va.example.com/cb")
	_, challenge := svcPKCE()

	v := env.svc.ValidateAuthorize(ctx, authParams(c.ClientID, "http://localhost:4567/cb", challenge, "s"))
	require.Nil(t, v.ClientErr)
	require.Nil(t, v.RequestErr)
	assert.Equal(t, "http://localhost:4567/cb", v.RedirectURI, "loopback port-insensitive match")

	// Client-level errors: never redirected to.
	for name, p := range map[string]AuthorizeParams{
		"missing client_id":      authParams("", "http://localhost/cb", challenge, "s"),
		"unknown client":         authParams("mcpc_nope"+uuid.New().String(), "http://localhost/cb", challenge, "s"),
		"missing redirect_uri":   authParams(c.ClientID, "", challenge, "s"),
		"unregistered redirect":  authParams(c.ClientID, "https://attacker.example.com/cb", challenge, "s"),
		"https port not ignored": authParams(c.ClientID, "https://va.example.com:8443/cb", challenge, "s"),
		"loopback path differs":  authParams(c.ClientID, "http://localhost/other", challenge, "s"),
	} {
		got := env.svc.ValidateAuthorize(ctx, p)
		assert.NotNil(t, got.ClientErr, name)
		assert.Nil(t, got.Client, name)
		assert.Empty(t, got.RedirectURI, name+": an untrusted redirect must never be echoed back")
	}

	// Request-level errors: redirect_uri is trusted, so they are redirectable.
	p := authParams(c.ClientID, "https://va.example.com/cb", challenge, "s")
	p.ResponseType = "token"
	v = env.svc.ValidateAuthorize(ctx, p)
	require.Nil(t, v.ClientErr)
	requireOAuthCode(t, v.RequestErr, "unsupported_response_type")
	assert.Equal(t, "https://va.example.com/cb", v.RedirectURI)

	for name, mut := range map[string]func(*AuthorizeParams){
		"plain method":      func(p *AuthorizeParams) { p.CodeChallengeMethod = "plain" },
		"missing method":    func(p *AuthorizeParams) { p.CodeChallengeMethod = "" },
		"missing challenge": func(p *AuthorizeParams) { p.CodeChallenge = "" },
		"short challenge":   func(p *AuthorizeParams) { p.CodeChallenge = challenge[:42] },
		"bad alphabet":      func(p *AuthorizeParams) { p.CodeChallenge = strings.Repeat("+", 43) },
	} {
		p := authParams(c.ClientID, "https://va.example.com/cb", challenge, "s")
		mut(&p)
		v := env.svc.ValidateAuthorize(ctx, p)
		require.Nil(t, v.ClientErr, name)
		requireOAuthCode(t, v.RequestErr, "invalid_request")
	}
}

func TestOAuthSvc_ConsentInfo(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	user, _ := env.createUser(t, "ci")
	ws := env.createWorkspace(t, user)
	loop := env.registerDCR(t, "Loopback App", "http://127.0.0.1/cb")
	web := env.registerDCR(t, "Web App", "https://web.example.com/cb", "http://localhost/cb")
	_, challenge := svcPKCE()

	info, oerr := env.svc.ConsentInfo(ctx, user, authParams(loop.ClientID, "http://127.0.0.1:9999/cb", challenge, "s"))
	require.Nil(t, oerr)
	assert.Equal(t, "Loopback App", info.ClientName)
	assert.Equal(t, "127.0.0.1:9999", info.RedirectHost)
	assert.True(t, info.LoopbackWarning, "a client that can only redirect to loopback must carry the warning")
	assert.Equal(t, oauthDefaultScope, info.Scope)
	var found bool
	for _, w := range info.Workspaces {
		found = found || w.ID == ws
	}
	assert.True(t, found, "the user's own workspace must be listed")

	p := authParams(web.ClientID, "https://web.example.com/cb", challenge, "s")
	p.Scope = "mesh"
	info, oerr = env.svc.ConsentInfo(ctx, user, p)
	require.Nil(t, oerr)
	assert.False(t, info.LoopbackWarning)
	assert.Equal(t, "web.example.com", info.RedirectHost)
	assert.Equal(t, "mesh", info.Scope)

	_, oerr = env.svc.ConsentInfo(ctx, user, authParams(web.ClientID, "https://attacker.example.com/cb", challenge, "s"))
	requireOAuthCode(t, oerr, "invalid_request")

	p = authParams(web.ClientID, "https://web.example.com/cb", "", "s")
	_, oerr = env.svc.ConsentInfo(ctx, user, p)
	requireOAuthCode(t, oerr, "invalid_request")
}

// ---------------------------------------------------------------------------
// Decide (consent)
// ---------------------------------------------------------------------------

func TestOAuthSvc_Decide(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, ownerName := env.createUser(t, "owner")
	ws := env.createWorkspace(t, owner)
	redirect := "https://decide.example.com/cb"
	c := env.registerDCR(t, "Decide App "+uuid.New().String()[:6], redirect)
	_, challenge := svcPKCE()

	t.Run("deny redirects with access_denied and state, creates nothing", func(t *testing.T) {
		dest, err := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, redirect, challenge, "st-deny"), UserID: owner, WorkspaceID: ws, Allow: false})
		require.NoError(t, err)
		u, _ := url.Parse(dest)
		assert.Equal(t, "access_denied", u.Query().Get("error"))
		assert.Equal(t, "st-deny", u.Query().Get("state"))
		assert.Empty(t, u.Query().Get("code"))
		var n int
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_grants WHERE client_id=$1`, c.ClientID))
		assert.Zero(t, n)
	})

	t.Run("request error on a trusted redirect is redirected, not returned", func(t *testing.T) {
		p := authParams(c.ClientID, redirect, challenge, "st-rt")
		p.ResponseType = "token"
		dest, err := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: p, UserID: owner, WorkspaceID: ws, Allow: true})
		require.NoError(t, err)
		u, _ := url.Parse(dest)
		assert.Equal(t, "unsupported_response_type", u.Query().Get("error"))
		assert.Empty(t, u.Query().Get("code"))
	})

	t.Run("untrusted redirect is an error, never a redirect", func(t *testing.T) {
		dest, err := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, "https://attacker.example.com/cb", challenge, "s"), UserID: owner, WorkspaceID: ws, Allow: true})
		assert.Empty(t, dest)
		var oe *oautherror.Error
		require.True(t, errors.As(err, &oe))
		assert.Equal(t, "invalid_request", oe.Code)
	})

	t.Run("allow creates connector agent + grant, and reuses them on re-consent", func(t *testing.T) {
		code1 := env.consent(t, c.ClientID, redirect, challenge, owner, ws)
		code2 := env.consent(t, c.ClientID, redirect, challenge, owner, ws)
		assert.NotEqual(t, code1, code2)

		var grants []domain.OAuthGrant
		require.NoError(t, env.db.Select(&grants, `SELECT id, user_id, client_id, workspace_id, agent_id, scope, created_at, revoked_at FROM oauth_grants WHERE client_id=$1`, c.ClientID))
		require.Len(t, grants, 1, "re-consent must reuse the grant")
		var agentName string
		var supervisor uuid.UUID
		require.NoError(t, env.db.QueryRow(`SELECT name, supervisor_user_id FROM agents WHERE id=$1`, grants[0].AgentID).Scan(&agentName, &supervisor))
		assert.Equal(t, c.ClientName+" — "+ownerName, agentName)
		assert.Equal(t, owner, supervisor)

		var codes int
		require.NoError(t, env.db.Get(&codes, `SELECT count(*) FROM oauth_authorization_codes WHERE grant_id=$1 AND code_hash IN ($2,$3)`, grants[0].ID, sha256Hex(code1), sha256Hex(code2)))
		assert.Equal(t, 2, codes, "codes are stored hashed")
	})

	t.Run("re-consent reactivates a revoked grant", func(t *testing.T) {
		var gid uuid.UUID
		require.NoError(t, env.db.Get(&gid, `SELECT id FROM oauth_grants WHERE client_id=$1`, c.ClientID))
		require.NoError(t, env.svc.RevokeMyGrant(ctx, owner, gid))
		env.consent(t, c.ClientID, redirect, challenge, owner, ws)
		var revoked *time.Time
		require.NoError(t, env.db.Get(&revoked, `SELECT revoked_at FROM oauth_grants WHERE id=$1`, gid))
		assert.Nil(t, revoked)
	})

	t.Run("non-member is forbidden", func(t *testing.T) {
		outsider, _ := env.createUser(t, "outsider")
		_, err := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, redirect, challenge, "s"), UserID: outsider, WorkspaceID: ws, Allow: true})
		requireAPIStatus(t, err, http.StatusForbidden)
	})

	t.Run("viewer is refused and no agent is created", func(t *testing.T) {
		viewer, _ := env.createUser(t, "viewer")
		env.addMember(t, ws, viewer, domain.RoleViewer)
		_, err := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, redirect, challenge, "s"), UserID: viewer, WorkspaceID: ws, Allow: true})
		requireAPIStatus(t, err, http.StatusForbidden)
		var n int
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_grants WHERE user_id=$1`, viewer))
		assert.Zero(t, n)
	})

	t.Run("member can consent", func(t *testing.T) {
		member, _ := env.createUser(t, "member")
		env.addMember(t, ws, member, domain.RoleMember)
		env.consent(t, c.ClientID, redirect, challenge, member, ws)
	})

	t.Run("deleted workspace is treated as no membership", func(t *testing.T) {
		u2, _ := env.createUser(t, "delws")
		ws2 := env.createWorkspace(t, u2)
		_, err := env.db.Exec(`UPDATE workspaces SET deleted_at=NOW() WHERE id=$1`, ws2)
		require.NoError(t, err)
		_, derr := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, redirect, challenge, "s"), UserID: u2, WorkspaceID: ws2, Allow: true})
		requireAPIStatus(t, derr, http.StatusForbidden)
	})

	t.Run("owner without a members row can still consent", func(t *testing.T) {
		u3, _ := env.createUser(t, "bareowner")
		ws3 := env.createWorkspace(t, u3)
		var n int
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM workspace_members WHERE workspace_id=$1`, ws3))
		require.Zero(t, n)
		env.consent(t, c.ClientID, redirect, challenge, u3, ws3)
	})
}

// TestOAuthSvc_Decide_SameClientNameDisambiguatesAgentSlug: two separate DCR
// registrations of "the same app" (same client_name, different client_id)
// consented by the same user into the same workspace must produce two working
// connectors, not a unique-constraint 500.
func TestOAuthSvc_Decide_SameClientNameDisambiguatesAgentSlug(t *testing.T) {
	env := newOAuthSvcEnv(t)
	owner, ownerName := env.createUser(t, "dup")
	ws := env.createWorkspace(t, owner)
	name := "Same Name " + uuid.New().String()[:6]
	a := env.registerDCR(t, name, "http://localhost/cb")
	b := env.registerDCR(t, name, "http://localhost/cb")
	_, challenge := svcPKCE()
	env.consent(t, a.ClientID, "http://localhost/cb", challenge, owner, ws)
	env.consent(t, b.ClientID, "http://localhost/cb", challenge, owner, ws)

	var names []string
	require.NoError(t, env.db.Select(&names, `SELECT a.name FROM oauth_grants g JOIN agents a ON a.id=g.agent_id WHERE g.workspace_id=$1 ORDER BY g.created_at`, ws))
	require.Len(t, names, 2)
	base := name + " — " + ownerName
	assert.Equal(t, base, names[0])
	assert.True(t, strings.HasPrefix(names[1], base+" ("), "second connector gets a disambiguating suffix: %q", names[1])
}

// TestOAuthSvc_Decide_UnnamedClientFallsBackToClientID: a DCR client without
// client_name still gets a named connector agent.
func TestOAuthSvc_Decide_UnnamedClientFallsBackToClientID(t *testing.T) {
	env := newOAuthSvcEnv(t)
	owner, ownerName := env.createUser(t, "noname")
	ws := env.createWorkspace(t, owner)
	c := env.registerDCR(t, "   ", "http://localhost/cb")
	_, challenge := svcPKCE()
	env.consent(t, c.ClientID, "http://localhost/cb", challenge, owner, ws)
	var agentName string
	require.NoError(t, env.db.Get(&agentName, `SELECT a.name FROM oauth_grants g JOIN agents a ON a.id=g.agent_id WHERE g.client_id=$1`, c.ClientID))
	assert.Equal(t, c.ClientID+" — "+ownerName, agentName)
}

// TestOAuthSvc_Decide_HandleCollisionWithMemberIsRetried: when a workspace
// MEMBER's username equals the connector agent's natural slug,
// agentService.Register refuses with 409 Conflict. That is a naming clash
// like the agents-slug one, so consent must retry with a suffixed name and
// succeed with a working token — not fail with server_error.
func TestOAuthSvc_Decide_HandleCollisionWithMemberIsRetried(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, ownerName := env.createUser(t, "hc")
	ws := env.createWorkspace(t, owner)
	c := env.registerDCR(t, "Handle "+uuid.New().String()[:6], "http://localhost/cb")

	squatter := uuid.New()
	baseName := c.ClientName + " — " + ownerName
	squatHandle := slugify(baseName)
	_, err := env.db.Exec(`INSERT INTO users (id, email, password_hash, display_name, username, is_active) VALUES ($1,$2,'x','squatter',$3,true)`,
		squatter, squatter.String()[:8]+"@oauth-svc.example.test", squatHandle)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = env.db.Exec(`DELETE FROM users WHERE id=$1`, squatter) })
	env.addMember(t, ws, squatter, domain.RoleMember)

	v, ch := svcPKCE()
	code := env.consent(t, c.ClientID, "http://localhost/cb", ch, owner, ws)

	var agentName, agentSlug string
	require.NoError(t, env.db.QueryRow(`SELECT a.name, a.slug FROM oauth_grants g JOIN agents a ON a.id=g.agent_id WHERE g.client_id=$1`, c.ClientID).Scan(&agentName, &agentSlug))
	assert.True(t, strings.HasPrefix(agentName, baseName+" ("), "retried name carries a suffix: %q", agentName)
	assert.NotEqual(t, squatHandle, agentSlug, "the connector must not take the member's handle")

	tok, oerr := env.svc.ExchangeCode(ctx, c.ClientID, "http://localhost/cb", code, v)
	require.Nil(t, oerr)
	agent, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, agentName, agent.Name)
}

// ---------------------------------------------------------------------------
// Code exchange
// ---------------------------------------------------------------------------

func TestOAuthSvc_ExchangeCode(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, _ := env.createUser(t, "ex")
	ws := env.createWorkspace(t, owner)
	redirect := "http://127.0.0.1/cb"
	c := env.registerDCR(t, "Exchange "+uuid.New().String()[:6], redirect)
	other := env.registerDCR(t, "Other "+uuid.New().String()[:6], redirect)

	fresh := func() (code, verifier string) {
		v, ch := svcPKCE()
		return env.consent(t, c.ClientID, redirect, ch, owner, ws), v
	}

	t.Run("success issues a working access+refresh pair", func(t *testing.T) {
		code, verifier := fresh()
		tok, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, verifier)
		require.Nil(t, oerr)
		assert.True(t, strings.HasPrefix(tok.AccessToken, OAuthAccessTokenPrefix))
		assert.True(t, strings.HasPrefix(tok.RefreshToken, oauthRefreshTokenPrefix))
		assert.Equal(t, "Bearer", tok.TokenType)
		assert.Equal(t, int(oauthAccessTokenTTL.Seconds()), tok.ExpiresIn)
		agent, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, ws, agent.WorkspaceID)
		acc, ref := env.tokenRow(t, tok.AccessToken), env.tokenRow(t, tok.RefreshToken)
		assert.Equal(t, acc.FamilyID, ref.FamilyID)
		assert.Nil(t, ref.ParentTokenID)
	})

	t.Run("missing params", func(t *testing.T) {
		_, oerr := env.svc.ExchangeCode(ctx, "", redirect, "x", "y")
		requireOAuthCode(t, oerr, "invalid_request")
		_, oerr = env.svc.ExchangeCode(ctx, c.ClientID, redirect, "", "y")
		requireOAuthCode(t, oerr, "invalid_request")
		_, oerr = env.svc.ExchangeCode(ctx, c.ClientID, redirect, "x", "")
		requireOAuthCode(t, oerr, "invalid_request")
	})

	t.Run("malformed verifier rejected before lookup; code stays redeemable", func(t *testing.T) {
		code, verifier := fresh()
		_, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, "short")
		requireOAuthCode(t, oerr, "invalid_grant")
		_, oerr = env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, verifier)
		assert.Nil(t, oerr)
	})

	t.Run("unknown code", func(t *testing.T) {
		v, _ := svcPKCE()
		_, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, "not-a-real-code", v)
		requireOAuthCode(t, oerr, "invalid_grant")
	})

	t.Run("wrong verifier, wrong client, wrong redirect — each rejected and code not consumed", func(t *testing.T) {
		code, verifier := fresh()
		wrongV, _ := svcPKCE()
		_, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, wrongV)
		requireOAuthCode(t, oerr, "invalid_grant")
		_, oerr = env.svc.ExchangeCode(ctx, other.ClientID, redirect, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
		_, oerr = env.svc.ExchangeCode(ctx, c.ClientID, "http://127.0.0.1:8080/cb", code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
		var used *time.Time
		require.NoError(t, env.db.Get(&used, `SELECT used_at FROM oauth_authorization_codes WHERE code_hash=$1`, sha256Hex(code)))
		assert.Nil(t, used, "failed attempts must not burn the code")
	})

	t.Run("replay revokes everything the first redemption issued", func(t *testing.T) {
		code, verifier := fresh()
		tok, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, verifier)
		require.Nil(t, oerr)
		_, oerr = env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")

		_, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
		_, oerr = env.svc.RefreshTokenGrant(ctx, c.ClientID, tok.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.NotNil(t, env.tokenRow(t, tok.AccessToken).RevokedAt)
	})

	t.Run("expired code", func(t *testing.T) {
		code, verifier := fresh()
		env.advance(oauthCodeTTL + time.Second)
		defer env.advance(-(oauthCodeTTL + time.Second))
		_, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
	})

	t.Run("code whose grant was revoked after consent", func(t *testing.T) {
		code, verifier := fresh()
		var gid uuid.UUID
		require.NoError(t, env.db.Get(&gid, `SELECT grant_id FROM oauth_authorization_codes WHERE code_hash=$1`, sha256Hex(code)))
		require.NoError(t, env.svc.RevokeMyGrant(ctx, owner, gid))
		_, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
	})
}

// TestOAuthSvc_ExchangeCode_ConcurrentRedemption: many simultaneous
// redemptions of one code yield exactly one token pair.
func TestOAuthSvc_ExchangeCode_ConcurrentRedemption(t *testing.T) {
	env := newOAuthSvcEnv(t)
	owner, _ := env.createUser(t, "race")
	ws := env.createWorkspace(t, owner)
	c := env.registerDCR(t, "Race "+uuid.New().String()[:6], "http://localhost/cb")
	for round := 0; round < 5; round++ {
		exchangeRaceRound(t, env, c, owner, ws)
	}
}

func exchangeRaceRound(t *testing.T, env *oauthSvcEnv, c *domain.OAuthClient, owner, ws uuid.UUID) {
	t.Helper()
	verifier, ch := svcPKCE()
	code := env.consent(t, c.ClientID, "http://localhost/cb", ch, owner, ws)

	const n = 8
	var ok atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, oerr := env.svc.ExchangeCode(context.Background(), c.ClientID, "http://localhost/cb", code, verifier); oerr == nil {
				ok.Add(1)
			} else {
				assert.Equal(t, "invalid_grant", oerr.Code)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(1), ok.Load(), "exactly one redemption may succeed")
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

func TestOAuthSvc_RefreshTokenGrant(t *testing.T) {
	ctx := context.Background()

	t.Run("rotation issues a new pair in the same family and retires the old refresh", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		next, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr)
		assert.NotEqual(t, f.tokens.RefreshToken, next.RefreshToken)
		oldRow, newRow := env.tokenRow(t, f.tokens.RefreshToken), env.tokenRow(t, next.RefreshToken)
		assert.NotNil(t, oldRow.RevokedAt)
		assert.Equal(t, oldRow.FamilyID, newRow.FamilyID)
		require.NotNil(t, newRow.ParentTokenID)
		assert.Equal(t, oldRow.ID, *newRow.ParentTokenID)
		_, err := env.svc.AuthenticateAccessToken(ctx, next.AccessToken)
		assert.NoError(t, err)
	})

	t.Run("reuse of a rotated-away refresh token revokes the whole family", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		next, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr)

		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.Contains(t, oerr.Description, "already been used")

		_, err := env.svc.AuthenticateAccessToken(ctx, next.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, next.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
	})

	t.Run("input validation", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, oerr := env.svc.RefreshTokenGrant(ctx, "", f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_request")
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, "")
		requireOAuthCode(t, oerr, "invalid_request")
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.AccessToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, oauthRefreshTokenPrefix+"unknown")
		requireOAuthCode(t, oerr, "invalid_grant")
		// None of the above consumed the real refresh token.
		assert.Nil(t, env.tokenRow(t, f.tokens.RefreshToken).RevokedAt)
	})

	t.Run("refresh presented by another client is refused and not consumed", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		other := env.registerDCR(t, "Other", "http://localhost/cb")
		_, oerr := env.svc.RefreshTokenGrant(ctx, other.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.Nil(t, env.tokenRow(t, f.tokens.RefreshToken).RevokedAt)
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		assert.Nil(t, oerr)
	})

	t.Run("expired refresh token", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		env.advance(oauthRefreshTokenTTL + time.Minute)
		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.Contains(t, oerr.Description, "expired")
	})

	t.Run("revoked grant", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.db.Exec(`UPDATE oauth_grants SET revoked_at=NOW() WHERE user_id=$1`, f.user)
		require.NoError(t, err)
		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
	})

	t.Run("consenting member removed from workspace: refused and family revoked", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		owner, _ := env.createUser(t, "own")
		ws := env.createWorkspace(t, owner)
		member, _ := env.createUser(t, "mem")
		env.addMember(t, ws, member, domain.RoleMember)
		c := env.registerDCR(t, "Removal "+uuid.New().String()[:6], "http://localhost/cb")
		v, ch := svcPKCE()
		code := env.consent(t, c.ClientID, "http://localhost/cb", ch, member, ws)
		tok, oerr := env.svc.ExchangeCode(ctx, c.ClientID, "http://localhost/cb", code, v)
		require.Nil(t, oerr)

		_, err := env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2`, ws, member)
		require.NoError(t, err)

		_, oerr = env.svc.RefreshTokenGrant(ctx, c.ClientID, tok.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.NotNil(t, env.tokenRow(t, tok.RefreshToken).RevokedAt, "family revoked")
		assert.NotNil(t, env.tokenRow(t, tok.AccessToken).RevokedAt, "family revoked")

		// Re-adding the member does not resurrect the revoked family.
		env.addMember(t, ws, member, domain.RoleMember)
		_, oerr = env.svc.RefreshTokenGrant(ctx, c.ClientID, tok.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
	})

	t.Run("deleted workspace: refused", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.db.Exec(`UPDATE workspaces SET deleted_at=NOW() WHERE id=$1`, f.ws)
		require.NoError(t, err)
		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.NotNil(t, env.tokenRow(t, f.tokens.RefreshToken).RevokedAt)
	})
}

// TestOAuthSvc_RefreshTokenGrant_ConcurrentRotation: simultaneous refreshes
// of one token must never fork the family into two live chains.
func TestOAuthSvc_RefreshTokenGrant_ConcurrentRotation(t *testing.T) {
	env := newOAuthSvcEnv(t)
	for round := 0; round < 5; round++ {
		refreshRaceRound(t, env)
	}
}

func refreshRaceRound(t *testing.T, env *oauthSvcEnv) {
	t.Helper()
	f := env.fullFlow(t)
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []*TokenResponse
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tok, oerr := env.svc.RefreshTokenGrant(context.Background(), f.client.ClientID, f.tokens.RefreshToken)
			if oerr == nil {
				mu.Lock()
				winners = append(winners, tok)
				mu.Unlock()
				return
			}
			assert.Equal(t, "invalid_grant", oerr.Code)
		}()
	}
	close(start)
	wg.Wait()
	assert.LessOrEqual(t, len(winners), 1, "at most one concurrent refresh may succeed")

	var live int
	fam := env.tokenRow(t, f.tokens.RefreshToken).FamilyID
	require.NoError(t, env.db.Get(&live, `SELECT count(*) FROM oauth_tokens WHERE family_id=$1 AND token_type='refresh' AND revoked_at IS NULL`, fam))
	assert.LessOrEqual(t, live, 1, "never more than one live refresh token per family")
}

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

func TestOAuthSvc_RevokeToken(t *testing.T) {
	ctx := context.Background()

	t.Run("empty and unknown tokens are silently accepted (RFC 7009)", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		assert.NoError(t, env.svc.RevokeToken(ctx, ""))
		assert.NoError(t, env.svc.RevokeToken(ctx, "mot_nope"))
	})

	t.Run("revoking an access token kills only that token", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		require.NoError(t, env.svc.RevokeToken(ctx, f.tokens.AccessToken))
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
		next, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr, "the paired refresh token must survive")
		_, err = env.svc.AuthenticateAccessToken(ctx, next.AccessToken)
		assert.NoError(t, err)
		// Revoking twice is still fine.
		assert.NoError(t, env.svc.RevokeToken(ctx, f.tokens.AccessToken))
	})

	t.Run("revoking a refresh token kills the whole family", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		next, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr)
		require.NoError(t, env.svc.RevokeToken(ctx, next.RefreshToken))
		_, err := env.svc.AuthenticateAccessToken(ctx, next.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, next.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
	})
}

// ---------------------------------------------------------------------------
// Access-token authentication
// ---------------------------------------------------------------------------

func TestOAuthSvc_AuthenticateAccessToken(t *testing.T) {
	ctx := context.Background()

	t.Run("resolves the connector agent into the grant's workspace", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		agent, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, f.ws, agent.WorkspaceID)
		assert.Equal(t, oauthConnectorWorkspaceRole, agent.WorkspaceRole)
		require.NotNil(t, agent.OAuthConnectorUserID)
		assert.Equal(t, f.user, *agent.OAuthConnectorUserID)
		assert.Contains(t, agent.Name, f.username)
	})

	t.Run("rejects wrong prefix, unknown, and refresh tokens", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		for _, tok := range []string{"", "agk_x", f.tokens.RefreshToken, OAuthAccessTokenPrefix + "unknown", strings.TrimPrefix(f.tokens.AccessToken, OAuthAccessTokenPrefix)} {
			a, err := env.svc.AuthenticateAccessToken(ctx, tok)
			assert.Nil(t, a)
			requireAPIStatus(t, err, http.StatusUnauthorized)
		}
	})

	t.Run("expired access token", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		env.advance(oauthAccessTokenTTL + time.Second)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("revoked grant via RevokeMyGrant", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		var gid uuid.UUID
		require.NoError(t, env.db.Get(&gid, `SELECT id FROM oauth_grants WHERE client_id=$1`, f.client.ClientID))
		// Revoke only the grant row (tokens stay "live") to prove the grant
		// check itself is enforced, not just token revocation.
		_, err := env.db.Exec(`UPDATE oauth_grants SET revoked_at=NOW() WHERE id=$1`, gid)
		require.NoError(t, err)
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("member removed from workspace loses access immediately", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		owner, _ := env.createUser(t, "own")
		ws := env.createWorkspace(t, owner)
		member, _ := env.createUser(t, "mem")
		env.addMember(t, ws, member, domain.RoleAdmin)
		c := env.registerDCR(t, "AuthRemoval "+uuid.New().String()[:6], "http://localhost/cb")
		v, ch := svcPKCE()
		code := env.consent(t, c.ClientID, "http://localhost/cb", ch, member, ws)
		tok, oerr := env.svc.ExchangeCode(ctx, c.ClientID, "http://localhost/cb", code, v)
		require.Nil(t, oerr)
		_, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		require.NoError(t, err)

		_, err = env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2`, ws, member)
		require.NoError(t, err)
		_, err = env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("deleted workspace", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.db.Exec(`UPDATE workspaces SET deleted_at=NOW() WHERE id=$1`, f.ws)
		require.NoError(t, err)
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("soft-deleted connector agent is refused", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=(SELECT agent_id FROM oauth_grants WHERE client_id=$1)`, f.client.ClientID)
		require.NoError(t, err)
		a, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		assert.Nil(t, a)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})
}

// ---------------------------------------------------------------------------
// "Connected apps" API
// ---------------------------------------------------------------------------

func TestOAuthSvc_ListAndRevokeMyGrants(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	f := env.fullFlow(t)
	stranger, _ := env.createUser(t, "stranger")

	grants, err := env.svc.ListMyGrants(ctx, f.user)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	g := grants[0]
	assert.Equal(t, f.client.ClientName, g.ClientName)
	assert.Equal(t, f.ws, g.Workspace.ID)
	assert.Contains(t, g.AgentName, f.username)
	assert.Nil(t, g.RevokedAt)

	none, err := env.svc.ListMyGrants(ctx, stranger)
	require.NoError(t, err)
	assert.Empty(t, none, "grants are per-user")

	// Someone else's grant: 404, and nothing changes.
	requireAPIStatus(t, env.svc.RevokeMyGrant(ctx, stranger, g.ID), http.StatusNotFound)
	requireAPIStatus(t, env.svc.RevokeMyGrant(ctx, f.user, uuid.New()), http.StatusNotFound)
	_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
	require.NoError(t, err, "a refused revoke must not affect the owner's tokens")

	require.NoError(t, env.svc.RevokeMyGrant(ctx, f.user, g.ID))
	_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
	requireAPIStatus(t, err, http.StatusUnauthorized)
	_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
	requireOAuthCode(t, oerr, "invalid_grant")
	var live int
	require.NoError(t, env.db.Get(&live, `SELECT count(*) FROM oauth_tokens WHERE grant_id=$1 AND revoked_at IS NULL`, g.ID))
	assert.Zero(t, live, "every token under the grant is revoked, not just the grant row")

	grants, err = env.svc.ListMyGrants(ctx, f.user)
	require.NoError(t, err)
	require.Len(t, grants, 1, "revoked grants stay listed")
	assert.NotNil(t, grants[0].RevokedAt)
}

// ---------------------------------------------------------------------------
// Database unavailable: every entry point fails closed
// ---------------------------------------------------------------------------

// TestOAuthSvc_DatabaseDownFailsClosed uses the real repositories over a
// closed connection pool. No entry point may report success, issue a token,
// or authenticate when its first lookup cannot be made.
func TestOAuthSvc_DatabaseDownFailsClosed(t *testing.T) {
	live := newOAuthSvcEnv(t) // skips if no DB; also pins timeNow
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	dead, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, dead.Close())
	_ = live

	userRepo := postgres.NewUserRepo(dead)
	wsRepo := postgres.NewWorkspaceRepo(dead)
	agentSvc := NewAgentService(postgres.NewAgentRepo(dead), postgres.NewActivityLogRepo(dead), wsRepo, userRepo)
	svc := NewOAuthService(postgres.NewOAuthRepo(dead), agentSvc, userRepo, wsRepo, postgres.NewWorkspaceMemberRepo(dead), postgres.NewAgentWorkspaceGrantRepo(dead))
	ctx := context.Background()
	v, _ := svcPKCE()

	_, oerr := svc.RegisterClientDCR(ctx, DCRRegisterInput{RedirectURIs: []string{"https://x.example.com/cb"}})
	requireOAuthCode(t, oerr, "server_error")
	_, oerr = svc.ResolveClient(ctx, "mcpc_x")
	requireOAuthCode(t, oerr, "server_error")
	_, oerr = svc.ExchangeCode(ctx, "mcpc_x", "https://x.example.com/cb", "code", v)
	requireOAuthCode(t, oerr, "server_error")
	_, oerr = svc.RefreshTokenGrant(ctx, "mcpc_x", oauthRefreshTokenPrefix+"x")
	requireOAuthCode(t, oerr, "server_error")
	assert.Error(t, svc.RevokeToken(ctx, "mot_x"))
	a, err := svc.AuthenticateAccessToken(ctx, OAuthAccessTokenPrefix+"x")
	assert.Nil(t, a)
	assert.Error(t, err)
	_, err = svc.ListMyGrants(ctx, uuid.New())
	assert.Error(t, err)
	assert.Error(t, svc.RevokeMyGrant(ctx, uuid.New(), uuid.New()))
	_, err = svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams("mcpc_x", "https://x.example.com/cb", "", "s"), UserID: uuid.New(), WorkspaceID: uuid.New(), Allow: true})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Additional CIMD cache and storage-failure paths
// ---------------------------------------------------------------------------

// TestOAuthSvc_ResolveClient_CIMDWithoutFetchTimestampIsRefetched: a cached
// CIMD row with no metadata_fetched_at is treated as stale, and a refetched
// document's grant_types replace the stored ones.
func TestOAuthSvc_ResolveClient_CIMDWithoutFetchTimestampIsRefetched(t *testing.T) {
	env := newOAuthSvcEnv(t)
	var grants atomic.Value
	grants.Store([]string{"authorization_code", "refresh_token"})
	clientID, hits := env.cimdServer(t, func(w http.ResponseWriter, self string) {
		writeJSON(w, map[string]interface{}{"client_id": self, "client_name": "TS", "redirect_uris": []string{"https://ts.example.com/cb"}, "grant_types": grants.Load()})
	})
	_, oerr := env.svc.ResolveClient(context.Background(), clientID)
	require.Nil(t, oerr)
	_, err := env.db.Exec(`UPDATE oauth_clients SET metadata_fetched_at=NULL WHERE client_id=$1`, clientID)
	require.NoError(t, err)

	grants.Store([]string{"authorization_code"})
	c, oerr := env.svc.ResolveClient(context.Background(), clientID)
	require.Nil(t, oerr)
	assert.Equal(t, int32(2), hits.Load(), "a row with no fetch timestamp must be refetched")
	assert.ElementsMatch(t, []string{"authorization_code"}, c.GrantTypes)
	var fetched *time.Time
	require.NoError(t, env.db.Get(&fetched, `SELECT metadata_fetched_at FROM oauth_clients WHERE client_id=$1`, clientID))
	assert.NotNil(t, fetched, "the refetch must record its timestamp")
}

// TestOAuthSvc_ControlCharactersInMetadataAreRejected: control characters
// (NUL, which Postgres cannot store at all, and others like CR/LF/ESC) in
// client_name or a redirect_uri are a client error — invalid_client_metadata
// — on DCR, on CIMD first sight, and on a CIMD refresh (where the cached row
// must be left unchanged). Never a 5xx, never stored.
func TestOAuthSvc_ControlCharactersInMetadataAreRejected(t *testing.T) {
	ctx := context.Background()
	badNames := []string{"nul\x00name", "cr\rlf\nname", "esc\x1bname", "del\x7fname", "c1\u0085name"}
	badURIs := []string{"https://n.example.com/cb\x00", "https://n.example.com/c\r\nb", "https://n.example.com/\tcb"}

	t.Run("dcr client_name", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		for _, name := range badNames {
			c, oerr := env.svc.RegisterClientDCR(ctx, DCRRegisterInput{ClientName: name, RedirectURIs: []string{"https://n.example.com/cb"}})
			assert.Nil(t, c, "%q", name)
			if c != nil {
				env.cleanupClient(t, c.ClientID)
			}
			requireOAuthCode(t, oerr, "invalid_client_metadata")
		}
	})

	t.Run("dcr redirect_uri", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		for _, ru := range badURIs {
			name := "ctl-" + uuid.New().String()[:8]
			c, oerr := env.svc.RegisterClientDCR(ctx, DCRRegisterInput{ClientName: name, RedirectURIs: []string{"https://ok.example.com/cb", ru}})
			assert.Nil(t, c, "%q", ru)
			if c != nil {
				env.cleanupClient(t, c.ClientID)
			}
			requireOAuthCode(t, oerr, "invalid_client_metadata")
			var n int
			require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_clients WHERE client_name=$1`, name))
			assert.Zero(t, n)
		}
	})

	cimdCases := map[string]map[string]interface{}{
		"nul in client_name":     {"client_name": "nul\x00name", "redirect_uris": []string{"https://n.example.com/cb"}},
		"newline in client_name": {"client_name": "a\nb", "redirect_uris": []string{"https://n.example.com/cb"}},
		"nul in redirect_uri":    {"client_name": "ok", "redirect_uris": []string{"https://n.example.com/cb\x00"}},
		"crlf in redirect_uri":   {"client_name": "ok", "redirect_uris": []string{"https://n.example.com/c\r\nb"}},
	}
	for name, doc := range cimdCases {
		doc := doc
		t.Run("cimd first sight: "+name, func(t *testing.T) {
			env := newOAuthSvcEnv(t)
			clientID, _ := env.cimdServer(t, func(w http.ResponseWriter, self string) {
				d := map[string]interface{}{"client_id": self}
				for k, v := range doc {
					d[k] = v
				}
				writeJSON(w, d)
			})
			c, oerr := env.svc.ResolveClient(ctx, clientID)
			assert.Nil(t, c)
			requireOAuthCode(t, oerr, "invalid_client_metadata")
			var n int
			require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_clients WHERE client_id=$1`, clientID))
			assert.Zero(t, n)
		})
	}

	t.Run("cimd refresh leaves the cached row unchanged", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		var name atomic.Value
		name.Store("clean")
		clientID, _ := env.cimdServer(t, func(w http.ResponseWriter, self string) {
			writeJSON(w, map[string]interface{}{"client_id": self, "client_name": name.Load(), "redirect_uris": []string{"https://n.example.com/cb"}})
		})
		_, oerr := env.svc.ResolveClient(ctx, clientID)
		require.Nil(t, oerr)
		name.Store("nul\x00name")
		env.advance(2 * oauthClientCacheTTL)
		c, oerr := env.svc.ResolveClient(ctx, clientID)
		assert.Nil(t, c)
		requireOAuthCode(t, oerr, "invalid_client_metadata")
		var stored string
		require.NoError(t, env.db.Get(&stored, `SELECT client_name FROM oauth_clients WHERE client_id=$1`, clientID))
		assert.Equal(t, "clean", stored)
	})
}

// TestOAuthSvc_MembershipLookupUnavailableFailsClosed wires the service with
// REAL repositories where only the workspace / membership / user store's
// connection is down (a closed pool). Consent must not be granted and
// tokens must stop authenticating when membership cannot be confirmed.
func TestOAuthSvc_MembershipLookupUnavailableFailsClosed(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	dead, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, dead.Close())

	owner, _ := env.createUser(t, "own")
	ws := env.createWorkspace(t, owner)
	member, _ := env.createUser(t, "mem")
	env.addMember(t, ws, member, domain.RoleMember)
	redirect := "https://mdown.example.com/cb"
	c := env.registerDCR(t, "MDown "+uuid.New().String()[:6], redirect)
	v, ch := svcPKCE()
	code := env.consent(t, c.ClientID, redirect, ch, member, ws)
	tok, oerr := env.svc.ExchangeCode(ctx, c.ClientID, redirect, code, v)
	require.Nil(t, oerr)

	live := env.svc
	with := func(mut func(s *oauthService)) *oauthService {
		cp := *live
		mut(&cp)
		return &cp
	}
	deadWS := with(func(s *oauthService) { s.workspaceRepo = postgres.NewWorkspaceRepo(dead) })
	deadMembers := with(func(s *oauthService) { s.workspaceMemberRepo = postgres.NewWorkspaceMemberRepo(dead) })
	deadUsers := with(func(s *oauthService) { s.userRepo = postgres.NewUserRepo(dead) })

	t.Run("consent info cannot list workspaces", func(t *testing.T) {
		_, oerr := deadWS.ConsentInfo(ctx, member, authParams(c.ClientID, redirect, ch, "s"))
		requireOAuthCode(t, oerr, "server_error")
	})

	t.Run("decide with workspace lookup down", func(t *testing.T) {
		dest, err := deadWS.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, redirect, ch, "s"), UserID: member, WorkspaceID: ws, Allow: true})
		assert.Empty(t, dest)
		requireAPIStatus(t, err, http.StatusInternalServerError)
	})

	t.Run("decide with membership lookup down (non-owner)", func(t *testing.T) {
		dest, err := deadMembers.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, redirect, ch, "s"), UserID: member, WorkspaceID: ws, Allow: true})
		assert.Empty(t, dest)
		requireAPIStatus(t, err, http.StatusInternalServerError)
	})

	t.Run("first consent with user lookup down creates nothing", func(t *testing.T) {
		c2 := env.registerDCR(t, "MDown2 "+uuid.New().String()[:6], redirect)
		dest, err := deadUsers.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c2.ClientID, redirect, ch, "s"), UserID: owner, WorkspaceID: ws, Allow: true})
		assert.Empty(t, dest)
		var oe *oautherror.Error
		require.True(t, errors.As(err, &oe))
		assert.Equal(t, "server_error", oe.Code)
		var n int
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_grants WHERE client_id=$1`, c2.ClientID))
		assert.Zero(t, n)
	})

	t.Run("access token refused when membership cannot be confirmed", func(t *testing.T) {
		_, err := deadMembers.AuthenticateAccessToken(ctx, tok.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
		// …but it is not revoked: the live service still accepts it.
		_, err = live.AuthenticateAccessToken(ctx, tok.AccessToken)
		assert.NoError(t, err)
	})

	t.Run("refresh during a membership-store outage is server_error and revokes nothing", func(t *testing.T) {
		_, oerr := deadMembers.RefreshTokenGrant(ctx, c.ClientID, tok.RefreshToken)
		requireOAuthCode(t, oerr, "server_error")

		var liveCount int
		fam := env.tokenRow(t, tok.RefreshToken).FamilyID
		require.NoError(t, env.db.Get(&liveCount, `SELECT count(*) FROM oauth_tokens WHERE family_id=$1 AND revoked_at IS NULL`, fam))
		assert.Equal(t, 2, liveCount, "an outage must not revoke the family")

		// Store back: the very same refresh token still rotates.
		next, oerr := live.RefreshTokenGrant(ctx, c.ClientID, tok.RefreshToken)
		require.Nil(t, oerr, "the family must still be usable once the store is back")
		_, err := env.svc.AuthenticateAccessToken(ctx, next.AccessToken)
		assert.NoError(t, err)
	})
}

// TestOAuthSvc_AgentStoreUnavailable wires the service's agentService over
// REAL repositories whose connection is closed (only the agent side; the
// OAuth, workspace and member stores stay live). A failure that is not a
// naming clash must not be retried as one or reported as success, and an
// agent-store failure during authentication must not authenticate.
func TestOAuthSvc_AgentStoreUnavailable(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	dead, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, dead.Close())

	f := env.fullFlow(t)

	t.Run("register fails (user-handle lookup down): server_error, nothing created", func(t *testing.T) {
		// Live workspace repo (Register needs the slug), dead user repo (the
		// handle-collision lookup Register runs next) -> a 500-class error
		// that is neither a slug nor a username conflict.
		agentSvc := NewAgentService(postgres.NewAgentRepo(env.db), postgres.NewActivityLogRepo(env.db),
			postgres.NewWorkspaceRepo(env.db), postgres.NewUserRepo(dead))
		cp := *env.svc
		cp.agentService = agentSvc

		c := env.registerDCR(t, "AgentDown "+uuid.New().String()[:6], "http://localhost/cb")
		_, ch := svcPKCE()
		dest, derr := cp.Decide(ctx, ConsentDecisionInput{AuthorizeParams: authParams(c.ClientID, "http://localhost/cb", ch, "s"), UserID: f.user, WorkspaceID: f.ws, Allow: true})
		assert.Empty(t, dest)
		var oe *oautherror.Error
		require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
		assert.Equal(t, "server_error", oe.Code)
		assert.NotContains(t, oe.Description, "persisted after retries", "a non-conflict failure must not be retried as a name clash")
		var n int
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_grants WHERE client_id=$1`, c.ClientID))
		assert.Zero(t, n)
		require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM agents WHERE workspace_id=$1 AND name LIKE $2`, f.ws, c.ClientName+"%"))
		assert.Zero(t, n)
	})

	t.Run("authenticate with agent lookup down: refused, error passed through (not 401/404)", func(t *testing.T) {
		agentSvc := NewAgentService(postgres.NewAgentRepo(dead), postgres.NewActivityLogRepo(dead),
			postgres.NewWorkspaceRepo(env.db), postgres.NewUserRepo(env.db))
		cp := *env.svc
		cp.agentService = agentSvc

		a, aerr := cp.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		assert.Nil(t, a)
		require.Error(t, aerr)
		var ae *apierror.Error
		if errors.As(aerr, &ae) {
			assert.NotEqual(t, http.StatusNotFound, ae.Code)
			assert.NotEqual(t, http.StatusUnauthorized, ae.Code, "an outage is not an invalid token")
		}
		// The token itself is untouched.
		_, aerr = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		assert.NoError(t, aerr)
	})
}
