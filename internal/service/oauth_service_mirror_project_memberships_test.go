package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// These are mock-backed (no Postgres) unit tests for registerConnectorAgent's
// defensive branches — the DB-backed happy path already lives in
// TestOAuthSvc_Decide_MirrorsProjectMembershipsOntoConnectorAgent
// (oauth_service_db_test.go). Both cases here are best-effort failure modes
// that are awkward to force through a real database without breaking the
// whole connection, so a mock projectMemberRepo/agentService is the more
// direct proof (task ec0bc566).

// TestOAuthSvc_RegisterConnectorAgent_MirrorFailureIsBestEffort proves a
// broken project-membership lookup is logged and swallowed, never surfaced as
// a registration failure — the new connector agent still comes back usable,
// just without inherited project access this one time.
func TestOAuthSvc_RegisterConnectorAgent_MirrorFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetRegisterResult(&RegisterAgentOutput{Agent: &domain.Agent{ID: agentID}}, nil)

	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetListByWorkspaceAndUserErr(errors.New("connection reset"))

	svc := &oauthService{agentService: agentSvc, projectMemberRepo: memberRepo}

	out, oerr := svc.registerConnectorAgent(context.Background(), uuid.New(), "Broken Lookup App", uuid.New())
	require.Nil(t, oerr, "a mirroring failure must not fail connector-agent registration")
	require.NotNil(t, out)
	assert.Equal(t, agentID, out.Agent.ID)
}

// TestOAuthSvc_RegisterConnectorAgent_MirrorCreateFailureIsBestEffort proves
// a per-membership Create failure (e.g. one project write hits a constraint
// violation) is logged and does not fail connector-agent registration either
// — mirroring is a strict best-effort add-on, not a transaction the whole
// registration rolls back on.
func TestOAuthSvc_RegisterConnectorAgent_MirrorCreateFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetRegisterResult(&RegisterAgentOutput{Agent: &domain.Agent{ID: agentID}}, nil)

	supervisor := uuid.New()
	memberRepo := NewMockProjectMemberRepository()
	memberRepo.members = []*domain.ProjectMember{{ID: uuid.New(), ProjectID: uuid.New(), UserID: &supervisor, Role: "member"}}
	memberRepo.SetCreateErr(errors.New("duplicate key"))

	svc := &oauthService{agentService: agentSvc, projectMemberRepo: memberRepo}

	out, oerr := svc.registerConnectorAgent(context.Background(), uuid.New(), "Broken Create App", supervisor)
	require.Nil(t, oerr, "a per-membership create failure must not fail connector-agent registration")
	require.NotNil(t, out)
	assert.Equal(t, agentID, out.Agent.ID)
}

// TestOAuthSvc_RegisterConnectorAgent_NilProjectMemberRepoIsANoop proves the
// nil guard: an oauthService wired without a projectMemberRepo must still
// register the connector agent, just without mirroring.
func TestOAuthSvc_RegisterConnectorAgent_NilProjectMemberRepoIsANoop(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetRegisterResult(&RegisterAgentOutput{Agent: &domain.Agent{ID: agentID}}, nil)

	svc := &oauthService{agentService: agentSvc}

	out, oerr := svc.registerConnectorAgent(context.Background(), uuid.New(), "No Repo App", uuid.New())
	require.Nil(t, oerr)
	require.NotNil(t, out)
	assert.Equal(t, agentID, out.Agent.ID)
}

// TestOAuthSvc_ReconcileProjectMemberships_CreateFailureIsBestEffort mirrors
// the two mirrorProjectMemberships best-effort tests above, but for
// reconcileProjectMemberships (task cf226500's resync): one project's Create
// failing must not stop the others in the same sweep from landing.
func TestOAuthSvc_ReconcileProjectMemberships_CreateFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetCreateErr(errors.New("duplicate key"))

	svc := &oauthService{projectMemberRepo: memberRepo}

	human := []domain.ProjectMember{
		{ProjectID: uuid.New(), Role: "member"},
		{ProjectID: uuid.New(), Role: "admin"},
	}
	added, removed, updated := svc.reconcileProjectMemberships(context.Background(), agentID, human, nil)
	assert.Zero(t, added, "every Create failed, so nothing should count as added")
	assert.Zero(t, removed)
	assert.Zero(t, updated)
}

