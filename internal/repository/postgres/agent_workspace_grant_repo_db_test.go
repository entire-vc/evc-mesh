package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here (agentDigestTestDB, reused below, is defined in
// agent_api_key_sha256_db_test.go).
//
// The service-level tests (internal/service/agent_service_grant_auth_test.go)
// prove the auth branching logic against a mock. These prove the half a mock
// cannot: that GetByWorkspaceAndPrefix really does see a revoked row (not nil)
// rather than the partial index silently hiding it, and that the lookup is
// truly scoped by workspace_id rather than by an accident of the mock's
// linear scan.

// seedGrantWorkspace creates an owner + workspace and returns the workspace ID.
func seedGrantWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{
		ID: uuid.New(), Email: "grants-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Grants Owner", Username: "grants-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "grants-ws", Slug: "grants-ws-" + suffix, OwnerID: owner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	return ws.ID
}

// seedGrantAgent creates an agent whose home workspace is wsID.
func seedGrantAgent(t *testing.T, db *sqlx.DB, wsID uuid.UUID) *domain.Agent {
	t.Helper()
	suffix := uuid.New().String()[:8]
	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: wsID, Name: "Grant Agent " + suffix, Slug: "grant-agent-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, APIKeyHash: "$2a$12$hash-" + suffix,
		APIKeyPrefix: "pfx-" + suffix, Status: domain.AgentStatusOffline, Role: "developer",
	}
	require.NoError(t, NewAgentRepo(db).Create(context.Background(), agent))
	return agent
}

// insertGrant writes a row directly (no Create method exists on this
// repository yet — U2's scope is the auth read path; U3 adds writes for the
// invite/revoke flow), mirroring migration 20260909001's DDL exactly.
func insertGrant(t *testing.T, db *sqlx.DB, agentID, wsID uuid.UUID, role, prefix, hash string, revokedAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	const q = `
		INSERT INTO agent_workspace_grants
			(id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, created_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7)
	`
	_, err := db.ExecContext(context.Background(), q, id, agentID, wsID, role, prefix, hash, revokedAt)
	require.NoError(t, err)
	return id
}

func TestAgentWorkspaceGrantRepo_GetByWorkspaceAndPrefix_ActiveRow(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	grantID := insertGrant(t, db, agent.ID, wsID, "admin", "activepfx1", "$2a$12$active-hash", nil)

	got, err := NewAgentWorkspaceGrantRepo(db).GetByWorkspaceAndPrefix(context.Background(), wsID, "activepfx1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, grantID, got.ID)
	assert.Equal(t, agent.ID, got.AgentID)
	assert.Equal(t, wsID, got.WorkspaceID)
	assert.Equal(t, "admin", got.Role)
	assert.Equal(t, "$2a$12$active-hash", got.APIKeyHash)
	assert.False(t, got.IsRevoked())
	assert.Nil(t, got.RevokedAt)
}

// This is the AC4 property at the SQL level: a revoked row must still come
// back (not nil), so the service layer can deny outright instead of treating
// it the same as "no connection ever existed" and falling back.
func TestAgentWorkspaceGrantRepo_GetByWorkspaceAndPrefix_RevokedRowIsReturnedNotHidden(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	revokedAt := time.Now().Add(-time.Minute)
	grantID := insertGrant(t, db, agent.ID, wsID, "member", "revokedpfx1", "$2a$12$revoked-hash", &revokedAt)

	got, err := NewAgentWorkspaceGrantRepo(db).GetByWorkspaceAndPrefix(context.Background(), wsID, "revokedpfx1")
	require.NoError(t, err)
	require.NotNil(t, got, "a revoked connection must still be returned — nil would be indistinguishable from \"never connected\"")
	assert.Equal(t, grantID, got.ID)
	assert.True(t, got.IsRevoked())
	require.NotNil(t, got.RevokedAt)
}

func TestAgentWorkspaceGrantRepo_GetByWorkspaceAndPrefix_NoRowAtAll(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)

	got, err := NewAgentWorkspaceGrantRepo(db).GetByWorkspaceAndPrefix(context.Background(), wsID, "nosuchprefix")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// IsRevoked backs cachedAgentAuth's per-hit freshness re-check
// (internal/service/agent_auth_cache.go) — the fix for AC4's cache-vs-revoke
// gap independent review found. These three cover the same states
// GetByWorkspaceAndPrefix does above, but through the PK-keyed query that
// re-check actually calls.
func TestAgentWorkspaceGrantRepo_IsRevoked_ActiveRow(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	grantID := insertGrant(t, db, agent.ID, wsID, "member", "isrevactive1", "$2a$12$active-hash", nil)

	revoked, err := NewAgentWorkspaceGrantRepo(db).IsRevoked(context.Background(), grantID)
	require.NoError(t, err)
	assert.False(t, revoked)
}

func TestAgentWorkspaceGrantRepo_IsRevoked_RevokedRow(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	revokedAt := time.Now().Add(-time.Minute)
	grantID := insertGrant(t, db, agent.ID, wsID, "member", "isrevrevoked1", "$2a$12$revoked-hash", &revokedAt)

	revoked, err := NewAgentWorkspaceGrantRepo(db).IsRevoked(context.Background(), grantID)
	require.NoError(t, err)
	assert.True(t, revoked)
}

// A grant ID that does not exist at all — deleted, or a caller error — reads
// as revoked. Fail-closed: there is no valid state where a live cache entry
// points at a row that has vanished.
func TestAgentWorkspaceGrantRepo_IsRevoked_NoRowAtAll(t *testing.T) {
	db := agentDigestTestDB(t)

	revoked, err := NewAgentWorkspaceGrantRepo(db).IsRevoked(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.True(t, revoked, "a vanished grant must read as revoked, not as valid")
}

// AC5 at the SQL level: the same prefix under a DIFFERENT workspace does not
// resolve, even though it resolves under the right one.
func TestAgentWorkspaceGrantRepo_GetByWorkspaceAndPrefix_ScopedByWorkspace(t *testing.T) {
	db := agentDigestTestDB(t)
	wsA := seedGrantWorkspace(t, db)
	wsB := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsA)
	insertGrant(t, db, agent.ID, wsA, "member", "scopedpfx1", "$2a$12$scoped-hash", nil)

	repo := NewAgentWorkspaceGrantRepo(db)

	inA, err := repo.GetByWorkspaceAndPrefix(context.Background(), wsA, "scopedpfx1")
	require.NoError(t, err)
	require.NotNil(t, inA)

	inB, err := repo.GetByWorkspaceAndPrefix(context.Background(), wsB, "scopedpfx1")
	require.NoError(t, err)
	assert.Nil(t, inB, "the same prefix under a different workspace must not resolve")
}
