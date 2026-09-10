package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #44f461a9 — agents.* and agent_workspace_grants must not diverge.
// These prove the half a mock cannot: that CreateWithHomeGrant/
// RotateHomeGrantKey really are atomic against real Postgres (a failed
// second statement leaves no partial row from the first), and that the
// fixed rotation actually closes the auth hole — the OLD prefix stops
// resolving through agent_workspace_grants, not just "the agents row looks
// right".

// newHomeGrantAgent builds (but does not persist) an agent domain object
// with realistic key material, ready for CreateWithHomeGrant.
func newHomeGrantAgent(wsID uuid.UUID, suffix string) *domain.Agent {
	return &domain.Agent{
		ID:           uuid.New(),
		WorkspaceID:  wsID,
		Name:         "Home Grant Agent " + suffix,
		Slug:         "home-grant-agent-" + suffix,
		AgentType:    domain.AgentTypeClaudeCode,
		APIKeyHash:   "$2a$12$hash-" + suffix,
		APIKeyPrefix: "pfx-" + suffix,
		Status:       domain.AgentStatusOffline,
		Role:         "developer",
	}
}

func TestAgentRepo_CreateWithHomeGrant_CreatesAgentAndGrantAtomically(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	suffix := uuid.New().String()[:8]
	agent := newHomeGrantAgent(wsID, suffix)

	require.NoError(t, NewAgentRepo(db).CreateWithHomeGrant(context.Background(), agent))

	stored, err := NewAgentRepo(db).GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, agent.APIKeyPrefix, stored.APIKeyPrefix)

	grant, err := NewAgentWorkspaceGrantRepo(db).GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, grant, "CreateWithHomeGrant must leave a home agent_workspace_grants row, not just the agents row")
	assert.Equal(t, "member", grant.Role)
	assert.Equal(t, agent.APIKeyPrefix, grant.APIKeyPrefix, "grant must carry the SAME key material as agents.*")
	assert.Equal(t, agent.APIKeyHash, grant.APIKeyHash)
	assert.Nil(t, grant.InvitedBy)
	assert.Nil(t, grant.RevokedAt)

	// The grant must actually be reachable through the auth lookup path, not
	// just present by direct id lookup.
	viaPrefix, err := NewAgentWorkspaceGrantRepo(db).GetByWorkspaceAndPrefix(context.Background(), wsID, agent.APIKeyPrefix)
	require.NoError(t, err)
	require.NotNil(t, viaPrefix)
	assert.Equal(t, agent.ID, viaPrefix.AgentID)
}

// Atomicity proof: force the AGENT insert to fail (duplicate primary key)
// and confirm the grant insert's effects do not appear either — the two
// writes commit or fail together. If a future change accidentally ran the
// grant insert on r.db instead of the shared tx (or reordered the two so the
// grant insert isn't guarded by the agent insert's error), this test would
// catch it as a stray grant row for an agent whose insert failed.
func TestAgentRepo_CreateWithHomeGrant_FailedAgentInsertLeavesNoOrphanGrant(t *testing.T) {
	db := agentDigestTestDB(t)
	wsA := seedGrantWorkspace(t, db)
	wsB := seedGrantWorkspace(t, db)
	suffix := uuid.New().String()[:8]

	agent := newHomeGrantAgent(wsA, suffix)
	require.NoError(t, NewAgentRepo(db).CreateWithHomeGrant(context.Background(), agent))

	// Re-attempt CreateWithHomeGrant with the SAME agent ID but a DIFFERENT
	// workspace and key material — the agent INSERT must fail on the
	// primary-key collision.
	dup := *agent
	dup.WorkspaceID = wsB
	dup.APIKeyPrefix = "dup-pfx-" + suffix
	dup.APIKeyHash = "$2a$12$dup-hash-" + suffix

	err := NewAgentRepo(db).CreateWithHomeGrant(context.Background(), &dup)
	require.Error(t, err, "duplicate agent id must fail the INSERT")

	// The original agent row must be untouched.
	stored, err := NewAgentRepo(db).GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, agent.APIKeyPrefix, stored.APIKeyPrefix, "the original row must survive the failed second attempt unchanged")

	// No grant row for (agent.ID, wsB) must have been committed — proves the
	// grant INSERT ran inside the SAME transaction as the failed agent
	// INSERT, not after/independent of it.
	grantInB, err := NewAgentWorkspaceGrantRepo(db).GetByAgentAndWorkspace(context.Background(), agent.ID, wsB)
	require.NoError(t, err)
	assert.Nil(t, grantInB, "a failed CreateWithHomeGrant must not leave a partial grant row behind")

	// The original home grant (wsA) must still show the ORIGINAL key
	// material, not the dup attempt's.
	grantInA, err := NewAgentWorkspaceGrantRepo(db).GetByAgentAndWorkspace(context.Background(), agent.ID, wsA)
	require.NoError(t, err)
	require.NotNil(t, grantInA)
	assert.Equal(t, agent.APIKeyPrefix, grantInA.APIKeyPrefix)
}

