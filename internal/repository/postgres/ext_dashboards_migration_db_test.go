//go:build integration

package postgres

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// dashboardsMigration is the migration this file pins; dashboardsPrev is the
// one right before it on main when it was written.
const (
	dashboardsMigration int64 = 20261114100
	dashboardsPrev      int64 = 20261113600
)

var dashboardsTables = []string{
	"ext_dashboards_dashboards",
	"ext_dashboards_placements",
	"ext_dashboards_widgets",
}

func dashboardsLeftovers(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	return map[string][]string{
		"tables":      agentChatStrings(t, db, `SELECT tablename FROM pg_tables WHERE tablename LIKE 'ext\_dashboards\_%' ORDER BY 1`),
		"indexes":     agentChatStrings(t, db, `SELECT indexname FROM pg_indexes WHERE tablename LIKE 'ext\_dashboards\_%' ORDER BY 1`),
		"types":       agentChatStrings(t, db, `SELECT typname FROM pg_type WHERE typname LIKE '%ext\_dashboards\_%' ORDER BY 1`),
		"constraints": agentChatStrings(t, db, `SELECT conname FROM pg_constraint WHERE conname LIKE '%ext\_dashboards\_%' ORDER BY 1`),
		"policies":    agentChatStrings(t, db, `SELECT policyname FROM pg_policies WHERE tablename LIKE 'ext\_dashboards\_%' ORDER BY 1`),
	}
}

// TestDashboardsMigrationUpDownUp: down leaves nothing of the extension behind,
// the second up brings back the three tables with RLS on each.
func TestDashboardsMigrationUpDownUp(t *testing.T) {
	db := testDB(t).DB
	const migrationsDir = "../../../migrations"

	require.GreaterOrEqual(t, gooseVersion(t, db), dashboardsMigration, "the suite DB must be migrated past the dashboards migration")

	require.NoError(t, goose.DownTo(db, migrationsDir, dashboardsPrev))
	t.Cleanup(func() { require.NoError(t, goose.Up(db, migrationsDir)) })
	require.Equal(t, dashboardsPrev, gooseVersion(t, db))
	for kind, names := range dashboardsLeftovers(t, db) {
		require.Empty(t, names, "down left %s behind", kind)
	}

	require.NoError(t, goose.Up(db, migrationsDir))
	left := dashboardsLeftovers(t, db)
	require.Equal(t, dashboardsTables, left["tables"])
	require.Subset(t, left["indexes"], []string{
		"ext_dashboards_dashboards_owner", "ext_dashboards_dashboards_scope",
		"ext_dashboards_placements_user", "ext_dashboards_widgets_dashboard",
	})
	require.Len(t, left["policies"], len(dashboardsTables), "one RLS policy per table")
	var rlsOn int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_class WHERE relname LIKE 'ext\_dashboards\_%' AND relkind='r' AND relrowsecurity`).Scan(&rlsOn))
	require.Equal(t, len(dashboardsTables), rlsOn)
}
