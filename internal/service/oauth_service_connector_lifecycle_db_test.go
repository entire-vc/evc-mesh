package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Second-round review of MR !1008: the connector agent's own lifecycle
// (agent-side connection revoked, agent deleted), error text an
// unauthenticated caller can read, and atomic token issuance. All against the
// real schema; each test names the failure it would let through.

// grantAgentID returns the connector agent behind the flow's grant.
func (env *oauthSvcEnv) grantAgentID(t *testing.T, clientID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, env.db.Get(&id, `SELECT agent_id FROM oauth_grants WHERE client_id=$1`, clientID))
	return id
}

// revokeAgentConnection is what an admin does through
// DELETE /workspaces/:ws_id/agent-grants/:grant_id.
func (env *oauthSvcEnv) revokeAgentConnection(t *testing.T, agentID, ws uuid.UUID) {
	t.Helper()
	res, err := env.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=NOW() WHERE agent_id=$1 AND workspace_id=$2`, agentID, ws)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the connector agent must have exactly one home connection row to revoke")
}

func (env *oauthSvcEnv) liveFamilyTokens(t *testing.T, raw string) int {
	t.Helper()
	fam := env.tokenRow(t, raw).FamilyID
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_tokens WHERE family_id=$1 AND revoked_at IS NULL`, fam))
	return n
}

// --- blocker 1: no internals to an unauthenticated caller -------------------

func TestOAuthSvc_CIMDFetchFailureRevealsNothingAboutTheNetwork(t *testing.T) {
	env := newOAuthSvcEnv(t) // default client: the real SSRF-guarded dialer, not a test double

	// localhost:5432 is refused by the dialer ("refusing to dial non-public
	// address ::1"); the .invalid name never resolves ("no such host"). Both
	// raw transport messages are oracles for which internal hosts exist.
	for _, clientID := range []string{
		"https://localhost:5432/x",
		"https://127.0.0.1:5432/x",
		"https://[::1]:5432/x",
		"https://evc-mesh-postgres-1.invalid/x",
		"https://169.254.169.254/latest/meta-data",
	} {
		_, oerr := env.svc.ResolveClient(context.Background(), clientID)
		requireOAuthCode(t, oerr, "invalid_client")
		for _, leak := range []string{"dial", "refusing", "non-public", "::1", "127.0.0.1", "169.254", "lookup", "no such host", "5432", "tcp", "invalid"} {
			assert.NotContains(t, oerr.Description, leak, "client_id %s: description must not carry transport detail", clientID)
		}
	}
}

func TestOAuthSvc_ConsentMembershipFailureRevealsNoDriverText(t *testing.T) {
	env := newOAuthSvcEnv(t)
	dead := closedDB(t)
	owner, _ := env.createUser(t, "own")
	ws := env.createWorkspace(t, owner)
	redirect := "https://leak.example.com/cb"
	c := env.registerDCR(t, "Leak "+uuid.New().String()[:6], redirect)
	_, ch := svcPKCE()

	cp := *env.svc
	cp.workspaceRepo = postgres.NewWorkspaceRepo(dead)
	_, err := cp.Decide(context.Background(), ConsentDecisionInput{
		AuthorizeParams: authParams(c.ClientID, redirect, ch, "s"), UserID: owner, WorkspaceID: ws, Allow: true,
	})
	requireAPIStatus(t, err, http.StatusInternalServerError)
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae))
	assert.Empty(t, ae.Details, "apierror.Wrap would put the driver's text here")
	assert.NotContains(t, ae.Error(), "sql:")
	assert.NotContains(t, ae.Message, "closed")
}

func closedDB(t *testing.T) *sqlx.DB {
	t.Helper()
	live := oauthSvcTestDB(t)
	_ = live
	dead, err := sqlx.Connect("postgres", envDSN())
	require.NoError(t, err)
	require.NoError(t, dead.Close())
	return dead
}

// --- blocker 2: revoking the agent-side connection disconnects the connector

