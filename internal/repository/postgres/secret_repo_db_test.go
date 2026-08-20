//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These tests run against a real Postgres because the guarantee under test
// is a database trigger (migration 20260820111) — the whole point is that
// it holds for writers that never go through this repository, and nothing
// in the Go test suite alone can demonstrate that. Mirrors the shape of
// project_integration_encryption_db_test.go for the same reason.

func newSecretTestWorkspace(t *testing.T) (repo *SecretRepo, wsID uuid.UUID) {
	t.Helper()
	db := testDB(t)
	withEncryptionKey(t, 0x42)
	ws, _, _ := createTestProject(t, db)
	return NewSecretRepo(db), ws.ID
}

func insertSecretDirectSQL(t *testing.T, wsID uuid.UUID, name, value string) error {
	t.Helper()
	db := testDB(t)
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO secrets (workspace_id, scope, name, encrypted_value, value_sha256_prefix, value_length, value_char_class, created_by, created_by_type)
		 VALUES ($1, 'workspace', $2, $3, 'deadbeef', 1, 'a-z', $4, 'agent')`,
		wsID, name, value, uuid.New())
	return err
}

// The regression class this whole feature exists to close: a value written
// straight to Postgres, bypassing the repository, must still be rejected —
// the same class of gap that left 2026-06's project_integrations rows in
// the clear.
func TestSecretRepoDB_TriggerRejectsPlaintextOnDirectInsert(t *testing.T) {
	_, wsID := newSecretTestWorkspace(t)
	err := insertSecretDirectSQL(t, wsID, "DIRECT_SQL_TOKEN", "plaintext-value-not-encrypted")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be encrypted")
}

func TestSecretRepoDB_TriggerAcceptsEncryptedOnDirectInsert(t *testing.T) {
	_, wsID := newSecretTestWorkspace(t)
	err := insertSecretDirectSQL(t, wsID, "DIRECT_SQL_TOKEN", "enc:v1:AAAABBBBCCCC")
	require.NoError(t, err)
}

func TestSecretRepoDB_CreateThenListNeverReturnsAValue(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	actor := uuid.New()

	created, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "HOMER_GH_TOKEN", Value: "ghp_supersecretvalue123",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)
	assert.Equal(t, "HOMER_GH_TOKEN", created.Name)
	// domain.Secret has no field a value could occupy, but assert on the
	// masked metadata to prove Create computed it correctly, not just that
	// the type happens to lack the field.
	assert.NotEmpty(t, created.ValueSHA256Prefix)
	assert.Len(t, created.ValueSHA256Prefix, 8)
	assert.Equal(t, len("ghp_supersecretvalue123"), created.ValueLength)
	assert.Contains(t, created.ValueCharClass, "a-z")
	assert.Contains(t, created.ValueCharClass, "0-9")

	list, err := repo.ListCurrent(context.Background(), wsID, nil, nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, created.ID, list[0].ID)

	// Direct SQL confirms the stored column is genuinely ciphertext, not a
	// masked repository return value that merely LOOKS clean.
	db := testDB(t)
	var stored string
	require.NoError(t, db.GetContext(context.Background(), &stored,
		`SELECT encrypted_value FROM secrets WHERE id = $1`, created.ID))
	assert.NotContains(t, stored, "ghp_supersecretvalue123")
	assert.Regexp(t, `^enc:v1:`, stored)
}

func TestSecretRepoDB_CreateRejectsDuplicateCurrentName(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	actor := uuid.New()
	input := domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "DUP_TOKEN", Value: "first-value",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	}
	_, err := repo.Create(context.Background(), input)
	require.NoError(t, err)

	input.Value = "second-value"
	_, err = repo.Create(context.Background(), input)
	require.Error(t, err)
}

func TestSecretRepoDB_SameNameAllowedAcrossDifferentScopes(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	db := testDB(t)
	agentRepo := NewAgentRepo(db)
	agentID := createTestAgent(t, agentRepo, wsID)
	actor := uuid.New()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "SHARED_NAME", Value: "ws-value",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeAgent, AgentID: &agentID,
		Name: "SHARED_NAME", Value: "agent-value",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err, "same name in a different scope must not collide")

	list, err := repo.ListCurrent(context.Background(), wsID, nil, &agentID)
	require.NoError(t, err)
	assert.Len(t, list, 2, "workspace-scope secret is always in scope, plus the agent-scope one")
}

func TestSecretRepoDB_RotateSupersedesOldRowAndInsertsNew(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	actor := uuid.New()
	created, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "ROTATE_ME", Value: "old-value",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	rotated, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: actor, CreatedByType: domain.ActorTypeAgent})
	require.NoError(t, err)
	assert.NotEqual(t, created.ID, rotated.ID, "rotate must insert a new row, not edit the old one")

	list, err := repo.ListCurrent(context.Background(), wsID, nil, nil)
	require.NoError(t, err)
	require.Len(t, list, 1, "exactly one current row after rotation")
	assert.Equal(t, rotated.ID, list[0].ID)

	// The old row still exists (audit trail), just no longer current.
	db := testDB(t)
	var rotatedAt *time.Time
	require.NoError(t, db.GetContext(context.Background(), &rotatedAt,
		`SELECT rotated_at FROM secrets WHERE id = $1`, created.ID))
	require.NotNil(t, rotatedAt)
}

func TestSecretRepoDB_RotateNonexistentReturnsNotFound(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "NEVER_EXISTED",
		domain.CreateSecretInput{Value: "x", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.Error(t, err)
}

func TestSecretRepoDB_DeleteStampsRotatedAtWithNoReplacement(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	actor := uuid.New()
	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "DELETE_ME", Value: "value",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	err = repo.Delete(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "DELETE_ME", actor, domain.ActorTypeAgent)
	require.NoError(t, err)

	list, err := repo.ListCurrent(context.Background(), wsID, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestSecretRepoDB_ResolveCurrentValues_DecryptsAndFlagsExpired(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	actor := uuid.New()
	past := time.Now().Add(-time.Hour)

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "LIVE_TOKEN", Value: "live-plaintext",
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)
	_, err = repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "EXPIRED_TOKEN", Value: "expired-plaintext", ExpiresAt: &past,
		CreatedBy: actor, CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	resolved, err := repo.ResolveCurrentValues(context.Background(), wsID, nil, nil)
	require.NoError(t, err)
	require.Len(t, resolved, 2)

	byName := map[string]domain.MaterializedSecret{}
	for _, m := range resolved {
		byName[m.Name] = m
	}
	live := byName["LIVE_TOKEN"]
	assert.False(t, live.Expired)
	assert.Equal(t, "live-plaintext", live.Value)

	expired := byName["EXPIRED_TOKEN"]
	assert.True(t, expired.Expired)
	assert.Empty(t, expired.Value, "an expired secret's value must not be decrypted or returned")
}

func TestSecretRepoDB_ScopeMismatchRejectedByRepo(t *testing.T) {
	repo, wsID := newSecretTestWorkspace(t)
	someProjectID := uuid.New()
	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace, ProjectID: &someProjectID,
		Name: "BAD_SCOPE", Value: "x",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err, "workspace scope with a project_id set must be rejected before it ever reaches the DB CHECK")
}
