package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Task U3 — invite/revoke/list on top of agent_workspace_grants. Covers every
// branch the task's acceptance criteria name: fresh invite, active-conflict,
// revoked-reactivate, immediate revoke, cross-workspace revoke scoping,
// invalid role, unknown agent/workspace, and the two listings.
// ---------------------------------------------------------------------------

type grantSvcFixture struct {
	svc          *agentWorkspaceGrantService
	grantRepo    *MockAgentWorkspaceGrantRepository
	agentRepo    *MockAgentRepository
	wsRepo       *MockWorkspaceRepository
	activityRepo *MockActivityLogRepository
}

func setupGrantSvcFixture() (*grantSvcFixture, *domain.Workspace, *domain.Agent) {
	grantRepo := NewMockAgentWorkspaceGrantRepository()
	agentRepo := NewMockAgentRepository()
	wsRepo := NewMockWorkspaceRepository()
	activityRepo := NewMockActivityLogRepository()

	ws := &domain.Workspace{ID: uuid.New(), Name: "Guest Co", Slug: "guest-co"}
	wsRepo.items[ws.ID] = ws

	homeWS := &domain.Workspace{ID: uuid.New(), Name: "Home Co", Slug: "home-co"}
	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: homeWS.ID, Name: "Roamer", Slug: "roamer",
		AgentType: domain.AgentTypeClaudeCode, APIKeyHash: "x", APIKeyPrefix: "x",
		Status: domain.AgentStatusOffline,
	}
	agentRepo.items[agent.ID] = agent

	svc := NewAgentWorkspaceGrantService(grantRepo, agentRepo, wsRepo, activityRepo).(*agentWorkspaceGrantService)
	return &grantSvcFixture{svc: svc, grantRepo: grantRepo, agentRepo: agentRepo, wsRepo: wsRepo, activityRepo: activityRepo}, ws, agent
}

func TestInviteAgent_NoExistingConnection_CreatesAndReturnsKey(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()
	inviter := uuid.New()

	result, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleAdmin, inviter)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.Reactivated)
	assert.Equal(t, agent.ID, result.Grant.AgentID)
	assert.Equal(t, ws.ID, result.Grant.WorkspaceID)
	assert.Equal(t, domain.RoleAdmin, result.Grant.Role)
	require.NotNil(t, result.Grant.InvitedBy)
	assert.Equal(t, inviter, *result.Grant.InvitedBy)
	assert.NotEmpty(t, result.APIKey)
	assert.Contains(t, result.APIKey, "agk_guest-co_")

	// The stored hash must verify against the returned raw key — proves the
	// same value that went to the caller is what got hashed, not a
	// regenerated or mismatched one.
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(result.Grant.APIKeyHash), []byte(result.APIKey)))

	// The row is actually persisted, not just returned.
	stored, err := f.grantRepo.GetByAgentAndWorkspace(context.Background(), agent.ID, ws.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, result.Grant.ID, stored.ID)
	assert.False(t, stored.IsRevoked())
}

func TestInviteAgent_DefaultsRoleToMember(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()

	result, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, "", uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, domain.RoleMember, result.Grant.Role)
	assert.Nil(t, result.Grant.InvitedBy, "invitedBy=uuid.Nil must not be stored as a pointer to the zero UUID")
}

func TestInviteAgent_InvalidRole_ValidationError(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()

	_, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, "superadmin", uuid.Nil)
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestInviteAgent_UnknownAgent_NotFound(t *testing.T) {
	f, ws, _ := setupGrantSvcFixture()

	_, err := f.svc.InviteAgent(context.Background(), ws.ID, uuid.New(), domain.RoleMember, uuid.Nil)
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestInviteAgent_UnknownWorkspace_NotFound(t *testing.T) {
	f, _, agent := setupGrantSvcFixture()

	_, err := f.svc.InviteAgent(context.Background(), uuid.New(), agent.ID, domain.RoleMember, uuid.Nil)
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// AC6, branch 1: an active connection already exists — refuse, do not
// silently reissue a key over a working connection.
func TestInviteAgent_ActiveConnectionExists_Conflict(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()

	first, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)

	_, err = f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleAdmin, uuid.Nil)
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, 409, apiErr.StatusCode())

	// The original connection must be untouched by the refused attempt.
	stored, err := f.grantRepo.GetByAgentAndWorkspace(context.Background(), agent.ID, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, first.Grant.ID, stored.ID)
	assert.Equal(t, domain.RoleMember, stored.Role)
}

