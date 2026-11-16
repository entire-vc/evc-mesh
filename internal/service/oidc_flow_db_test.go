package service

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the service boundary against real Postgres. Handler tests also
// exercise this flow, but their coverage profile instruments only handler.
func TestOIDCService_IdentityLifecycle(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	clientID := env.staticClient(t)
	keys, err := NewOIDCKeySet(newTestRSAKey(t, 2048))
	require.NoError(t, err)
	env.svc.oidcKeys = keys
	userID, _ := env.createUser(t, "oidc")
	verifier, challenge := svcPKCE()
	p := AuthorizeParams{
		ClientID: clientID, RedirectURI: "https://keep.example/cb", ResponseType: "code",
		CodeChallenge: challenge, CodeChallengeMethod: "S256", Scope: "openid", Nonce: "identity-nonce", State: "identity-state",
	}
	info, oerr := env.svc.ConsentInfo(ctx, userID, p)
	require.Nil(t, oerr)
	assert.True(t, info.IdentityOnly)
	assert.Empty(t, info.Workspaces)
	assert.Equal(t, "keep.example", info.RedirectHost)
	assert.Same(t, keys, env.svc.OIDCKeys())

	issue := func(t *testing.T) string {
		t.Helper()
		redirect, decideErr := env.svc.Decide(ctx, ConsentDecisionInput{AuthorizeParams: p, UserID: userID, Allow: true})
		require.NoError(t, decideErr)
		u, parseErr := url.Parse(redirect)
		require.NoError(t, parseErr)
		assert.Equal(t, p.State, u.Query().Get("state"))
		code := u.Query().Get("code")
		require.True(t, strings.HasPrefix(code, oidcIdentityCodePrefix))
		return code
	}

	code := issue(t)
	// A failed PKCE or redirect check must not consume a valid code.
	_, oerr = env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, strings.Repeat("x", 64))
	requireOAuthCode(t, oerr, "invalid_grant")
	_, oerr = env.svc.ExchangeCode(ctx, clientID, "https://other.example/cb", code, verifier)
	requireOAuthCode(t, oerr, "invalid_grant")
	tokens, oerr := env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, verifier)
	require.Nil(t, oerr)
	require.NotNil(t, tokens)
	assert.Equal(t, "openid", tokens.Scope)
	assert.Empty(t, tokens.RefreshToken)
	assert.True(t, strings.HasPrefix(tokens.AccessToken, OIDCIdentityAccessTokenPrefix))
	assert.Equal(t, 300, tokens.ExpiresIn)
	parsed, err := jwt.Parse(tokens.IDToken, func(token *jwt.Token) (interface{}, error) {
		assert.Equal(t, keys.ActiveKID(), token.Header["kid"])
		return keys.PublicKey(token.Header["kid"].(string)), nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("https://mesh.example"), jwt.WithAudience(clientID))
	require.NoError(t, err)
	assert.True(t, parsed.Valid)
	claims := parsed.Claims.(jwt.MapClaims)
	assert.Equal(t, userID.String(), claims["sub"])
	assert.Equal(t, p.Nonce, claims["nonce"])
	assert.NotContains(t, claims, "email")
	_, oerr = env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, verifier)
	requireOAuthCode(t, oerr, "invalid_grant")
	_, oerr = env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, "mic_unknown", verifier)
	requireOAuthCode(t, oerr, "invalid_grant")

	t.Run("expired code", func(t *testing.T) {
		code := issue(t)
		env.advance(2 * time.Minute)
		defer env.advance(-2 * time.Minute)
		_, oerr := env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
	})
	t.Run("inactive user", func(t *testing.T) {
		code := issue(t)
		_, err := env.db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, userID)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = env.db.Exec(`UPDATE users SET is_active=true WHERE id=$1`, userID) })
		_, oerr := env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
	})
	t.Run("client removed from config", func(t *testing.T) {
		code := issue(t)
		delete(env.svc.oidcClients, clientID)
		defer func() { env.svc.oidcClients[clientID] = true }()
		_, oerr := env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, verifier)
		requireOAuthCode(t, oerr, "invalid_client")
		v := env.svc.ValidateAuthorize(ctx, p)
		requireOAuthCode(t, v.ClientErr, "invalid_client")
	})
	t.Run("provider disabled", func(t *testing.T) {
		code := issue(t)
		env.svc.oidcKeys = nil
		defer func() { env.svc.oidcKeys = keys }()
		_, oerr := env.svc.ExchangeCode(ctx, clientID, p.RedirectURI, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
		v := env.svc.ValidateAuthorize(ctx, p)
		requireOAuthCode(t, v.RequestErr, "invalid_scope")
	})
	for _, tc := range []struct{ scope, nonce, redirect, want string }{
		{"openid mesh", "", p.RedirectURI, "invalid_scope"},
		{"openid profile", "", p.RedirectURI, "invalid_scope"},
		{"openid email", "", p.RedirectURI, "invalid_scope"},
		{"openid offline_access", "", p.RedirectURI, "invalid_scope"},
		{"openid unknown", "", p.RedirectURI, "invalid_scope"},
		{"mesh", "", p.RedirectURI, "invalid_scope"},
		{"openid", strings.Repeat("n", oidcNonceMaxLen+1), p.RedirectURI, "invalid_request"},
	} {
		t.Run(tc.want+tc.scope, func(t *testing.T) {
			bad := p
			bad.Scope, bad.Nonce, bad.RedirectURI = tc.scope, tc.nonce, tc.redirect
			v := env.svc.ValidateAuthorize(ctx, bad)
			requireOAuthCode(t, v.RequestErr, tc.want)
		})
	}
}

func TestOIDCService_StaticClientCannotTakeOverDCR(t *testing.T) {
	env := newOAuthSvcEnv(t)
	c := env.registerDCR(t, "Existing DCR", "https://app.example/cb")
	err := env.svc.ConfigureOIDC(context.Background(), OIDCSettings{
		Clients: []OIDCStaticClient{{ClientID: c.ClientID, ClientName: "Replacement", RedirectURIs: []string{"https://other.example/cb"}}},
	})
	require.ErrorContains(t, err, "already registered")
	got, oerr := env.svc.ResolveClient(context.Background(), c.ClientID)
	require.Nil(t, oerr)
	assert.Equal(t, c.ClientName, got.ClientName)
	assert.Equal(t, c.RedirectURIs, got.RedirectURIs)
}
