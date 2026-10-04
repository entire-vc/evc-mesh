//go:build integration

package postgres

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Nothing in CI ever ran a goose Down before #5194afd4: the repo-integration
// job applies `goose up` and the whole suite runs on a fully-migrated
// schema, so a migration whose Down is broken or a silent no-op shipped
// green. This test round-trips the NEWEST migration on the same real
// Postgres the suite runs on: down one version, the goose version must
// actually drop; up, the exact version must be restored. For this branch's
// own migration (20261004001) the schema is asserted too — the table must
// be gone after down and back after up (AC3 of #5194afd4: up+down on real
// Postgres in CI). A future branch whose newest migration is something else
// still gets the version round-trip; it should extend the schema block for
// its own table.
func TestNewestMigrationDownUpRoundTrip(t *testing.T) {
	db := testDB(t).DB

	const migrationsDir = "../../../migrations"

	before := gooseVersion(t, db)
	require.NotZero(t, before, "the suite DB must be fully migrated before this test runs")

	require.NoError(t, goose.Down(db, migrationsDir),
		"goose down must revert the newest migration — a broken Down fails here")

	after := gooseVersion(t, db)
	require.Less(t, after, before,
		"down must actually lower the goose version — a no-op Down is the exact failure this test exists to catch")

	ownMigration := before == 20261004001 // this branch's migration
	if ownMigration {
		require.False(t, tableExists(t, db, "closed_followup_roots"),
			"down must drop closed_followup_roots")
	}

	require.NoError(t, goose.Up(db, migrationsDir),
		"goose up must re-apply the reverted migration")
	require.Equal(t, before, gooseVersion(t, db),
		"up must restore the exact pre-down version")

	if ownMigration {
		require.True(t, tableExists(t, db, "closed_followup_roots"),
			"up must recreate closed_followup_roots")
	}
}

// gooseVersion reads the applied version from goose's own bookkeeping table
// (the default name — the app and the CI job both use the goose CLI/library
// defaults, so this cannot drift from what actually ran).
func gooseVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var version int64
	err := db.QueryRow("SELECT COALESCE(max(version_id), 0) FROM goose_db_version").Scan(&version)
	require.NoError(t, err)
	return version
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists)
	require.NoError(t, err)
	return exists
}
