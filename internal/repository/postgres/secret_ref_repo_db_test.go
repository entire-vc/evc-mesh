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

// These tests run against a real Postgres: the unique indexes and CHECKs of
// secret_refs (migration 20261027017) are what serialize concurrent creates
// and hold for writers that never go through this repository. Keep is the
// external boundary and is replaced by fakeKeep (secret_ref_repo_sqlmock_test.go).

func newSecretTestWorkspace(t *testing.T) (repo *SecretRefRepo, wsID uuid.UUID) {
	t.Helper()
	repo, wsID, _ = newSecretTestWorkspaceWithKeep(t)
	return repo, wsID
}

func newSecretTestWorkspaceWithKeep(t *testing.T) (*SecretRefRepo, uuid.UUID, *fakeKeep) {
	t.Helper()
	db := testDB(t)
	ws, _, _ := createTestProject(t, db)
	keep := newFakeKeep()
	return NewSecretRefRepo(db, keep), ws.ID, keep
}

// The table has nowhere a value could go: no column but the masked metadata
// and the Keep ref.
func TestSecretRefRepoDB_TableHasNoValueColumn(t *testing.T) {
	db := testDB(t)
	var cols []string
	require.NoError(t, db.SelectContext(context.Background(), &cols,
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'secret_refs' ORDER BY ordinal_position`))
	require.NotEmpty(t, cols)
	assert.NotContains(t, cols, "encrypted_value")
	assert.NotContains(t, cols, "value")
	var old *string
	require.NoError(t, db.GetContext(context.Background(), &old, `SELECT to_regclass('secrets')::text`))
	assert.Nil(t, old, "the secrets table must be gone")
}

func TestSecretRefRepoDB_KeepRefShapeIsEnforced(t *testing.T) {
	_, wsID := newSecretTestWorkspace(t)
	db := testDB(t)
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO secret_refs (workspace_id, scope, name, keep_ref, value_sha256_prefix, value_length, value_char_class, created_by, created_by_type)
		 VALUES ($1, 'workspace', 'DIRECT_SQL', 'enc:v1:AAAA', 'deadbeef', 1, 'a-z', $2, 'agent')`,
		wsID, uuid.New())
	require.Error(t, err, "a ciphertext is not a Keep ref")
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

	// Direct SQL over the whole row: the value is in no column at all.
	db := testDB(t)
	var row string
	require.NoError(t, db.GetContext(context.Background(), &row,
		`SELECT row_to_json(s)::text FROM secret_refs s WHERE id = $1`, created.ID))
	assert.NotContains(t, row, "ghp_supersecretvalue123")
	assert.Contains(t, row, "keep:mesh-secret.workspace.workspace.HOMER_GH_TOKEN.")
}

func TestSecretRepoDB_CreateRejectsDuplicateCurrentName(t *testing.T) {
	repo, wsID, keep := newSecretTestWorkspaceWithKeep(t)
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
	// The refused duplicate never reached Keep: the current value stands.
	for _, v := range keep.puts {
		assert.Equal(t, "first-value", v)
	}
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
		`SELECT rotated_at FROM secret_refs WHERE id = $1`, created.ID))
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

func TestSecretRepoDB_ResolveCurrentValues_ReadsKeepAndFlagsExpired(t *testing.T) {
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
	assert.Empty(t, expired.Value, "an expired secret's value must not be read or returned")
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
