package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestRuntimeQuotaHourlyMigrationUpDownUp(t *testing.T) {
	db := agentDigestTestDB(t)
	db.SetMaxOpenConns(1)
	schema := "quota_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes the locally generated schema identifier with pq; identifiers cannot be bound parameters.
	_, err := db.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Only the same pq-quoted, locally generated test schema identifier is interpolated.
		_, cleanupErr := db.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		require.NoError(t, cleanupErr)
	})
	_, err = db.Exec(`SELECT set_config('search_path', $1, false)`, schema)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE integration_configs (id uuid PRIMARY KEY)`)
	require.NoError(t, err)
	const file = "20261118850_runtime_quota_hourly.sql"
	raw, err := os.ReadFile("../../../migrations/" + file)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), raw, 0o600))
	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, os.DirFS(dir))
	require.NoError(t, err)
	ctx := context.Background()
	present := func(want bool) {
		var actual bool
		require.NoError(t, db.Get(&actual, `SELECT to_regclass($1) IS NOT NULL`, schema+".runtime_quota_hourly"))
		require.Equal(t, want, actual)
	}
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	present(true)
	id := uuid.New()
	_, err = db.Exec(`INSERT INTO integration_configs(id) VALUES ($1)`, id)
	require.NoError(t, err)
	insert := `INSERT INTO runtime_quota_hourly(integration_id,pool_ref,quota_window,hour,window_raw,status_last,controller_ref_last,observed_at_last) VALUES($1,'pool',$2,'2026-11-18 12:00:00Z','7d','unknown','controller',now())`
	_, err = db.Exec(insert, id, "week")
	require.NoError(t, err)
	var used *float64
	require.NoError(t, db.Get(&used, `SELECT used_last FROM runtime_quota_hourly`))
	require.Nil(t, used)
	_, err = db.Exec(insert, id, "bad-window")
	require.Error(t, err, "the database rejects invalid normalized windows")
	_, err = provider.Down(ctx)
	require.NoError(t, err)
	present(false)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	present(true)
	t.Log("runtime_quota_hourly: up (table present, NULL preserved, invalid window rejected) -> down (absent) -> up (present)")
}
