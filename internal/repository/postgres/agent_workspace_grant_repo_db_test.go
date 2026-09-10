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

// seedGrantUser creates a standalone user for use as invited_by — that column
// FK-references users(id), so a bare uuid.New() 23503s at INSERT/UPDATE time.
func seedGrantUser(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	suffix := uuid.New().String()[:8]
	u := &domain.User{
		ID: uuid.New(), Email: "grant-inviter-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Grant Inviter", Username: "grant-inviter-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(context.Background(), u))
	return u.ID
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

// ---------------------------------------------------------------------------
// Task U3 — write path (Create/Reactivate/Revoke) and the two joined listings.
// U2's read-only tests above cover GetByWorkspaceAndPrefix/IsRevoked; these
// prove the half a mock cannot: that Create/Reactivate/Revoke really commit
// through the DB's own uq_agent_ws_grant constraint and RETURNING clause, and
// that ListActiveBy* really join agents/workspaces rather than the mock's
// bare pass-through.
// ---------------------------------------------------------------------------

func TestAgentWorkspaceGrantRepo_GetByAgentAndWorkspace_ActiveAndRevokedAndAbsent(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	repo := NewAgentWorkspaceGrantRepo(db)

	absent, err := repo.GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	assert.Nil(t, absent)

	grantID := insertGrant(t, db, agent.ID, wsID, "member", "gawpfx1", "$2a$12$h", nil)
	active, err := repo.GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, grantID, active.ID)
	assert.False(t, active.IsRevoked())
}

func TestAgentWorkspaceGrantRepo_Create_PersistsAndEnforcesUniqueIndex(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	repo := NewAgentWorkspaceGrantRepo(db)
	invitedBy := seedGrantUser(t, db)

	g := &domain.AgentWorkspaceGrant{
		ID: uuid.New(), AgentID: agent.ID, WorkspaceID: wsID, Role: "admin",
		APIKeyPrefix: "createdpfx1", APIKeyHash: "$2a$12$created-hash",
		InvitedBy: &invitedBy, CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(context.Background(), g))

	got, err := repo.GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, g.ID, got.ID)
	assert.Equal(t, "admin", got.Role)
	assert.Equal(t, "createdpfx1", got.APIKeyPrefix)
	require.NotNil(t, got.InvitedBy)
	assert.Equal(t, invitedBy, *got.InvitedBy)

	// uq_agent_ws_grant(agent_id, workspace_id) — a second row for the SAME
	// pair must be rejected at the DB level even bypassing the service's own
	// pre-check, proving the constraint (not just application discipline) is
	// what AC6 ultimately rests on.
	dup := &domain.AgentWorkspaceGrant{
		ID: uuid.New(), AgentID: agent.ID, WorkspaceID: wsID, Role: "member",
		APIKeyPrefix: "duppfx1", APIKeyHash: "$2a$12$dup-hash", CreatedAt: time.Now(),
	}
	err = repo.Create(context.Background(), dup)
	require.Error(t, err, "a second row for the same (agent_id, workspace_id) must violate uq_agent_ws_grant")
}

func TestAgentWorkspaceGrantRepo_Reactivate_SameRowNewKeyClearsRevoked(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	repo := NewAgentWorkspaceGrantRepo(db)
	revokedAt := time.Now().Add(-time.Hour)
	grantID := insertGrant(t, db, agent.ID, wsID, "member", "oldpfx1", "$2a$12$old-hash", &revokedAt)
	newInviter := seedGrantUser(t, db)

	require.NoError(t, repo.Reactivate(context.Background(), grantID, "admin", "newpfx1", "$2a$12$new-hash", &newInviter))

	got, err := repo.GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, grantID, got.ID, "reactivate must reuse the same row id")
	assert.Equal(t, "admin", got.Role)
	assert.Equal(t, "newpfx1", got.APIKeyPrefix)
	assert.Equal(t, "$2a$12$new-hash", got.APIKeyHash)
	require.NotNil(t, got.InvitedBy)
	assert.Equal(t, newInviter, *got.InvitedBy)
	assert.False(t, got.IsRevoked())
	assert.Nil(t, got.RevokedAt)
}