func TestOAuthSvc_AgentConnectionRevokeDisconnectsConnector(t *testing.T) {
	ctx := context.Background()

	t.Run("access token stops authenticating", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.NoError(t, err, "control: works before the revoke")

		env.revokeAgentConnection(t, env.grantAgentID(t, f.client.ClientID), f.ws)
		// The admin's revoke happens outside the OAuth service, so the
		// positive-auth cache cannot be told: the guarantee is "not later than
		// the cache TTL", and the control call above populated the cache.
		env.advance(oauthAuthCacheTTL + time.Second)

		a, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		assert.Nil(t, a)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("refresh is invalid_grant and the whole family dies", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		env.revokeAgentConnection(t, env.grantAgentID(t, f.client.ClientID), f.ws)

		_, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "invalid_grant")
		assert.Zero(t, env.liveFamilyTokens(t, f.tokens.RefreshToken), "no token of that family may stay live")
	})

	t.Run("an already-issued code does not turn into tokens", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		verifier, ch := svcPKCE()
		code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws)
		env.revokeAgentConnection(t, env.grantAgentID(t, f.client.ClientID), f.ws)

		_, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
		requireOAuthCode(t, oerr, "invalid_grant")
	})

	t.Run("a connection-store outage refuses the request but revokes nothing", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		cp := *env.svc
		cp.agentGrantRepo = postgres.NewAgentWorkspaceGrantRepo(closedDB(t))

		_, oerr := cp.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "server_error")
		assert.Equal(t, 2, env.liveFamilyTokens(t, f.tokens.RefreshToken), "an outage must not disconnect every connector")
	})
}

// --- blocker 3: a deleted / cut-off connector can be re-consented ----------

func TestOAuthSvc_ReconsentRecoversDeadConnector(t *testing.T) {
	ctx := context.Background()

	reconsent := func(t *testing.T, env *oauthSvcEnv, f *oauthFlow) *TokenResponse {
		t.Helper()
		verifier, ch := svcPKCE()
		code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws)
		tok, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
		require.Nil(t, oerr)
		return tok
	}

	t.Run("agent soft-deleted by an admin", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		oldAgent := env.grantAgentID(t, f.client.ClientID)
		_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
		require.NoError(t, err)
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		require.Error(t, err, "control: the dead connector really is dead before re-consent")

		tok := reconsent(t, env, f)

		a, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		require.NoError(t, err, "re-consent must produce a working connector, not a silent 401")
		assert.NotEqual(t, oldAgent, a.ID, "a new agent, not the deleted one")
		var grants int
		require.NoError(t, env.db.Get(&grants, `SELECT count(*) FROM oauth_grants WHERE client_id=$1`, f.client.ClientID))
		assert.Equal(t, 1, grants, "the same grant row is retargeted, not duplicated")
		_, err = env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized) // tokens minted for the dead agent must not silently become the new agent's
	})

	t.Run("agent connection revoked by an admin", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		oldAgent := env.grantAgentID(t, f.client.ClientID)
		env.revokeAgentConnection(t, oldAgent, f.ws)

		tok := reconsent(t, env, f)

		a, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		require.NoError(t, err)
		assert.NotEqual(t, oldAgent, a.ID)
	})

	t.Run("a healthy connector is reused, not replaced", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		before := env.grantAgentID(t, f.client.ClientID)
		tok := reconsent(t, env, f)
		a, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, before, a.ID)
	})
}

// --- item 5: token issuance is atomic at the service level -----------------

// failing wraps the real repo and fails one atomic step, so the service's
// behavior on that failure is observable without a fault-injection proxy.
type failingOAuthRepo struct {
	repository.OAuthRepository
	failRotate, failRedeem bool
	// redeemThenLose commits the real redemption and then reports ok=false —
	// exactly what the loser of a redemption race observes: the winner's
	// transaction already committed the family.
	redeemThenLose bool
}

