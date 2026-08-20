//go:build integration

package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// These tests cover the write paths for project_integrations.agent_key against
// a real Postgres, because the guard being tested is a database trigger — the
// whole point of it is that it holds for writers that never touch Go, and
// nothing in the Go test suite can demonstrate that.
//
// Background: on 2026-08-20 all eleven production credentials were sitting in
// the clear despite the repository encrypting on write. Ten of them shared one
// created_at to the microsecond with created_by NULL — a bulk INSERT issued
// directly to Postgres, which the application-layer encryption never saw.

// withEncryptionKey installs a key for the duration of the test and resets the
// package singleton around it, so ordering between tests cannot leak state.
func withEncryptionKey(t *testing.T, fill byte) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill + byte(i)
	}
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(key))
	t.Cleanup(encryption.ResetForTest)
}

func newIntegrationProject(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	_, proj, _ := createTestProject(t, db)
	return proj.ID
}

func insertIntegrationDirectSQL(t *testing.T, db *sqlx.DB, projectID uuid.UUID, agentKey string) error {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO project_integrations (id, project_id, type, enabled, settings, agent_key, created_at, updated_at)
		 VALUES ($1, $2, 'team_relay', true, '{}', $3, NOW(), NOW())`,
		uuid.New(), projectID, agentKey)
	return err
}

// The regression itself: a credential written straight to Postgres, the way
// the ten production rows were, must now be refused.
func TestTriggerRejectsPlaintextAgentKeyOnDirectInsert(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)

	err := insertIntegrationDirectSQL(t, db, projID, "tr_agent_"+strings.Repeat("a", 48))
	require.Error(t, err, "a plaintext credential must not be storable by any writer")
	assert.Contains(t, err.Error(), "must be encrypted")
}

// Positive control: the trigger is not simply refusing everything.
func TestTriggerAcceptsEncryptedAgentKeyOnDirectInsert(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)
	withEncryptionKey(t, 41)

	sealed, err := encryption.Encrypt("tr_agent_" + strings.Repeat("b", 48))
	require.NoError(t, err)
	require.NoError(t, insertIntegrationDirectSQL(t, db, projID, sealed))
}

func TestTriggerRejectsPlaintextAgentKeyOnDirectUpdate(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)
	withEncryptionKey(t, 43)

	sealed, err := encryption.Encrypt("tr_agent_original")
	require.NoError(t, err)
	require.NoError(t, insertIntegrationDirectSQL(t, db, projID, sealed))

	_, err = db.ExecContext(context.Background(),
		`UPDATE project_integrations SET agent_key = 'tr_agent_pasted_by_hand' WHERE project_id = $1`, projID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be encrypted")
}

// A row that predates the backfill must stay writable for everything EXCEPT
// its credential — otherwise shipping the trigger before the backfill would
// break toggling an existing integration off.
func TestTriggerLeavesLegacyPlaintextRowsUpdatable(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)
	ctx := context.Background()

	legacy := "tr_agent_" + strings.Repeat("c", 48)
	tx, err := db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL mesh.allow_plaintext_agent_key = 'on'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO project_integrations (id, project_id, type, enabled, settings, agent_key, created_at, updated_at)
		 VALUES ($1, $2, 'team_relay', true, '{}', $3, NOW(), NOW())`,
		uuid.New(), projID, legacy)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Touching another column leaves agent_key unchanged → allowed.
	_, err = db.ExecContext(ctx,
		`UPDATE project_integrations SET enabled = false WHERE project_id = $1`, projID)
	require.NoError(t, err, "an untouched legacy credential must not block unrelated updates")

	// Rewriting it to plaintext is still refused.
	_, err = db.ExecContext(ctx,
		`UPDATE project_integrations SET agent_key = 'tr_agent_something_else' WHERE project_id = $1`, projID)
	require.Error(t, err)
}