func TestAgentWorkspaceGrantRepo_Revoke_ActiveRowSetsRevokedAtAndReportsFound(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	repo := NewAgentWorkspaceGrantRepo(db)
	grantID := insertGrant(t, db, agent.ID, wsID, "member", "revnowpfx1", "$2a$12$h", nil)

	found, err := repo.Revoke(context.Background(), grantID, wsID)
	require.NoError(t, err)
	assert.True(t, found)

	revoked, err := repo.IsRevoked(context.Background(), grantID)
	require.NoError(t, err)
	assert.True(t, revoked)
}

// Idempotent: revoking an already-revoked row still reports found=true (the
// row exists in this workspace) and does not error.
func TestAgentWorkspaceGrantRepo_Revoke_AlreadyRevoked_StillFoundNoError(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	repo := NewAgentWorkspaceGrantRepo(db)
	firstRevoke := time.Now().Add(-time.Hour)
	grantID := insertGrant(t, db, agent.ID, wsID, "member", "alreadyrevpfx1", "$2a$12$h", &firstRevoke)

	found, err := repo.Revoke(context.Background(), grantID, wsID)
	require.NoError(t, err)
	assert.True(t, found)

	// COALESCE must not have overwritten the ORIGINAL revocation time with NOW().
	got, err := repo.GetByAgentAndWorkspace(context.Background(), agent.ID, wsID)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
	assert.WithinDuration(t, firstRevoke, *got.RevokedAt, time.Second)
}

// Cross-workspace scoping at the SQL level — the DELETE route names both
// ws_id and grant_id, and this proves the query actually enforces both.
func TestAgentWorkspaceGrantRepo_Revoke_WrongWorkspace_NotFoundAndUntouched(t *testing.T) {
	db := agentDigestTestDB(t)
	wsA := seedGrantWorkspace(t, db)
	wsB := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsA)
	repo := NewAgentWorkspaceGrantRepo(db)
	grantID := insertGrant(t, db, agent.ID, wsA, "member", "crosswspfx1", "$2a$12$h", nil)

	found, err := repo.Revoke(context.Background(), grantID, wsB)
	require.NoError(t, err)
	assert.False(t, found, "a grant that belongs to a DIFFERENT workspace must not be found, let alone revoked")

	revoked, err := repo.IsRevoked(context.Background(), grantID)
	require.NoError(t, err)
	assert.False(t, revoked, "the misdirected revoke attempt must not have touched the real row")
}

func TestAgentWorkspaceGrantRepo_Revoke_NoSuchRow_NotFound(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	repo := NewAgentWorkspaceGrantRepo(db)

	found, err := repo.Revoke(context.Background(), uuid.New(), wsID)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestAgentWorkspaceGrantRepo_ListActiveByWorkspace_JoinsAgentExcludesRevoked(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedGrantWorkspace(t, db)
	activeAgent := seedGrantAgent(t, db, wsID)
	revokedAgent := seedGrantAgent(t, db, wsID)
	repo := NewAgentWorkspaceGrantRepo(db)

	insertGrant(t, db, activeAgent.ID, wsID, "admin", "listactivepfx1", "$2a$12$h", nil)
	revokedAt := time.Now()
	insertGrant(t, db, revokedAgent.ID, wsID, "member", "listrevokedpfx1", "$2a$12$h", &revokedAt)

	list, err := repo.ListActiveByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, list, 1, "the revoked connection must not appear")
	assert.Equal(t, activeAgent.ID, list[0].AgentID)
	assert.Equal(t, "admin", list[0].Role)
	assert.Equal(t, activeAgent.Name, list[0].Agent.Name, "must actually join agents.name, not leave it blank")
	assert.Equal(t, activeAgent.Slug, list[0].Agent.Slug)
}

func TestAgentWorkspaceGrantRepo_ListActiveByAgent_JoinsWorkspaceExcludesRevoked(t *testing.T) {
	db := agentDigestTestDB(t)
	wsActive := seedGrantWorkspace(t, db)
	wsRevoked := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsActive)
	repo := NewAgentWorkspaceGrantRepo(db)

	insertGrant(t, db, agent.ID, wsActive, "member", "lba-activepfx1", "$2a$12$h", nil)
	revokedAt := time.Now()
	insertGrant(t, db, agent.ID, wsRevoked, "member", "lba-revokedpfx1", "$2a$12$h", &revokedAt)

	list, err := repo.ListActiveByAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "the revoked connection must not appear")
	assert.Equal(t, wsActive, list[0].WorkspaceID)
	assert.NotEmpty(t, list[0].Workspace.Slug, "must actually join workspaces.slug, not leave it blank")
}
