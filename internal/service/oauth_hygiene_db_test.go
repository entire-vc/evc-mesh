package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// OAuth hygiene (#23579e6b): periodic purge, grant_type enforcement on
// /oauth/token, the mot_ authentication cache, and the single invalid-code
// description. Real Postgres throughout, like the rest of the *_db_test.go
// files in this package; the fake clock is oauthSvcEnv.advance.

// --- 1. purge of expired codes and tokens ---

// insertToken writes a raw oauth_tokens row so the test controls expires_at
// and revoked_at exactly — the service only ever creates fresh ones.
func (env *oauthSvcEnv) insertToken(t *testing.T, grantID uuid.UUID, typ string, expires time.Time, revoked *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.db.Exec(
		`INSERT INTO oauth_tokens (id, grant_id, token_type, token_hash, family_id, expires_at, revoked_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, grantID, typ, "purge-test-"+id.String(), uuid.New(), expires, revoked)
	require.NoError(t, err)
	return id
}

func (env *oauthSvcEnv) insertCode(t *testing.T, clientID string, grantID uuid.UUID, expires time.Time, used *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.db.Exec(
		`INSERT INTO oauth_authorization_codes (id, code_hash, client_id, redirect_uri, code_challenge, grant_id, expires_at, used_at)
		 VALUES ($1, $2, $3, 'http://127.0.0.1/cb', 'c', $4, $5, $6)`,
		id, "purge-test-"+id.String(), clientID, grantID, expires, used)
	require.NoError(t, err)
	return id
}

func (env *oauthSvcEnv) rowExists(t *testing.T, table string, id uuid.UUID) bool {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT COUNT(*) FROM `+table+` WHERE id = $1`, id))
	return n == 1
}

func TestOAuthSvc_PurgeExpired(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	grantID := env.tokenRow(t, f.tokens.AccessToken).GrantID
	now := time.Now()
	past := func(d time.Duration) time.Time { return now.Add(-d) }
	revoked := past(time.Minute)

	// Tokens.
	oldExpired := env.insertToken(t, grantID, "access", past(oauthTokenRetention+time.Hour), nil)
	oldExpiredRevoked := env.insertToken(t, grantID, "refresh", past(oauthTokenRetention+time.Hour), &revoked)
	recentlyExpired := env.insertToken(t, grantID, "access", past(time.Hour), nil)
	liveRevoked := env.insertToken(t, grantID, "refresh", now.Add(24*time.Hour), &revoked)
	live := env.insertToken(t, grantID, "access", now.Add(time.Hour), nil)

	// Codes.
	oldCode := env.insertCode(t, f.client.ClientID, grantID, past(oauthCodeRetention+time.Hour), nil)
	oldUsedCode := env.insertCode(t, f.client.ClientID, grantID, past(oauthCodeRetention+time.Hour), &revoked)
	recentCode := env.insertCode(t, f.client.ClientID, grantID, past(time.Minute), nil)
	liveUsedCode := env.insertCode(t, f.client.ClientID, grantID, now.Add(time.Minute), &revoked)

	codes, tokens, err := env.svc.PurgeExpired(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, codes, int64(2), "both codes past retention are removed")
	assert.GreaterOrEqual(t, tokens, int64(2), "both tokens past retention are removed")

	for name, id := range map[string]uuid.UUID{"expired token": oldExpired, "expired revoked token": oldExpiredRevoked} {
		assert.False(t, env.rowExists(t, "oauth_tokens", id), "%s past retention must be gone", name)
	}
	for name, id := range map[string]uuid.UUID{
		"token inside retention grace":                    recentlyExpired,
		"revoked but unexpired refresh (reuse detection)": liveRevoked,
		"live access token":                               live,
	} {
		assert.True(t, env.rowExists(t, "oauth_tokens", id), "%s must survive", name)
	}
	for name, id := range map[string]uuid.UUID{"expired code": oldCode, "expired used code": oldUsedCode} {
		assert.False(t, env.rowExists(t, "oauth_authorization_codes", id), "%s past retention must be gone", name)
	}
	for name, id := range map[string]uuid.UUID{"code inside retention grace": recentCode, "used but unexpired code (replay detection)": liveUsedCode} {
		assert.True(t, env.rowExists(t, "oauth_authorization_codes", id), "%s must survive", name)
	}

	// The tokens the flow itself issued are untouched and still work.
	_, aerr := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
	require.NoError(t, aerr)
	_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
	require.Nil(t, oerr)

	// Idempotent: a second pass finds nothing of ours left to remove.
	_, _, err = env.svc.PurgeExpired(ctx)
	require.NoError(t, err)
}

