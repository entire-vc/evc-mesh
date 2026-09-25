package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Round-3 review of MR !1008 (deferred list): what an admin's revoke really
// means, and integrity of re-targeting a grant at a new connector agent.
// Real Postgres throughout; every test names the state it would let through.

func (env *oauthSvcEnv) liveAgents(t *testing.T, ws uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM agents WHERE workspace_id=$1 AND deleted_at IS NULL`, ws))
	return n
}

// liveConnections counts non-revoked workspace connections, INCLUDING those of
// soft-deleted agents: a soft-deleted agent whose connection row stays live
// still shows up in the workspace's agent list, and that is precisely what a
// discarded connector must not leave behind.
func (env *oauthSvcEnv) liveConnections(t *testing.T, ws uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n,
		`SELECT count(*) FROM agent_workspace_grants WHERE workspace_id=$1 AND revoked_at IS NULL`, ws))
	return n
}

func (env *oauthSvcEnv) grantCount(t *testing.T, clientID string) int {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_grants WHERE client_id=$1`, clientID))
	return n
}

// consentErr runs Decide(allow) and returns its error.
func (env *oauthSvcEnv) consentErr(f *oauthFlow) (string, error) {
	_, ch := svcPKCE()
	return env.svc.Decide(context.Background(), ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user, WorkspaceID: f.ws, Allow: true,
	})
}

// --- 1: an admin's revoke is a block, not a reset ---------------------------

