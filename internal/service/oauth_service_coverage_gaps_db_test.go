package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/oautherror"
)

// This file targets the getOrCreateGrant/currentGrant/createGrantOrJoinWinner/
// discardConnectorAgent/connectorRevokedByAdmin error branches that the normal
// happy-path and race-condition test files don't otherwise exercise. Every
// scenario drives the real black-box Decide() entry point; only ONE
// dependency method is ever overridden per test, via the established
// embed-the-real-interface-and-override-one-method pattern (see
// failingRetargetRepo / failingConnectionRepo in
// oauth_service_grant_integrity_db_test.go, reused here directly).

// failingReactivateRepo makes ReactivateGrant fail so getOrCreateGrant's own
// reactivation error branch (668-670) is exercised.
type failingReactivateRepo struct {
	repository.OAuthRepository
}

func (r *failingReactivateRepo) ReactivateGrant(ctx context.Context, id uuid.UUID, now time.Time) error {
	return errors.New("injected: reactivate failed")
}

func TestOAuthSvc_ReactivateFailureSurfacesAsServerError(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	grant := env.tokenRow(t, f.tokens.AccessToken)
	require.NoError(t, env.svc.RevokeMyGrant(ctx, f.user, grant.GrantID))

	cp := *env.svc
	cp.repo = &failingReactivateRepo{OAuthRepository: env.svc.repo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)

	// Recovery still works once the fault is gone — the earlier failure must
	// not have corrupted the grant itself.
	verifier, ch2 := svcPKCE()
	code := env.consent(t, f.client.ClientID, f.redirect, ch2, f.user, f.ws)
	tok, oerr := env.svc.ExchangeCode(ctx, f.client.ClientID, f.redirect, code, verifier)
	require.Nil(t, oerr)
	_, err := env.svc.AuthenticateAccessToken(ctx, tok.AccessToken)
	require.NoError(t, err)
}

// failingAdminRevokedCheckRepo makes HasAdminRevokedConnector fail so
// getOrCreateGrant's own lookup-error branch (693-696) is exercised.
type failingAdminRevokedCheckRepo struct {
	repository.OAuthRepository
}

func (r *failingAdminRevokedCheckRepo) HasAdminRevokedConnector(ctx context.Context, workspaceID, userID uuid.UUID, baseName string) (bool, error) {
	return false, errors.New("injected: admin-revoked lookup failed")
}