func (r *failingOAuthRepo) RotateRefreshToken(ctx context.Context, id uuid.UUID, now time.Time, tokens []*domain.OAuthToken) (bool, error) {
	if r.failRotate {
		return false, errors.New("injected: rotate failed")
	}
	return r.OAuthRepository.RotateRefreshToken(ctx, id, now, tokens)
}

func (r *failingOAuthRepo) RedeemCode(ctx context.Context, id uuid.UUID, now time.Time, fam uuid.UUID, tokens []*domain.OAuthToken) (bool, error) {
	if r.failRedeem {
		return false, errors.New("injected: redeem failed")
	}
	if r.redeemThenLose {
		if _, err := r.OAuthRepository.RedeemCode(ctx, id, now, fam, tokens); err != nil {
			return false, err
		}
		return false, nil
	}
	return r.OAuthRepository.RedeemCode(ctx, id, now, fam, tokens)
}

func TestOAuthSvc_FailedIssuanceDoesNotBurnTheCredential(t *testing.T) {
	ctx := context.Background()

	t.Run("refresh: the token survives a failed rotation and still rotates", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		cp := *env.svc
		cp.repo = &failingOAuthRepo{OAuthRepository: env.svc.repo, failRotate: true}

		_, oerr := cp.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		requireOAuthCode(t, oerr, "server_error")
		assert.Equal(t, 2, env.liveFamilyTokens(t, f.tokens.RefreshToken), "the family is untouched")

		// The client's retry — the same token — is a normal refresh, not reuse.
		next, oerr := env.svc.RefreshTokenGrant(ctx, f.client.ClientID, f.tokens.RefreshToken)
		require.Nil(t, oerr, "a failed rotation must not turn the client's retry into a reuse verdict")
		_, err := env.svc.AuthenticateAccessToken(ctx, next.AccessToken)
		assert.NoError(t, err)
	})

	t.Run("code: it stays redeemable after a failed redemption", func(t *testing.T) {
		env := newOAuthSvcEnv(t)
		f := env.fullFlow(t)
		verifier, ch := svcPKCE()
		code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws)
		cp := *env.svc
		cp.repo = &failingOAuthRepo{OAuthRepository: env.svc.repo, failRedeem: true}

		_, oerr := cp.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
		requireOAuthCode(t, oerr, "server_error")

		tok, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
		require.Nil(t, oerr, "a code that produced no tokens must still be redeemable")
		_, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		assert.NoError(t, err)
	})
}

// envDSN mirrors oauthSvcTestDB's connection string choice.
func envDSN() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
}

// TestOAuthSvc_LostRedemptionRaceKillsTheWinnersFamily: RFC 6749 §4.1.2 — a
// code presented twice means the code leaked, so what the first redemption
// produced must die. The sequential replay is covered elsewhere; this is the
// race variant, where the loser sees ok=false from RedeemCode.
func TestOAuthSvc_LostRedemptionRaceKillsTheWinnersFamily(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	verifier, ch := svcPKCE()
	code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws)

	cp := *env.svc
	cp.repo = &failingOAuthRepo{OAuthRepository: env.svc.repo, redeemThenLose: true}
	_, oerr := cp.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
	requireOAuthCode(t, oerr, "invalid_grant")

	var fam uuid.UUID
	require.NoError(t, env.db.Get(&fam, `SELECT issued_family_id FROM oauth_authorization_codes WHERE code_hash=$1`, sha256Hex(code)))
	var live, total int
	require.NoError(t, env.db.Get(&total, `SELECT count(*) FROM oauth_tokens WHERE family_id=$1`, fam))
	require.NoError(t, env.db.Get(&live, `SELECT count(*) FROM oauth_tokens WHERE family_id=$1 AND revoked_at IS NULL`, fam))
	require.Equal(t, 2, total, "control: the winner's pair exists")
	assert.Zero(t, live, "a second redemption attempt must revoke what the first one produced")
}