// AC6, branch 2: a REVOKED connection exists — reactivate the same row with
// a fresh key rather than refusing or creating a second row (the unique
// index would reject a second INSERT anyway, but the point is this is the
// one designed path back in, mirroring re-inviting a removed human member).
func TestInviteAgent_RevokedConnectionExists_Reactivates(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()

	first, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)
	firstKey := first.APIKey

	require.NoError(t, f.svc.RevokeGrant(context.Background(), ws.ID, first.Grant.ID))

	second, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleAdmin, uuid.New())
	require.NoError(t, err)

	assert.True(t, second.Reactivated)
	assert.Equal(t, first.Grant.ID, second.Grant.ID, "reactivation must reuse the SAME row id, not create a second one")
	assert.Equal(t, domain.RoleAdmin, second.Grant.Role)
	assert.NotEqual(t, firstKey, second.APIKey, "reactivation must issue a FRESH key, not resurrect the old one")
	assert.Nil(t, second.Grant.RevokedAt)

	// Only one row exists for this (agent, workspace) pair — no duplicate.
	all, err := f.grantRepo.ListActiveByWorkspace(context.Background(), ws.ID)
	require.NoError(t, err)
	assert.Len(t, all, 1)

	// The old key must no longer work — same shape as the AC2 live check,
	// asserted here at the hash level: it does not verify against the new hash.
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(second.Grant.APIKeyHash), []byte(firstKey)))
}

func TestRevokeGrant_SetsRevokedAt(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()
	result, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.RevokeGrant(context.Background(), ws.ID, result.Grant.ID))

	stored, err := f.grantRepo.GetByAgentAndWorkspace(context.Background(), agent.ID, ws.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.True(t, stored.IsRevoked())
}

// Idempotent: revoking an already-revoked grant is still a success, not an error.
func TestRevokeGrant_AlreadyRevoked_StillSucceeds(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()
	result, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.RevokeGrant(context.Background(), ws.ID, result.Grant.ID))
	require.NoError(t, f.svc.RevokeGrant(context.Background(), ws.ID, result.Grant.ID))
}

func TestRevokeGrant_UnknownGrant_NotFound(t *testing.T) {
	f, ws, _ := setupGrantSvcFixture()

	err := f.svc.RevokeGrant(context.Background(), ws.ID, uuid.New())
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A grant_id that is real but belongs to a DIFFERENT workspace must 404, not
// leak its existence and not revoke it — the DELETE route names both ws_id
// and grant_id, and only their combination is authoritative.
func TestRevokeGrant_WrongWorkspace_NotFoundAndUntouched(t *testing.T) {
	f, wsA, agent := setupGrantSvcFixture()
	wsB := &domain.Workspace{ID: uuid.New(), Name: "Other Co", Slug: "other-co"}
	f.wsRepo.items[wsB.ID] = wsB

	result, err := f.svc.InviteAgent(context.Background(), wsA.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)

	err = f.svc.RevokeGrant(context.Background(), wsB.ID, result.Grant.ID)
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, 404, apiErr.StatusCode())

	// The grant under its REAL workspace must be untouched by the misdirected attempt.
	stored, err := f.grantRepo.GetByAgentAndWorkspace(context.Background(), agent.ID, wsA.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.False(t, stored.IsRevoked())
}

func TestListWorkspaceAgents_OnlyActive(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()
	agent2 := &domain.Agent{
		ID: uuid.New(), WorkspaceID: uuid.New(), Name: "Second", Slug: "second",
		AgentType: domain.AgentTypeClaudeCode, APIKeyHash: "x", APIKeyPrefix: "x",
		Status: domain.AgentStatusOffline,
	}
	f.agentRepo.items[agent2.ID] = agent2

	g1, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)
	_, err = f.svc.InviteAgent(context.Background(), ws.ID, agent2.ID, domain.RoleAdmin, uuid.Nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.RevokeGrant(context.Background(), ws.ID, g1.Grant.ID))

	list, err := f.svc.ListWorkspaceAgents(context.Background(), ws.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "the revoked connection must not appear in the listing")
	assert.Equal(t, agent2.ID, list[0].AgentID)
}

func TestListAgentWorkspaces_OnlyActive(t *testing.T) {
	f, wsA, agent := setupGrantSvcFixture()
	wsB := &domain.Workspace{ID: uuid.New(), Name: "Second WS", Slug: "second-ws"}
	f.wsRepo.items[wsB.ID] = wsB

	gA, err := f.svc.InviteAgent(context.Background(), wsA.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err)
	_, err = f.svc.InviteAgent(context.Background(), wsB.ID, agent.ID, domain.RoleAdmin, uuid.Nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.RevokeGrant(context.Background(), wsA.ID, gA.Grant.ID))

	list, err := f.svc.ListAgentWorkspaces(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, wsB.ID, list[0].WorkspaceID)
}