func TestOAuthSvc_HasAdminRevokedConnectorFailureRefusesConsent(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	user, _ := env.createUser(t, "hac-fail")
	ws := env.createWorkspace(t, user)
	redirect := "http://127.0.0.1/cb"
	client := env.registerDCR(t, "HAC Fail "+uuid.New().String()[:8], redirect)

	cp := *env.svc
	cp.repo = &failingAdminRevokedCheckRepo{OAuthRepository: env.svc.repo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(client.ClientID, redirect, ch, "st"),
		UserID:          user,
		WorkspaceID:     ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)

	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM oauth_grants WHERE client_id=$1`, client.ClientID))
	assert.Zero(t, n, "no grant may be created when the admin-revoke check itself fails")
}

// failingCreateGrantRepo makes CreateGrant fail with an error that is
// deliberately NOT a uq_oauth_grant conflict, exercising
// createGrantOrJoinWinner's non-conflict branch (764-766).
type failingCreateGrantRepo struct {
	repository.OAuthRepository
}

func (r *failingCreateGrantRepo) CreateGrant(ctx context.Context, g *domain.OAuthGrant) error {
	return errors.New("injected: create grant failed (not a conflict)")
}

func TestOAuthSvc_CreateGrantNonConflictFailureDiscardsAgent(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	user, _ := env.createUser(t, "cg-fail")
	ws := env.createWorkspace(t, user)
	redirect := "http://127.0.0.1/cb"
	client := env.registerDCR(t, "CG Fail "+uuid.New().String()[:8], redirect)

	cp := *env.svc
	cp.repo = &failingCreateGrantRepo{OAuthRepository: env.svc.repo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(client.ClientID, redirect, ch, "st"),
		UserID:          user,
		WorkspaceID:     ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)
	assert.Zero(t, env.liveAgents(t, ws), "a non-conflict CreateGrant failure must not leave an orphan connector agent")
}

// nilAlwaysGrantLookupRepo always answers "no grant exists" from
// GetGrantByUserClientWorkspace. The FIRST call (getOrCreateGrant's own
// existing-grant check) lets a fresh consent proceed into a real INSERT that
// collides with a grant a prior real flow already created for the same
// (user, client, workspace) — a genuine uq_oauth_grant conflict. The SECOND
// call (createGrantOrJoinWinner's winner look-up) then also reports "no
// grant", exercising the winner-vanished branch (771-773).
type nilAlwaysGrantLookupRepo struct {
	repository.OAuthRepository
}

func (r *nilAlwaysGrantLookupRepo) GetGrantByUserClientWorkspace(context.Context, uuid.UUID, string, uuid.UUID) (*domain.OAuthGrant, error) {
	return nil, nil
}

func TestOAuthSvc_DoubleAllowWinnerVanished(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t) // a real grant already exists for (f.user, f.client.ClientID, f.ws)

	cp := *env.svc
	cp.repo = &nilAlwaysGrantLookupRepo{OAuthRepository: env.svc.repo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)
	assert.Equal(t, 1, env.grantCount(t, f.client.ClientID), "the real winner from the genuine conflict must be untouched")
}

// nilFirstThenErrGrantLookupRepo answers "no grant" on the first call (so a
// real uq_oauth_grant conflict is hit for real, same set-up as
// nilAlwaysGrantLookupRepo above) but fails the SECOND call outright,
// exercising createGrantOrJoinWinner's winner-lookup-error branch (768-770).
type nilFirstThenErrGrantLookupRepo struct {
	repository.OAuthRepository
	calls atomic.Int32
}

func (r *nilFirstThenErrGrantLookupRepo) GetGrantByUserClientWorkspace(ctx context.Context, userID uuid.UUID, clientID string, workspaceID uuid.UUID) (*domain.OAuthGrant, error) {
	if r.calls.Add(1) == 1 {
		return nil, nil
	}
	return nil, errors.New("injected: winner lookup failed")
}

func TestOAuthSvc_DoubleAllowWinnerLookupFailure(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)

	cp := *env.svc
	cp.repo = &nilFirstThenErrGrantLookupRepo{OAuthRepository: env.svc.repo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)
	assert.Equal(t, 1, env.grantCount(t, f.client.ClientID), "the real winner from the genuine conflict must be untouched")
}

// retargetLostRaceRepo simulates RetargetGrant losing its compare-and-swap —
// a genuine, error-free outcome (ok=false, err=nil) — so getOrCreateGrant
// falls into currentGrant's own re-read, and lets each test fail a DIFFERENT
// part of that re-read (GetGrantByID itself, or a "grant disappeared" nil
// result) without one obscuring the other.
type retargetLostRaceRepo struct {
	repository.OAuthRepository
	getGrantByIDErr error
	getGrantByIDNil bool
}

func (r *retargetLostRaceRepo) RetargetGrant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) (bool, error) {
	return false, nil
}

func (r *retargetLostRaceRepo) GetGrantByID(ctx context.Context, id uuid.UUID) (*domain.OAuthGrant, error) {
	if r.getGrantByIDErr != nil {
		return nil, r.getGrantByIDErr
	}
	if r.getGrantByIDNil {
		return nil, nil
	}
	return r.OAuthRepository.GetGrantByID(ctx, id)
}

func TestOAuthSvc_RetargetLostRaceRereadFailure(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, env.grantAgentID(t, f.client.ClientID))
	require.NoError(t, err)

	cp := *env.svc
	cp.repo = &retargetLostRaceRepo{OAuthRepository: env.svc.repo, getGrantByIDErr: errors.New("injected: re-read failed")}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)
}

func TestOAuthSvc_RetargetLostRaceGrantVanished(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, env.grantAgentID(t, f.client.ClientID))
	require.NoError(t, err)

	cp := *env.svc
	cp.repo = &retargetLostRaceRepo{OAuthRepository: env.svc.repo, getGrantByIDNil: true}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)
}

// failingDeleteAgentService makes AgentService.Delete fail so
// discardConnectorAgent's best-effort delete-error branch (808-811) is
// exercised.
type failingDeleteAgentService struct {
	AgentService
}

func (a *failingDeleteAgentService) Delete(ctx context.Context, id uuid.UUID) error {
	return errors.New("injected: agent delete failed")
}

func TestOAuthSvc_DiscardConnectorAgentDeleteFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	oldAgent := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
	require.NoError(t, err)
	require.Zero(t, env.liveAgents(t, f.ws), "control: the only connector is dead")

	cp := *env.svc
	cp.repo = &failingRetargetRepo{OAuthRepository: env.svc.repo}
	cp.agentService = &failingDeleteAgentService{AgentService: env.svc.agentService}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr, "the underlying retarget failure must still surface even though clean-up itself failed")
	assert.Equal(t, 1, env.liveAgents(t, f.ws), "a failed best-effort Delete must not silently vanish the orphan agent it could not remove")
}

func TestOAuthSvc_DiscardConnectorAgentConnectionLookupFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	oldAgent := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
	require.NoError(t, err)
	connectionsBefore := env.liveConnections(t, f.ws)

	cp := *env.svc
	cp.repo = &failingRetargetRepo{OAuthRepository: env.svc.repo}
	cp.agentGrantRepo = &failingConnectionRepo{AgentWorkspaceGrantRepository: env.svc.agentGrantRepo, okReads: 0}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	assert.Zero(t, env.liveAgents(t, f.ws), "the orphan agent itself is still deleted even though its connection lookup failed")
	assert.Equal(t, connectionsBefore+1, env.liveConnections(t, f.ws), "the failed lookup leaves the orphan's connection row live, uncut")
}

// failingRevokeConnectionRepo makes Revoke fail so discardConnectorAgent's
// best-effort revoke-error branch (817-819) is exercised, while
// GetByAgentAndWorkspace still delegates through to the real repo so the
// orphan's connection row is genuinely found first.
type failingRevokeConnectionRepo struct {
	repository.AgentWorkspaceGrantRepository
}

func (r *failingRevokeConnectionRepo) Revoke(ctx context.Context, id, workspaceID uuid.UUID) (bool, error) {
	return false, errors.New("injected: revoke failed")
}

func TestOAuthSvc_DiscardConnectorAgentRevokeFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	oldAgent := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
	require.NoError(t, err)
	connectionsBefore := env.liveConnections(t, f.ws)

	cp := *env.svc
	cp.repo = &failingRetargetRepo{OAuthRepository: env.svc.repo}
	cp.agentGrantRepo = &failingRevokeConnectionRepo{AgentWorkspaceGrantRepository: env.svc.agentGrantRepo}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	assert.Zero(t, env.liveAgents(t, f.ws), "the orphan agent itself is still deleted even though cutting its connection failed")
	assert.Equal(t, connectionsBefore+1, env.liveConnections(t, f.ws), "the failed revoke leaves the orphan's connection row live")
}

func TestOAuthSvc_AdminRevokeCheckSkippedWhenConnectionsFeatureDisabled(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	oldAgent := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
	require.NoError(t, err)

	cp := *env.svc
	cp.agentGrantRepo = nil // workspace-connections feature disabled for this call
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.NoError(t, derr, "with the connections feature off, connectorRevokedByAdmin must short-circuit false and let the retarget proceed")
	assert.NotEqual(t, oldAgent, env.grantAgentID(t, f.client.ClientID), "the grant must have been retargeted at a fresh connector")
}

// flakyAgentLookupService answers the FIRST AgentService.GetByID call for
// real (letting the "connector agent was deleted" 404 short-circuit fire
// exactly like the other race tests) and fails the SECOND call — the one
// made from inside connectorRevokedByAdmin itself — with a non-404 error,
// exercising its own propagate-the-error branch (838).
type flakyAgentLookupService struct {
	AgentService
	calls atomic.Int32
}

func (a *flakyAgentLookupService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
	if a.calls.Add(1) == 1 {
		return a.AgentService.GetByID(ctx, id)
	}
	return nil, errors.New("injected: agent store flaked")
}

func TestOAuthSvc_ConnectorRevokedByAdminAgentLookupFailure(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	f := env.fullFlow(t)
	oldAgent := env.grantAgentID(t, f.client.ClientID)
	_, err := env.db.Exec(`UPDATE agents SET deleted_at=NOW() WHERE id=$1`, oldAgent)
	require.NoError(t, err)
	before := env.liveAgents(t, f.ws)

	cp := *env.svc
	cp.agentService = &flakyAgentLookupService{AgentService: env.svc.agentService}
	_, ch := svcPKCE()
	_, derr := cp.Decide(ctx, ConsentDecisionInput{
		AuthorizeParams: authParams(f.client.ClientID, f.redirect, ch, "st"),
		UserID:          f.user,
		WorkspaceID:     f.ws,
		Allow:           true,
	})
	require.Error(t, derr)
	var oe *oautherror.Error
	require.True(t, errors.As(derr, &oe), "got %T %v", derr, derr)
	assert.Equal(t, "server_error", oe.Code)
	assert.Equal(t, before, env.liveAgents(t, f.ws), "an admin-revoke check failure must not register a replacement connector")
	assert.Equal(t, oldAgent, env.grantAgentID(t, f.client.ClientID))
}