// --- 4. /oauth/token grant_type must be one the client registered for ---

func TestOAuthSvc_TokenEndpointEnforcesClientGrantTypes(t *testing.T) {
	ctx := context.Background()

	t.Run("client without refresh_token cannot refresh", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.db.Exec(`UPDATE oauth_clients SET grant_types = ARRAY['authorization_code'] WHERE client_id = $1`, f.client.ClientID)
		require.NoError(t, err)

		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "unauthorized_client")
		row := env.tokenRow(t, f.tokens.RefreshToken)
		assert.Nil(t, row.RevokedAt, "a refused refresh must not rotate or revoke the presented token")
	})

	t.Run("client without authorization_code cannot redeem a code, and the code survives", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		user, _ := env.createUser(t, "owner")
		ws := env.createWorkspace(t, user)
		redirect := "http://127.0.0.1/cb"
		client := env.registerDCR(t, "NoAuthCode "+uuid.New().String()[:8], redirect)
		verifier, challenge := svcPKCE()
		code := env.consent(t, client.ClientID, redirect, challenge, user, ws)
		_, err := env.db.Exec(`UPDATE oauth_clients SET grant_types = ARRAY['refresh_token'] WHERE client_id = $1`, client.ClientID)
		require.NoError(t, err)

		_, oerr := env.svc.ExchangeCode(ctx, client.ClientID, redirect, code, verifier)
		requireOAuthCode(t, oerr, "unauthorized_client")

		_, err = env.db.Exec(`UPDATE oauth_clients SET grant_types = ARRAY['authorization_code'] WHERE client_id = $1`, client.ClientID)
		require.NoError(t, err)
		tok, oerr := env.svc.ExchangeCode(ctx, client.ClientID, redirect, code, verifier)
		require.Nil(t, oerr, "the refused attempt must not have burned the code")
		assert.NotEmpty(t, tok.AccessToken)
	})

	t.Run("control: a client registered for both still works end to end", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr)
	})
}

// --- 5. mot_ authentication cache ---

// memberFlow is a flow whose consenting user is a plain workspace MEMBER (not
// the workspace owner, whom the membership check always accepts), so removing
// the membership row really does revoke access.
type memberFlowResult struct {
	ws, member uuid.UUID
	tokens     *TokenResponse
}

func (env *oauthSvcEnv) memberFlow(t *testing.T) *memberFlowResult {
	t.Helper()
	owner, _ := env.createUser(t, "own")
	ws := env.createWorkspace(t, owner)
	member, _ := env.createUser(t, "mem")
	env.addMember(t, ws, member, domain.RoleAdmin)
	c := env.registerDCR(t, "MemberFlow "+uuid.New().String()[:6], "http://localhost/cb")
	v, ch := svcPKCE()
	code := env.consent(t, c.ClientID, "http://localhost/cb", ch, member, ws)
	tok, oerr := env.svc.ExchangeCode(context.Background(), c.ClientID, "http://localhost/cb", code, v)
	require.Nil(t, oerr)
	return &memberFlowResult{ws: ws, member: member, tokens: tok}
}