// The exemption must not survive the transaction that granted it, or a pooled
// connection would carry it to the next unrelated writer.
func TestPlaintextExemptionDoesNotLeakAcrossTransactions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	first := newIntegrationProject(t, db)
	tx, err := db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL mesh.allow_plaintext_agent_key = 'on'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO project_integrations (id, project_id, type, enabled, settings, agent_key, created_at, updated_at)
		 VALUES ($1, $2, 'team_relay', true, '{}', 'tr_agent_exempt', NOW(), NOW())`,
		uuid.New(), first)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	second := newIntegrationProject(t, db)
	require.Error(t, insertIntegrationDirectSQL(t, db, second, "tr_agent_should_be_refused"),
		"the exemption is SET LOCAL and must end with its transaction")
}

// End to end through the repository: what lands in the column is ciphertext,
// and what comes back out is the credential.
func TestUpsertStoresCiphertextAndGetReturnsPlaintext(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)
	withEncryptionKey(t, 47)
	repo := NewProjectIntegrationRepo(db)
	ctx := context.Background()

	plaintext := "tr_agent_" + strings.Repeat("d", 48)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.Upsert(ctx, &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: projID,
		Type:      "team_relay",
		Enabled:   true,
		Settings:  json.RawMessage(`{"share_slug":"probe"}`),
		AgentKey:  plaintext,
		CreatedAt: now,
		UpdatedAt: now,
	}))

	// What the database actually holds — read raw, bypassing the repo.
	var stored string
	require.NoError(t, db.GetContext(ctx,
		&stored, `SELECT agent_key FROM project_integrations WHERE project_id = $1`, projID))
	assert.True(t, strings.HasPrefix(stored, "enc:v1:"),
		"the column must hold a version-tagged ciphertext, not the credential")
	assert.NotContains(t, stored, plaintext)

	got, err := repo.Get(ctx, projID, "team_relay")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, plaintext, got.AgentKey, "the credential must survive the round trip")
}

// Preserving an existing credential (agent_key omitted from the request) must
// not trip the trigger — the CASE in the upsert leaves the old value in place,
// and an encrypted old value is fine either way.
func TestUpsertPreservesExistingCredentialWithoutRewriting(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)
	withEncryptionKey(t, 53)
	repo := NewProjectIntegrationRepo(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	plaintext := "tr_agent_keepme"
	require.NoError(t, repo.Upsert(ctx, &domain.ProjectIntegration{
		ID: uuid.New(), ProjectID: projID, Type: "team_relay", Enabled: true,
		Settings: json.RawMessage(`{}`), AgentKey: plaintext, CreatedAt: now, UpdatedAt: now,
	}))

	require.NoError(t, repo.Upsert(ctx, &domain.ProjectIntegration{
		ID: uuid.New(), ProjectID: projID, Type: "team_relay", Enabled: false,
		Settings: json.RawMessage(`{"share_slug":"changed"}`), AgentKey: "", CreatedAt: now, UpdatedAt: now,
	}))

	got, err := repo.Get(ctx, projID, "team_relay")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.Enabled)
	assert.Equal(t, plaintext, got.AgentKey, "omitting agent_key must preserve it, not wipe it")
}

// A deployment with no key configured is a documented self-hosting mode; the
// repository grants the exemption for it so integrations remain configurable.
func TestUpsertWithoutKeyStoresPlaintextViaExemption(t *testing.T) {
	db := testDB(t)
	projID := newIntegrationProject(t, db)

	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, "")
	t.Cleanup(encryption.ResetForTest)

	repo := NewProjectIntegrationRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	plaintext := "tr_agent_no_key_configured"

	require.NoError(t, repo.Upsert(ctx, &domain.ProjectIntegration{
		ID: uuid.New(), ProjectID: projID, Type: "team_relay", Enabled: true,
		Settings: json.RawMessage(`{}`), AgentKey: plaintext, CreatedAt: now, UpdatedAt: now,
	}), "a keyless instance must still be able to configure an integration")

	got, err := repo.Get(ctx, projID, "team_relay")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, plaintext, got.AgentKey)
}