func TestOAuthSvc_AdminRevokeIsAHardBlock(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	agentID := env.grantAgentID(t, f.client.ClientID)
	before := env.liveAgents(t, f.ws)

	env.revokeAgentConnection(t, agentID, f.ws)

	t.Run("re-consent is refused and registers no new agent", func(t *testing.T) {
		dest, err := env.consentErr(f)
		require.Error(t, err, "consent must not quietly hand out a fresh connector after an admin cut this one off; got redirect %q", dest)
		requireAPIStatus(t, err, http.StatusForbidden)
		assert.Equal(t, agentID, env.grantAgentID(t, f.client.ClientID), "the grant must stay on the revoked agent")
		assert.Equal(t, before, env.liveAgents(t, f.ws), "no replacement connector agent may appear")
		assert.Equal(t, 1, env.grantCount(t, f.client.ClientID))
	})

	t.Run("the old access token stays dead", func(t *testing.T) {
		_, err := env.svc.AuthenticateAccessToken(ctx, f.tokens.AccessToken)
		requireAPIStatus(t, err, http.StatusUnauthorized)
	})

	t.Run("the admin lifts the block by re-inviting the connector — and only then it works again", func(t *testing.T) {
		_, err := env.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=NULL WHERE agent_id=$1 AND workspace_id=$2`, agentID, f.ws)
		require.NoError(t, err)

		verifier, ch := svcPKCE()
		code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws)
		tok, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
		require.Nil(t, oerr)
		a, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, agentID, a.ID, "the same connector, back — not a second agent")
	})
}

// A lookup failure while deciding "was this an admin revoke?" must refuse the
// consent, not fall through to retargeting: an outage must not be a bypass.
func TestOAuthSvc_AdminRevokeCheckFailureRefusesInsteadOfRetargeting(t *testing.T) {
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	agentID := env.grantAgentID(t, f.client.ClientID)
	env.revokeAgentConnection(t, agentID, f.ws)
	before := env.liveAgents(t, f.ws)

	cp := *env.svc
	// The usability check (first read) sees the revoke; the "was it an admin?"
	// check (second read) hits the outage — the one path a blanket failure
	// would never reach, because the first read would fail instead.
	cp.agentGrantRepo = &failingConnectionRepo{AgentWorkspaceGrantRepository: env.svc.agentGrantRepo, okReads: 1}
	_, ch := svcPKCE()
	_, err := cp.Decide(context.Background(), ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"), UserID: f.user, WorkspaceID: f.ws, Allow: true,
	})
	require.Error(t, err)
	assert.Equal(t, agentID, env.grantAgentID(t, f.client.ClientID))
	assert.Equal(t, before, env.liveAgents(t, f.ws))
}

type failingConnectionRepo struct {
	repository.AgentWorkspaceGrantRepository
	okReads int32
	reads   atomic.Int32
}

func (r *failingConnectionRepo) GetByAgentAndWorkspace(ctx context.Context, a, w uuid.UUID) (*domain.AgentWorkspaceGrant, error) {
	if r.reads.Add(1) > r.okReads {
		return nil, errors.New("injected: connection store down")
	}
	return r.AgentWorkspaceGrantRepository.GetByAgentAndWorkspace(ctx, a, w)
}

// --- 2: retargeting kills codes issued before it ----------------------------

func TestOAuthSvc_RetargetInvalidatesEarlierCodes(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	verifier1, ch1 := svcPKCE()
	staleCode := env.consent(t, f.client.ClientID, f.redirect, ch1, f.user, f.ws) // issued, never redeemed

	// The connector agent is deleted (retarget stays the recovery for that).
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, env.grantAgentID(t, f.client.ClientID))
	require.NoError(t, err)

	verifier2, ch2 := svcPKCE()
	freshCode := env.consent(t, f.client.ClientID, f.redirect, ch2, f.user, f.ws)

	_, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, staleCode, verifier1)
	requireOAuthCode(t, oerr, "invalid_grant")

	tok, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, freshCode, verifier2)
	require.Nil(t, oerr, "the code from the re-consent itself must still work")
	_, err = env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
	require.NoError(t, err)
}

// --- 3: retargeting is atomic; a failure leaves no orphan agent -------------

type failingRetargetRepo struct {
	repository.OAuthRepository
}

func (r *failingRetargetRepo) RetargetGrant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) (bool, error) {
	return false, errors.New("injected: retarget failed")
}

func TestOAuthSvc_FailedRetargetLeavesNoOrphanAgent(t *testing.T) {
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	oldAgent := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
	require.NoError(t, err)
	require.Zero(t, env.liveAgents(t, f.ws), "control: the only connector is dead")
	connectionsBefore := env.liveConnections(t, f.ws) // the dead agent's own row, untouched by this test

	cp := *env.svc
	cp.repo = &failingRetargetRepo{OAuthRepository: env.svc.repo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(context.Background(), ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"), UserID: f.user, WorkspaceID: f.ws, Allow: true,
	})
	require.Error(t, derr)

	assert.Zero(t, env.liveAgents(t, f.ws), "the agent registered for the failed retarget must be discarded")
	assert.Equal(t, connectionsBefore, env.liveConnections(t, f.ws), "and must not keep a live workspace connection")
	assert.Equal(t, oldAgent, env.grantAgentID(t, f.client.ClientID), "the grant is untouched")

	// Recovery still works once the fault is gone.
	verifier, ch2 := svcPKCE()
	code := env.consent(t, f.client.ClientID, f.redirect, ch2, f.user, f.ws)
	tok, oerr := env.svc.ExchangeCode(context.Background(), f.client.ClientID, f.redirect, code, verifier)
	require.Nil(t, oerr)
	_, err = env.svc.AuthenticateAccessToken(context.Background(), tok.AccessToken)
	require.NoError(t, err)
}

// --- 4: a double click on Allow --------------------------------------------

// gateRepo holds the first n callers of GetGrantByUserClientWorkspace until all
// n have read, so every one of them sees the same pre-consent state — the
// interleaving a real double click produces, made deterministic.
type gateRepo struct {
	repository.OAuthRepository
	wg    sync.WaitGroup
	calls atomic.Int32
	n     int32
}

func newGateRepo(inner repository.OAuthRepository, n int) *gateRepo {
	g := &gateRepo{OAuthRepository: inner, n: int32(n)}
	g.wg.Add(n)
	return g
}

func (g *gateRepo) GetGrantByUserClientWorkspace(ctx context.Context, u uuid.UUID, c string, w uuid.UUID) (*domain.OAuthGrant, error) {
	grant, err := g.OAuthRepository.GetGrantByUserClientWorkspace(ctx, u, c, w)
	if g.calls.Add(1) <= g.n { // later reads (the loser re-reading the winner) pass straight through
		g.wg.Done()
		done := make(chan struct{})
		go func() { g.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second): // a peer never arrived: fail on the assertions, do not hang
		}
	}
	return grant, err
}

func runConcurrentConsents(t *testing.T, env *oauthSvcEnv, f *oauthFlow, n int) []error {
	t.Helper()
	cp := *env.svc
	cp.repo = newGateRepo(env.svc.repo, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ch := svcPKCE()
			_, errs[i] = cp.Decide(context.Background(), ConsentDecisionInput{
				AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"), UserID: f.user, WorkspaceID: f.ws, Allow: true,
			})
		}(i)
	}
	wg.Wait()
	return errs
}

func TestOAuthSvc_DoubleAllowCreatesOneConnector(t *testing.T) {
	env := newOAuthSvcEnv(t)
	user, username := env.createUser(t, "dbl")
	ws := env.createWorkspace(t, user)
	redirect := "http://127.0.0.1/cb"
	client := env.registerDCR(t, "Double "+uuid.New().String()[:8], redirect)
	f := &oauthFlow{user: user, username: username, ws: ws, client: client, redirect: redirect}

	for i, err := range runConcurrentConsents(t, env, f, 2) {
		assert.NoError(t, err, "consent %d: a double click must not surface a 500", i)
	}
	assert.Equal(t, 1, env.grantCount(t, client.ClientID))
	assert.Equal(t, 1, env.liveAgents(t, ws), "exactly one connector agent — no orphan 'Name (ab12)'")
	assert.Equal(t, 1, env.liveConnections(t, ws), "the discarded agent's connection must be cut too")
}

func TestOAuthSvc_DoubleAllowOnRetargetCreatesOneConnector(t *testing.T) {
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, env.grantAgentID(t, f.client.ClientID))
	require.NoError(t, err)
	connectionsBefore := env.liveConnections(t, f.ws) // the deleted agent's own row

	for i, cerr := range runConcurrentConsents(t, env, f, 2) {
		assert.NoError(t, cerr, "consent %d", i)
	}
	assert.Equal(t, 1, env.grantCount(t, f.client.ClientID))
	assert.Equal(t, 1, env.liveAgents(t, f.ws), "the loser of the retarget race must discard the agent it registered")
	assert.Equal(t, connectionsBefore+1, env.liveConnections(t, f.ws), "one new connection (the winner's); the loser's must be cut")
}

// --- 5: a token minted in the revoke window cannot be resurrected ----------

func TestOAuthSvc_ReactivateDoesNotResurrectAWindowToken(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	grantID := env.tokenRow(t, f.tokens.AccessToken).GrantID

	// The user revokes; an exchange that had already passed its "grant is not
	// revoked" check inserts its pair a moment later, before the tokens sweep.
	require.NoError(t, env.svc.repo.RevokeGrant(ctx, grantID, timeNow()))
	family := uuid.New()
	resp, rows, oerr := env.svc.buildTokenPair(grantID, family, nil)
	require.Nil(t, oerr)
	for _, row := range rows {
		require.NoError(t, env.svc.repo.CreateToken(ctx, row))
	}
	_, err := env.svc.AuthenticateAccessToken(ctx, resp.AccessToken)
	requireAPIStatus(t, err, http.StatusUnauthorized) // control: dead only because the grant is revoked

	// Re-consent reactivates the grant.
	verifier, ch := svcPKCE()
	code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws)
	fresh, exOerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
	require.Nil(t, exOerr)
	_, err = env.svc.AuthenticateAccessToken(ctx, fresh.AccessToken)
	require.NoError(t, err, "control: the re-consent itself works")

	_, err = env.svc.AuthenticateAccessToken(ctx, resp.AccessToken)
	requireAPIStatus(t, err, http.StatusUnauthorized) // the window token must NOT have come back
}

// A code issued before the user revoked must not redeem after they re-consent.
func TestOAuthSvc_RevokeThenReconsentKillsEarlierCode(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	verifier1, ch1 := svcPKCE()
	staleCode := env.consent(t, f.client.ClientID, f.redirect, ch1, f.user, f.ws)
	grantID := env.tokenRow(t, f.tokens.AccessToken).GrantID

	require.NoError(t, env.svc.RevokeMyGrant(ctx, f.user, grantID))
	_, ch2 := svcPKCE()
	env.consent(t, f.client.ClientID, f.redirect, ch2, f.user, f.ws) // reactivates

	_, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, staleCode, verifier1)
	requireOAuthCode(t, oerr, "invalid_grant")
}

// --- review round: the block must not hang on a client_id ------------------

// A DCR client is free to re-register and gets a fresh client_id, so an
// admin's revoke keyed only on (user, client_id) is undone by registering the
// same app again.
func TestOAuthSvc_AdminRevokeSurvivesReRegisteredClient(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	name := f.client.ClientName
	env.revokeAgentConnection(t, env.grantAgentID(t, f.client.ClientID), f.ws)
	agentsBefore := env.liveAgents(t, f.ws)

	again := env.registerDCR(t, name, f.redirect) // same app, fresh client_id
	require.NotEqual(t, f.client.ClientID, again.ClientID)
	_, ch := svcPKCE()
	_, err := env.svc.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(again.ClientID, f.redirect, ch, "st"), UserID: f.user, WorkspaceID: f.ws, Allow: true,
	})
	require.Error(t, err, "a re-registered client must not walk around an admin's revoke")
	requireAPIStatus(t, err, http.StatusForbidden)
	assert.Equal(t, agentsBefore, env.liveAgents(t, f.ws), "and no replacement agent may appear")
	assert.Zero(t, env.grantCount(t, again.ClientID))

	t.Run("a different application is not affected", func(t *testing.T) {
		other := env.registerDCR(t, "Unrelated "+uuid.New().String()[:8], f.redirect)
		env.consent(t, other.ClientID, f.redirect, func() string { _, c := svcPKCE(); return c }(), f.user, f.ws)
	})

	t.Run("re-inviting the connector lifts it for the re-registered client too", func(t *testing.T) {
		_, uerr := env.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=NULL WHERE agent_id=$1 AND workspace_id=$2`,
			env.grantAgentID(t, f.client.ClientID), f.ws)
		require.NoError(t, uerr)
		_, ch2 := svcPKCE()
		_, derr := env.svc.Decide(ctx, ConsentDecisionInput{
			AuthorizeParams: authParams(again.ClientID, f.redirect, ch2, "st"), UserID: f.user, WorkspaceID: f.ws, Allow: true,
		})
		require.NoError(t, derr)
	})
}

