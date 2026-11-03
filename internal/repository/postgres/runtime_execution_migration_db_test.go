package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Up -> down -> up of the execution observation migration in a throwaway
// schema, so the real tables are never touched. The three tables are
// telemetry only: Down removes them without any other object going with them.
func TestRuntimeExecutionMigration_UpDownUp(t *testing.T) {
	db := pmFKTestDB(t)
	raw, err := os.ReadFile("../../../migrations/20261031200_runtime_execution_observations.sql")
	require.NoError(t, err)
	body := string(raw)
	upStart, downStart := strings.Index(body, "-- +goose Up"), strings.Index(body, "-- +goose Down")
	require.True(t, upStart >= 0 && downStart > upStart)
	up, down := body[upStart:downStart], body[downStart:]

	ctx := context.Background()
	schema := "rex_mig_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	conn, err := db.Connx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close()
	})
	_, err = conn.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "SET search_path TO "+schema+", public")
	require.NoError(t, err)
	// The migration's foreign keys name these parents unqualified, so the
	// throwaway schema gets empty stand-ins that shadow the real tables on
	// search_path. Without them every CREATE/DROP of a referencing table locks
	// the shared public parents and deadlocks (40P01) against DB tests running
	// concurrently on the same CI database.
	for _, parent := range []string{"integration_configs", "projects", "agents", "workspaces"} {
		_, err = conn.ExecContext(ctx, "CREATE TABLE "+schema+"."+parent+" (id UUID PRIMARY KEY)")
		require.NoError(t, err)
	}

	tables := []string{"runtime_execution_instances", "runtime_execution_reports", "runtime_execution_attempts"}
	count := func() int {
		var n int
		require.NoError(t, conn.GetContext(ctx, &n,
			`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = ANY($2)`, schema, "{"+strings.Join(tables, ",")+"}"))
		return n
	}
	run := func(sql string) {
		_, err := conn.ExecContext(ctx, sql)
		require.NoError(t, err)
	}
	require.Zero(t, count(), "the throwaway schema starts empty")
	run(up)
	require.Equal(t, len(tables), count())
	// Ratchet: no foreign key of the migration may point outside the throwaway
	// schema, otherwise the shared-table lock (and the deadlock) comes back.
	var outside int
	require.NoError(t, conn.GetContext(ctx, &outside, `
		SELECT count(*) FROM pg_constraint c
		JOIN pg_class child ON child.oid = c.conrelid
		JOIN pg_namespace cn ON cn.oid = child.relnamespace
		JOIN pg_class parent ON parent.oid = c.confrelid
		JOIN pg_namespace pn ON pn.oid = parent.relnamespace
		WHERE c.contype = 'f' AND cn.nspname = $1 AND pn.nspname <> $1`, schema))
	require.Zero(t, outside, "every foreign key resolves inside the throwaway schema")
	run(down)
	require.Zero(t, count(), "Down removes every table the migration created")
	run(up)
	require.Equal(t, len(tables), count(), "Up is repeatable after Down")
}
