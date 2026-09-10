package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Task #44f461a9 — agents.* and agent_workspace_grants must not diverge:
// a freshly registered agent gets a home grant atomically with the agent
// row, and rotating an agent's key rotates its home grant's key material in
// the same operation. Before this fix, Register created ONLY the agents row
// (no grant, ever, for anything registered after migration 20260909001) and
// RotateAPIKey updated ONLY the agents row, leaving a backfilled agent's
// home grant authenticating under the OLD key forever.
// ---------------------------------------------------------------------------

// TestAgentService_Register_CreatesHomeGrantAtomically is AC1: registering a
// new agent must make it show up in agent_workspace_grants immediately, with
// the SAME key material as agents.* — not a second, independent key.
func TestAgentService_Register_CreatesHomeGrantAtomically(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Scout",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	grant := agentRepo.HomeGrant(out.Agent.ID)
	require.NotNil(t, grant, "Register must create a home agent_workspace_grants row atomically with the agent — task #44f461a9")
	assert.Equal(t, out.Agent.ID, grant.AgentID)
	assert.Equal(t, ws.ID, grant.WorkspaceID, "home grant's workspace must be the agent's own workspace")
	assert.Equal(t, domain.RoleMember, grant.Role)
	assert.Equal(t, out.Agent.APIKeyPrefix, grant.APIKeyPrefix, "grant must carry the SAME key material as agents.*, not a second key")
	assert.Equal(t, out.Agent.APIKeyHash, grant.APIKeyHash)
	assert.Nil(t, grant.RevokedAt)
	assert.Nil(t, grant.InvitedBy, "a home grant is not an invite — nobody invited this agent into its own workspace")
}

// setupHomeGrantAuthFixture wires an agentService with a REAL grant repo
// mirror, so CreateWithHomeGrant/RotateHomeGrantKey writes are visible to
// Authenticate exactly as they would be against real Postgres (both land in
// the same agent_workspace_grants table there; here the mock mirror plays
// that role — see MockAgentRepository.grantMirror's doc).
func setupHomeGrantAuthFixture() (*agentService, *MockAgentRepository, *domain.Workspace) {
	agentRepo := NewMockAgentRepository()
	grantRepo := NewMockAgentWorkspaceGrantRepository()
	agentRepo.WithGrantRepoMirror(grantRepo)
	activityRepo := NewMockActivityLogRepository()
	wsRepo := NewMockWorkspaceRepository()

	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme Corp", Slug: "acme"}
	wsRepo.items[ws.ID] = ws

	svc := NewAgentService(agentRepo, activityRepo, wsRepo, NewMockUserRepository()).(*agentService)
	svc.SetAgentWorkspaceGrantRepo(grantRepo)

	return svc, agentRepo, ws
}

// TestAgentService_RotateAPIKey_HomeGrant_InvalidatesOldKey is AC4 + its own
// red control: with a home grant present (the state EVERY agent is in once
// Register is fixed, and the state 32/32 live prod agents were already in
// via the U1 backfill), rotating the key must make the OLD key stop
// authenticating. Revert RotateHomeGrantKey's grant-side UPDATE (or call
// plain agentRepo.Update again) and this test fails: Authenticate finds the
// grant row by its STALE prefix and the old key keeps working — exactly
// task #44f461a9's rotation half, reproduced live by Garfield against a real
// Postgres stand.
func TestAgentService_RotateAPIKey_HomeGrant_InvalidatesOldKey(t *testing.T) {
	svc, _, ws := setupHomeGrantAuthFixture()
	ctx := context.Background()

	out, err := svc.Register(ctx, RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Rotate Me",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	oldKey := out.APIKey

	// Baseline: the fresh key authenticates via the grant path (not legacy).
	agent, err := svc.Authenticate(ctx, ws.Slug, oldKey)
	require.NoError(t, err)
	require.NotNil(t, agent.GrantID, "must have authenticated via the grant, not the legacy fallback")

	newKey, err := svc.RotateAPIKey(ctx, out.Agent.ID)
	require.NoError(t, err)
	require.NotEqual(t, oldKey, newKey)

	_, err = svc.Authenticate(ctx, ws.Slug, oldKey)
	requireUnauthorized(t, err)

	got, err := svc.Authenticate(ctx, ws.Slug, newKey)
	require.NoError(t, err)
	assert.Equal(t, out.Agent.ID, got.ID)
}

// TestAgentService_RotateAPIKey_NoHomeGrant_StillInvalidatesOldKey is
// Garfield's positive control: an agent with NO grant row (the legacy state,
// still reachable for any agent that predates a fully-fixed Register or was
// never backfilled) must keep working correctly through the legacy-only
// path — this was never broken, and the fix must not regress it.
func TestAgentService_RotateAPIKey_NoHomeGrant_StillInvalidatesOldKey(t *testing.T) {
	svc, ws := setupAgentServiceNoGrantRepo()
	ctx := context.Background()

	out, err := svc.Register(ctx, RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Legacy Only",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	oldKey := out.APIKey

	newKey, err := svc.RotateAPIKey(ctx, out.Agent.ID)
	require.NoError(t, err)

	_, err = svc.Authenticate(ctx, ws.Slug, oldKey)
	requireUnauthorized(t, err)

	got, err := svc.Authenticate(ctx, ws.Slug, newKey)
	require.NoError(t, err)
	assert.Equal(t, out.Agent.ID, got.ID)
}

// setupAgentServiceNoGrantRepo is setupAgentService, spelled out again here
// because that helper doesn't return a workspace slug-only view — kept
// separate from setupHomeGrantAuthFixture so this test's fixture visibly has
// NO grantRepo wired at all (grantRepo == nil), not merely an unpopulated
// one, matching Authenticate's actual "U2 never happened for this call" path.
func setupAgentServiceNoGrantRepo() (*agentService, *domain.Workspace) {
	svc, _, ws := setupAgentService()
	return svc, ws
}