func (env *oauthSvcEnv) removeMember(t *testing.T, m *memberFlowResult) {
	t.Helper()
	_, err := env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2`, m.ws, m.member)
	require.NoError(t, err)
}

func TestOAuthSvc_AuthCache(t *testing.T) {
	ctx := context.Background()

	t.Run("a grant revoked behind the service's back is honoured on the next request", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken) // cache hit
		require.NoError(t, err)

		_, err = env.db.Exec(`UPDATE oauth_grants SET revoked_at = NOW() WHERE user_id = $1`, f.user)
		require.NoError(t, err)

		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("membership removal is visible after the TTL and not before it", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		m := env.memberFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		require.NoError(t, err)

		// Behind the service's back — the way an admin action reaches the DB.
		env.removeMember(t, m)

		_, err = env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		require.NoError(t, err, "inside the TTL the cached authentication is served (this is what the cache is for)")

		env.advance(oauthAuthCacheTTL + time.Second)
		_, err = env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("control: a cache that outlives the TTL WOULD serve the removed member", func(t *testing.T) {
		// The red run for the test above: same steps with a 1 h TTL. If this
		// stopped serving the stale entry, the previous test would prove nothing.
		env := newOAuthSvcEnv(t)
		env.svc.SetAuthCacheTTLForTesting(time.Hour)
		m := env.memberFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		require.NoError(t, err)
		env.removeMember(t, m)

		env.advance(oauthAuthCacheTTL + time.Second)
		_, err = env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		require.NoError(t, err, "with a 1 h TTL the stale entry is still served — the previous subtest would have failed")
	})

	t.Run("agent connection cut by an admin is visible after the TTL", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)
		env.revokeAgentConnection(t, env.grantAgentID(t, f.client.ClientID), f.ws)

		env.advance(oauthAuthCacheTTL + time.Second)
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("RevokeMyGrant takes effect at once", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)

		require.NoError(t, env.svc.RevokeMyGrant(ctx, f.user, env.tokenRow(t, f.tokens.AccessToken).GrantID))
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("revoking the refresh token kills the cached access token at once", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)

		require.NoError(t, env.svc.RevokeToken(ctx, f.tokens.RefreshToken))
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("revoking the access token itself takes effect at once", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)

		require.NoError(t, env.svc.RevokeToken(ctx, f.tokens.AccessToken))
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("refresh-token reuse detection kills the cached access token at once", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)

		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr)
		_, oerr = env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken) // replay
		requireOAuthCode(t, oerr, "invalid_grant")

		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("re-consent that retargets the grant kills the old agent's cached token at once", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)

		env.revokeAgentConnection(t, env.grantAgentID(t, f.client.ClientID), f.ws)
		_, challenge := svcPKCE()
		_ = env.consent(t, f.client.ClientID, f.redirect, challenge, f.user, f.ws) // registers a new agent, retargets the grant

		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("a caller mutating the returned agent cannot poison the cache", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		first, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)
		want := first.WorkspaceID

		hit, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken) // served from cache
		require.NoError(t, err)
		hit.WorkspaceID = uuid.New()
		hit.WorkspaceRole = "owner"

		again, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, want, again.WorkspaceID)
		assert.Equal(t, "member", again.WorkspaceRole)
	})

	t.Run("a bad token is never cached as bad", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, "mot_definitely-not-a-token")
		requireAPIStatus(t, err, http.StatusUnauthorized)
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err)
	})

	t.Run("TTL zero disables the cache", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		env.svc.SetAuthCacheTTLForTesting(0)
		m := env.memberFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		require.NoError(t, err)
		env.removeMember(t, m)
		_, err = env.svc.AuthenticateAccessToken(ctx, m.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})
}

// The cache must never vouch for a token past the token's own expires_at.
func TestOAuthAuthCache_EntryNeverOutlivesTheToken(t *testing.T) {
	now := time.Now()
	c := newOAuthAuthCache(15 * time.Second)
	agent := &domain.Agent{ID: uuid.New(), Name: "a"}

	c.put("k", agent, uuid.New(), uuid.New(), now.Add(2*time.Second), now, c.generation())
	_, _, ok := c.get("k", now.Add(time.Second))
	assert.True(t, ok)
	_, _, ok = c.get("k", now.Add(3*time.Second))
	assert.False(t, ok, "token expired at +2s; a 15 s cache TTL must not extend it")
}

// A request that read the database before a revoke and finishes after the
// eviction must not put its (stale) answer back.
func TestOAuthAuthCache_PutAfterEvictionIsDropped(t *testing.T) {
	now := time.Now()
	agent := &domain.Agent{ID: uuid.New()}

	for name, evict := range map[string]func(c *oauthAuthCache, grant, family uuid.UUID){
		"grant":  func(c *oauthAuthCache, grant, _ uuid.UUID) { c.evictGrant(grant) },
		"family": func(c *oauthAuthCache, _, family uuid.UUID) { c.evictFamily(family) },
		"token":  func(c *oauthAuthCache, _, _ uuid.UUID) { c.evictToken("k") },
	} {
		t.Run(name, func(t *testing.T) {
			c := newOAuthAuthCache(time.Minute)
			grant, family := uuid.New(), uuid.New()
			gen := c.generation()   // request starts: snapshot
			evict(c, grant, family) // revoke lands while it is reading
			c.put("k", agent, grant, family, now.Add(time.Hour), now, gen)
			_, _, ok := c.get("k", now)
			assert.False(t, ok, "an answer read before the eviction must not be cached after it")

			c.put("k", agent, grant, family, now.Add(time.Hour), now, c.generation())
			_, _, ok = c.get("k", now)
			assert.True(t, ok, "control: a request that started after the eviction caches normally")
		})
	}
}

func TestOAuthAuthCache_BoundedSize(t *testing.T) {
	now := time.Now()
	c := newOAuthAuthCache(time.Minute)
	agent := &domain.Agent{ID: uuid.New()}
	for i := 0; i < oauthAuthCacheMaxEntries+10; i++ {
		c.put(uuid.NewString(), agent, uuid.New(), uuid.New(), now.Add(time.Hour), now, c.generation())
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	assert.LessOrEqual(t, n, oauthAuthCacheMaxEntries)
}

// --- 7. one neutral description for every unredeemable code ---

func TestOAuthSvc_UnredeemableCodesShareOneDescription(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	user, _ := env.createUser(t, "owner")
	ws := env.createWorkspace(t, user)
	redirect := "http://127.0.0.1/cb"
	client := env.registerDCR(t, "DescClient "+uuid.New().String()[:8], redirect)

	fresh := func() (code, verifier string) {
		v, ch := svcPKCE()
		return env.consent(t, client.ClientID, redirect, ch, user, ws), v
	}
	descOf := func(t *testing.T, code, verifier string) string {
		t.Helper()
		_, oerr := env.svc.ExchangeCode(ctx, client.ClientID, redirect, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
		return oerr.Description
	}
	wellFormedVerifier, _ := svcPKCE()

	got := map[string]string{}

	got["nonexistent code, well-formed verifier"] = descOf(t, "no-such-code-"+uuid.NewString(), wellFormedVerifier)
	got["nonexistent code, malformed verifier"] = descOf(t, "no-such-code-"+uuid.NewString(), "short")

	code, _ := fresh()
	got["wrong verifier for a real code"] = descOf(t, code, wellFormedVerifier)

	code, verifier := fresh()
	_, oerr := env.svc.ExchangeCode(ctx, client.ClientID, redirect, code, verifier)
	require.Nil(t, oerr)
	got["already used code"] = descOf(t, code, verifier)

	code, verifier = fresh()
	env.advance(oauthCodeTTL + time.Second)
	got["expired code"] = descOf(t, code, verifier)

	// A live code presented by another client: must not confirm the code exists.
	env.advance(-(oauthCodeTTL + time.Second))
	other := env.registerDCR(t, "DescOther "+uuid.New().String()[:8], redirect)
	code, verifier = fresh()
	_, oerr = env.svc.ExchangeCode(ctx, other.ClientID, redirect, code, verifier)
	requireOAuthCode(t, oerr, "invalid_grant")
	got["live code, wrong client"] = oerr.Description

	for name, desc := range got {
		assert.Equal(t, oauthInvalidCodeDescription, desc, name)
	}
	assert.Equal(t, "invalid or expired authorization code", oauthInvalidCodeDescription)
}
