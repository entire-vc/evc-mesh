package postgres

import (
	"math"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Two branches that each add a migration can pick the same next version and
// both merge green, because nothing loaded the migration set until goose up
// ran on main (pipeline 10129, version 20261010001). goose.CollectMigrations
// is the loader `goose up` uses and refuses a duplicate version, so running
// it in the plain unit job fails the second MR before it can merge.
func TestMigrationSetLoadsWithUniqueVersions(t *testing.T) {
	migrations, err := goose.CollectMigrations("../../../migrations", 0, math.MaxInt64)
	require.NoError(t, err, "goose must be able to load the migration set (duplicate version?)")
	require.NotEmpty(t, migrations)

	seen := make(map[int64]string, len(migrations))
	for _, m := range migrations {
		prev, dup := seen[m.Version]
		require.Falsef(t, dup, "version %d used by both %s and %s", m.Version, prev, m.Source)
		seen[m.Version] = m.Source
	}
}