// TestOAuthSvc_ReconcileProjectMemberships_AddsRemovesAndUpdatesInOneSweep
// proves reconcileProjectMemberships handles all three drift kinds together:
// a project only the human holds is added, a project only the agent holds is
// removed, and a role that differs between the two is corrected — without
// touching the project where both already agree.
func TestOAuthSvc_ReconcileProjectMemberships_AddsRemovesAndUpdatesInOneSweep(t *testing.T) {
	agentID := uuid.New()
	memberRepo := NewMockProjectMemberRepository()
	svc := &oauthService{projectMemberRepo: memberRepo}

	onlyHuman := uuid.New()
	onlyAgent := uuid.New()
	roleDrift := uuid.New()
	unchanged := uuid.New()

	human := []domain.ProjectMember{
		{ProjectID: onlyHuman, Role: "member"},
		{ProjectID: roleDrift, Role: "member"},
		{ProjectID: unchanged, Role: "admin"},
	}
	agentMemberships := []domain.ProjectMember{
		{ProjectID: onlyAgent, Role: "member"},
		{ProjectID: roleDrift, Role: "admin"},
		{ProjectID: unchanged, Role: "admin"},
	}
	memberRepo.members = []*domain.ProjectMember{
		{ProjectID: onlyAgent, AgentID: &agentID, Role: "member"},
		{ProjectID: roleDrift, AgentID: &agentID, Role: "admin"},
		{ProjectID: unchanged, AgentID: &agentID, Role: "admin"},
	}

	added, removed, updated := svc.reconcileProjectMemberships(context.Background(), agentID, human, agentMemberships)
	assert.Equal(t, 1, added)
	assert.Equal(t, 1, removed)
	assert.Equal(t, 1, updated)
}

// TestOAuthSvc_ReconcileProjectMemberships_UpdateRoleFailureIsBestEffort
// mirrors the Create-failure test above for the role-correction branch: one
// project's UpdateRoleAgent failing must not stop the sweep or be counted as
// a successful update.
func TestOAuthSvc_ReconcileProjectMemberships_UpdateRoleFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetUpdateRoleAgentErr(errors.New("connection reset"))
	svc := &oauthService{projectMemberRepo: memberRepo}

	projectID := uuid.New()
	human := []domain.ProjectMember{{ProjectID: projectID, Role: "admin"}}
	agentMemberships := []domain.ProjectMember{{ProjectID: projectID, Role: "member"}}

	added, removed, updated := svc.reconcileProjectMemberships(context.Background(), agentID, human, agentMemberships)
	assert.Zero(t, updated, "the failed role update must not count as updated")
	assert.Zero(t, added)
	assert.Zero(t, removed)
}

// TestOAuthSvc_ReconcileProjectMemberships_DeleteFailureIsBestEffort mirrors
// the same posture for the removal branch: one project's DeleteAgent failing
// must not stop the sweep or be counted as a successful removal.
func TestOAuthSvc_ReconcileProjectMemberships_DeleteFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetDeleteAgentErr(errors.New("connection reset"))
	svc := &oauthService{projectMemberRepo: memberRepo}

	agentMemberships := []domain.ProjectMember{{ProjectID: uuid.New(), Role: "member"}}

	added, removed, updated := svc.reconcileProjectMemberships(context.Background(), agentID, nil, agentMemberships)
	assert.Zero(t, removed, "the failed delete must not count as removed")
	assert.Zero(t, added)
	assert.Zero(t, updated)
}

// failingListActiveGrantsRepo makes ListActiveGrants fail so
// ResyncConnectorMemberships's own listing-error branch is exercised without
// a real broken database — embeds a nil repository.OAuthRepository since no
// other method of it is ever reached in these tests.
type failingListActiveGrantsRepo struct {
	repository.OAuthRepository
}

func (r *failingListActiveGrantsRepo) ListActiveGrants(_ context.Context) ([]domain.OAuthGrant, error) {
	return nil, errors.New("connection reset")
}

// oneGrantRepo returns a single fixed grant from ListActiveGrants — enough
// to drive ResyncConnectorMemberships's per-grant loop body once without a
// real database.
type oneGrantRepo struct {
	repository.OAuthRepository
	grant domain.OAuthGrant
}

