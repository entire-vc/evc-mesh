//go:build integration

package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Migration 20261013005 deletes leftover provider='slack' rows. The generic
// round-trip test only checks goose versions, so this one asserts the data
// effect: a slack row is gone after up, a github row in the same workspace
// survives.
//
// It runs the migration's Up statement on the already-migrated suite DB instead
// of goose.DownTo/Up: rolling back to the version before 20261013005 reverted
// every migration added since (and re-applied them), which took 100s of a 600s
// package budget on the shared runner and held DDL locks the other tests waited
// on, growing with each new migration.
func TestRemoveSlackMigrationDeletesOnlySlackRows(t *testing.T) {
	db := testDB(t).DB
	const slackMigration = 20261013005

	require.GreaterOrEqual(t, gooseVersion(t, db), int64(slackMigration),
		"the suite DB must be fully migrated before this test runs")

	files, err := os.ReadDir("../../../migrations")
	require.NoError(t, err)
	var upSQL string
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), "20261013005_") {
			continue
		}
		raw, err := os.ReadFile("../../../migrations/" + f.Name())
		require.NoError(t, err)
		up := strings.SplitN(string(raw), "-- +goose Up", 2)
		require.Len(t, up, 2, "migration has no goose Up section")
		upSQL = strings.SplitN(up[1], "-- +goose Down", 2)[0]
	}
	require.Contains(t, upSQL, "integration_configs", "migration 20261013005 Up section not found")

	ws := uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'slack removal fixture',$2,$3)`,
		ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })

	for _, p := range []string{"slack", "github"} {
		_, err = db.Exec(`INSERT INTO integration_configs(workspace_id, provider) VALUES($1,$2)`, ws, p)
		require.NoError(t, err)
	}

	_, err = db.Exec(upSQL)
	require.NoError(t, err)

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