// This is task #44f461a9's rotation half, reproduced at the repository
// level: after RotateHomeGrantKey, the OLD prefix must no longer resolve
// through agent_workspace_grants — that lookup is exactly what
// agentService.Authenticate runs on every request. Before the fix (plain
// agentRepo.Update), the grant row kept the stale prefix/hash and this old
// lookup kept succeeding forever.
func TestAgentRepo_RotateHomeGrantKey_OldPrefixNoLongerResolvesInGrantsTable(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	suffix := uuid.New().String()[:8]
	agent := newHomeGrantAgent(wsID, suffix)
	require.NoError(t, NewAgentRepo(db).CreateWithHomeGrant(context.Background(), agent))

	oldPrefix := agent.APIKeyPrefix

	rotated := *agent
	rotated.APIKeyPrefix = "rotated-pfx-" + suffix
	rotated.APIKeyHash = "$2a$12$rotated-hash-" + suffix

	require.NoError(t, NewAgentRepo(db).RotateHomeGrantKey(context.Background(), &rotated))

	// agents.* reflects the new key.
	stored, err := NewAgentRepo(db).GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, rotated.APIKeyPrefix, stored.APIKeyPrefix)

	// The grant row reflects the new key too.
	grant, err := NewAgentWorkspaceGrantRepo(db).GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, grant)
	assert.Equal(t, rotated.APIKeyPrefix, grant.APIKeyPrefix)
	assert.Equal(t, rotated.APIKeyHash, grant.APIKeyHash)

	// The money assertion: the OLD prefix must not resolve to anything
	// anymore — this is the exact query agentService.Authenticate runs.
	stale, err := NewAgentWorkspaceGrantRepo(db).GetByWorkspaceAndPrefix(context.Background(), wsID, oldPrefix)
	require.NoError(t, err)
	assert.Nil(t, stale, "the OLD key's prefix must no longer resolve through agent_workspace_grants after rotation")

	// And the NEW prefix does resolve, with the new hash.
	fresh, err := NewAgentWorkspaceGrantRepo(db).GetByWorkspaceAndPrefix(context.Background(), wsID, rotated.APIKeyPrefix)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.Equal(t, rotated.APIKeyHash, fresh.APIKeyHash)
}

// Positive control (Garfield's repro): an agent with NO home grant row at
// all — the legacy, never-backfilled state — must still rotate cleanly
// through the agents-table side, with zero grant rows created as a side
// effect. This already worked before the fix; it must keep working.
func TestAgentRepo_RotateHomeGrantKey_NoGrantRow_NotAnError(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID) // plain Create — no grant row

	rotated := *agent
	rotated.APIKeyPrefix = "legacy-rotated-pfx"
	rotated.APIKeyHash = "$2a$12$legacy-rotated-hash"

	require.NoError(t, NewAgentRepo(db).RotateHomeGrantKey(context.Background(), &rotated))

	stored, err := NewAgentRepo(db).GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "legacy-rotated-pfx", stored.APIKeyPrefix)

	grant, err := NewAgentWorkspaceGrantRepo(db).GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	assert.Nil(t, grant, "RotateHomeGrantKey must not create a grant row where none existed")
}