// The block has two independent detectors: the grant's own agent (revoked
// connection) and a same-named connector under any client_id. Renaming the
// revoked agent defeats the second, so the first must still hold on its own.
func TestOAuthSvc_AdminRevokeOfRenamedAgentStillBlocksTheSameGrant(t *testing.T) {
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	agentID := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET name = 'renamed-' || id::text WHERE id=$1`, agentID)
	require.NoError(t, err)
	env.revokeAgentConnection(t, agentID, f.ws)
	before := env.liveAgents(t, f.ws)

	_, derr := env.consentErr(f)
	requireAPIStatus(t, derr, http.StatusForbidden)
	assert.Equal(t, before, env.liveAgents(t, f.ws), "no replacement agent may appear")
	assert.Equal(t, agentID, env.grantAgentID(t, f.client.ClientID))
}

// LIKE metacharacters in an application's name must not widen the match. The
// pattern is built from the NEW application's name and matched against the
// names of revoked connectors, so the revoked one is set up as a disambiguated
// "ToolXA — user (ab12)" and the new one is called "Tool_A": unescaped, its
// "_" would match the "X".
func TestOAuthSvc_AdminRevokeMatchIsExactOnMetacharacters(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	user, _ := env.createUser(t, "meta")
	ws := env.createWorkspace(t, user)
	redirect := "http://127.0.0.1/cb"

	revoked := env.registerDCR(t, "ToolXA", redirect)
	_, ch := svcPKCE()
	env.consent(t, revoked.ClientID, redirect, ch, user, ws)
	var agentID uuid.UUID
	require.NoError(t, env.db.Get(&agentID, `SELECT agent_id FROM oauth_grants WHERE client_id=$1`, revoked.ClientID))
	_, err := env.db.Exec(`UPDATE agents SET name = name || ' (ab12)' WHERE id=$1`, agentID)
	require.NoError(t, err)
	env.revokeAgentConnection(t, agentID, ws)

	other := env.registerDCR(t, "Tool_A", redirect)
	_, ch2 := svcPKCE()
	_, derr := env.svc.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(other.ClientID, redirect, ch2, "st"), UserID: user, WorkspaceID: ws, Allow: true,
	})
	require.NoError(t, derr, "an application whose name only matches the revoked one as a LIKE pattern is not blocked")

	t.Run("control: the disambiguated name of the SAME application is blocked", func(t *testing.T) {
		same := env.registerDCR(t, "ToolXA", redirect)
		_, ch3 := svcPKCE()
		_, serr := env.svc.Decide(ctx, ConsentDecisionInput{
			AuthorizeParams: authParams(same.ClientID, redirect, ch3, "st"), UserID: user, WorkspaceID: ws, Allow: true,
		})
		requireAPIStatus(t, serr, http.StatusForbidden)
	})
}

// Two consents that both read a revoked grant must not kill each other's
// credentials: the second reactivation finds the grant already live.
func TestOAuthSvc_SecondReactivationLeavesTheFirstOnesTokensAlone(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	grantID := env.tokenRow(t, f.tokens.AccessToken).GrantID

	require.NoError(t, env.svc.RevokeMyGrant(ctx, f.user, grantID))
	verifier, ch := svcPKCE()
	code := env.consent(t, f.client.ClientID, f.redirect, ch, f.user, f.ws) // consent A reactivates
	tokA, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
	require.Nil(t, oerr)

	// consent B, working from its earlier snapshot, reactivates once more
	require.NoError(t, env.svc.repo.ReactivateGrant(ctx, grantID, timeNow()))

	_, err := env.svc.AuthenticateAccessToken(ctx, tokA.AccessToken)
	require.NoError(t, err, "A's live token must survive B's late reactivation")
}
