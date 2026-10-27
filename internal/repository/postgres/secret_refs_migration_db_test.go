//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The secrets -> secret_refs migration, up -> down -> up with data,
// in a dedicated schema: the secrets migrations it builds on are replayed
// there from their own files, so the test starts from the real 20261026001
// shape, not from a copy of it.
var secretRefsMigrations = []string{
	"20260820111_create_secrets.sql",
	"20261021001_secrets_keep_ref.sql",
	"20261026001_secrets_keep_ref_shape.sql",
	"20261027017_secret_refs_replace_secrets.sql",
}

func TestSecretRefsMigrationUpDownUp(t *testing.T) {
	db := isolatedSecretsSchema(t)
	dir := secretRefsMigrationDir(t)
	ws, agent := uuid.New(), uuid.New()
	_, err := db.Exec(`INSERT INTO workspaces (id) VALUES ($1)`, ws)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (id) VALUES ($1)`, agent)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db.DB, dir, 20261026001))
	current, rotated, pending := uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`
		INSERT INTO secrets (id, workspace_id, scope, name, encrypted_value, value_sha256_prefix, value_length, value_char_class, created_by, keep_ref)
		VALUES ($1, $2, 'workspace', 'EXT_HELLO_SIGNING_SECRET', 'enc:v1:AAAA', 'aaaaaaaa', 64, 'a-z+0-9', $3, 'keep:mesh-secret.workspace.workspace.EXT_HELLO_SIGNING_SECRET.x')`,
		current, ws, uuid.New())
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO secrets (id, workspace_id, agent_id, scope, name, encrypted_value, value_sha256_prefix, value_length, value_char_class, created_by, rotated_at)
		VALUES ($1, $2, $3, 'agent', 'OLD_TOKEN', 'enc:v1:BBBB', 'bbbbbbbb', 10, 'a-z', $4, now())`,
		rotated, ws, agent, uuid.New())
	require.NoError(t, err)

	// Fail closed: a current row with no Keep ref would lose its value.
	_, err = db.Exec(`
		INSERT INTO secrets (id, workspace_id, scope, name, encrypted_value, value_sha256_prefix, value_length, value_char_class, created_by)
		VALUES ($1, $2, 'workspace', 'NOT_MOVED', 'enc:v1:CCCC', 'cccccccc', 5, 'a-z', $3)`, pending, ws, uuid.New())
	require.NoError(t, err)
	err = goose.Up(db.DB, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 current secret(s) without a Keep reference")
	assert.EqualValues(t, 20261026001, gooseVersion(t, db.DB))
	_, err = db.Exec(`DELETE FROM secrets WHERE id = $1`, pending)
	require.NoError(t, err)

	require.NoError(t, goose.Up(db.DB, dir))
	assertSecretRefsShape(t, db, true)
	var ids []uuid.UUID
	require.NoError(t, db.Select(&ids, `SELECT id FROM secret_refs`))
	assert.Equal(t, []uuid.UUID{current}, ids, "only the row Keep holds survives; the rotated builtin version goes with the table")

	require.NoError(t, goose.Down(db.DB, dir))
	assertSecretRefsShape(t, db, false)
	var back struct {
		ID      uuid.UUID `db:"id"`
		KeepRef string    `db:"keep_ref"`
		Enc     string    `db:"encrypted_value"`
	}
	require.NoError(t, db.Get(&back, `SELECT id, keep_ref, encrypted_value FROM secrets`))
	assert.Equal(t, current, back.ID)
	assert.Equal(t, "keep:mesh-secret.workspace.workspace.EXT_HELLO_SIGNING_SECRET.x", back.KeepRef,
		"the previous binary resolves the row through keep_ref")
	assert.Equal(t, "enc:v0:keep-only", back.Enc)
	// The restored trigger is live again.
	_, err = db.Exec(`UPDATE secrets SET encrypted_value = 'plain' WHERE id = $1`, current)
	require.Error(t, err)

	require.NoError(t, goose.Up(db.DB, dir))
	assertSecretRefsShape(t, db, true)
	require.NoError(t, db.Select(&ids, `SELECT id FROM secret_refs`))
	assert.Equal(t, []uuid.UUID{current}, ids)
}

func assertSecretRefsShape(t *testing.T, db *sqlx.DB, up bool) {
	t.Helper()
	var refs, secrets, fn bool
	require.NoError(t, db.Get(&refs, `SELECT to_regclass('secret_refs_migration_test.secret_refs') IS NOT NULL`))
	require.NoError(t, db.Get(&secrets, `SELECT to_regclass('secret_refs_migration_test.secrets') IS NOT NULL`))
	require.NoError(t, db.Get(&fn, `SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.proname = 'secrets_require_encrypted_value' AND n.nspname = 'secret_refs_migration_test')`))
	assert.Equal(t, up, refs, "secret_refs present")
	assert.Equal(t, !up, secrets, "secrets present")
	assert.Equal(t, !up, fn, "encrypted-value trigger function present")
}

func secretRefsMigrationDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range secretRefsMigrations {
		body, err := os.ReadFile(filepath.Join("../../../migrations", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0o600))
	}
	require.NoError(t, goose.SetDialect("postgres"))
	return dir
}

// isolatedSecretsSchema gives the migrations their own schema with the few
// tables they reference, under an advisory lock in case two copies of the
// suite share one PostgreSQL.
func isolatedSecretsSchema(t *testing.T) *sqlx.DB {
	t.Helper()
	lockDB := testDB(t)
	lock, err := lockDB.BeginTxx(context.Background(), nil)
	require.NoError(t, err)
	_, err = lock.Exec(`SELECT pg_advisory_xact_lock(90304261027017)`)
	require.NoError(t, err)
	var db *sqlx.DB
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
		}
		if _, cleanupErr := lockDB.Exec(`DROP SCHEMA IF EXISTS secret_refs_migration_test CASCADE`); cleanupErr != nil {
			t.Errorf("drop isolated schema: %v", cleanupErr)
		}
		if cleanupErr := lock.Rollback(); cleanupErr != nil && cleanupErr != sql.ErrTxDone {
			t.Errorf("release isolated migration test lock: %v", cleanupErr)
		}
	})
	_, err = lockDB.Exec(`DROP SCHEMA IF EXISTS secret_refs_migration_test CASCADE; CREATE SCHEMA secret_refs_migration_test`)
	require.NoError(t, err)

	db = testDB(t)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`SELECT set_config('search_path', 'secret_refs_migration_test', false)`)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TYPE actor_type AS ENUM ('agent', 'user', 'system');
		CREATE TABLE workspaces (id uuid PRIMARY KEY);
		CREATE TABLE projects (id uuid PRIMARY KEY);
		CREATE TABLE agents (id uuid PRIMARY KEY);
		CREATE TABLE goose_db_version (
			id integer PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY,
			version_id bigint NOT NULL,
			is_applied boolean NOT NULL,
			tstamp timestamp NOT NULL DEFAULT now()
		);
		INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true);
	`)
	require.NoError(t, err)
	return db
}