func (r *oneGrantRepo) ListActiveGrants(_ context.Context) ([]domain.OAuthGrant, error) {
	return []domain.OAuthGrant{r.grant}, nil
}

// TestOAuthSvc_ResyncConnectorMemberships_NilProjectMemberRepoIsANoop proves
// the nil guard: an oauthService wired without a projectMemberRepo (e.g. an
// older deployment mid-rollout) must return a zero result and no error,
// never touch s.repo at all.
func TestOAuthSvc_ResyncConnectorMemberships_NilProjectMemberRepoIsANoop(t *testing.T) {
	svc := &oauthService{}
	result, err := svc.ResyncConnectorMemberships(context.Background())
	require.NoError(t, err)
	assert.Zero(t, result)
}

// TestOAuthSvc_ResyncConnectorMemberships_ListActiveGrantsErrorSurfaces
// proves a broken grants listing is returned as a real error, not swallowed
// — unlike the per-grant best-effort branches below, this one fails the
// whole sweep since there is nothing to iterate over.
func TestOAuthSvc_ResyncConnectorMemberships_ListActiveGrantsErrorSurfaces(t *testing.T) {
	svc := &oauthService{repo: &failingListActiveGrantsRepo{}, projectMemberRepo: NewMockProjectMemberRepository()}
	result, err := svc.ResyncConnectorMemberships(context.Background())
	require.Error(t, err)
	assert.Zero(t, result.Agents)
}

// TestOAuthSvc_ResyncConnectorMemberships_ConnectorAgentUsableErrorIsJoined
// proves a broken connector-agent lookup for one grant is joined into the
// sweep's returned error (so it surfaces, e.g. to a monitoring log) rather
// than silently dropping that grant — but does not panic or abort the loop.
func TestOAuthSvc_ResyncConnectorMemberships_ConnectorAgentUsableErrorIsJoined(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetGetByIDResult(nil, errors.New("agent lookup broke"))
	grant := domain.OAuthGrant{ID: uuid.New(), UserID: uuid.New(), WorkspaceID: uuid.New(), AgentID: agentID}

	svc := &oauthService{
		repo:              &oneGrantRepo{grant: grant},
		agentService:      agentSvc,
		projectMemberRepo: NewMockProjectMemberRepository(),
	}
	result, err := svc.ResyncConnectorMemberships(context.Background())
	require.Error(t, err)
	assert.Zero(t, result.Agents, "the grant whose agent lookup failed must not count as inspected")
}

// TestOAuthSvc_ResyncConnectorMemberships_ListSupervisorMembershipsErrorIsJoined
// proves a broken supervisor-membership lookup for one usable grant is
// joined into the sweep's error and that grant is skipped, without aborting
// the rest of the sweep.
func TestOAuthSvc_ResyncConnectorMemberships_ListSupervisorMembershipsErrorIsJoined(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetGetByIDResult(&domain.Agent{ID: agentID}, nil)
	grant := domain.OAuthGrant{ID: uuid.New(), UserID: uuid.New(), WorkspaceID: uuid.New(), AgentID: agentID}

	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetListByWorkspaceAndUserErr(errors.New("connection reset"))

	svc := &oauthService{repo: &oneGrantRepo{grant: grant}, agentService: agentSvc, projectMemberRepo: memberRepo}
	result, err := svc.ResyncConnectorMemberships(context.Background())
	require.Error(t, err)
	assert.Zero(t, result.Agents)
}

// TestOAuthSvc_ResyncConnectorMemberships_ListAgentMembershipsErrorIsJoined
// is ListSupervisorMembershipsErrorIsJoined's sibling for the agent-side
// listing call.
func TestOAuthSvc_ResyncConnectorMemberships_ListAgentMembershipsErrorIsJoined(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetGetByIDResult(&domain.Agent{ID: agentID}, nil)
	grant := domain.OAuthGrant{ID: uuid.New(), UserID: uuid.New(), WorkspaceID: uuid.New(), AgentID: agentID}

	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetListByWorkspaceAndAgentErr(errors.New("connection reset"))

	svc := &oauthService{repo: &oneGrantRepo{grant: grant}, agentService: agentSvc, projectMemberRepo: memberRepo}
	result, err := svc.ResyncConnectorMemberships(context.Background())
	require.Error(t, err)
	assert.Zero(t, result.Agents)
}
