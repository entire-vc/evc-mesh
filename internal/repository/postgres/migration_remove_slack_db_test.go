//go:build integration

package postgres

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Migration 20261013005 deletes leftover provider='slack' rows. The generic
// round-trip test only checks goose versions, so this one asserts the data
// effect: a slack row is gone after up, a github row in the same workspace
// survives. The Down of the migration is a no-op, which lets us seed rows at
// the pre-migration version.
func TestRemoveSlackMigrationDeletesOnlySlackRows(t *testing.T) {
	db := testDB(t).DB
	const migrationsDir = "../../../migrations"
	const slackMigration = 20261013005

	require.GreaterOrEqual(t, gooseVersion(t, db), int64(slackMigration),
		"the suite DB must be fully migrated before this test runs")

	var prev int64
	require.NoError(t, db.QueryRow(
		"SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE version_id < $1 AND is_applied",
		slackMigration).Scan(&prev))
	require.NotZero(t, prev)

	ws := uuid.New()
	_, err := db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'slack removal fixture',$2,$3)`,
		ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })

	require.NoError(t, goose.DownTo(db, migrationsDir, prev))
	t.Cleanup(func() { require.NoError(t, goose.Up(db, migrationsDir)) })

	for _, p := range []string{"slack", "github"} {
		_, err = db.Exec(`INSERT INTO integration_configs(workspace_id, provider) VALUES($1,$2)`, ws, p)
		require.NoError(t, err)
	}

	require.NoError(t, goose.Up(db, migrationsDir))

	var providers []string
	rows, err := db.Query(`SELECT provider FROM integration_configs WHERE workspace_id=$1 ORDER BY provider`, ws)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var p string
		require.NoError(t, rows.Scan(&p))
		providers = append(providers, p)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"github"}, providers)
}
