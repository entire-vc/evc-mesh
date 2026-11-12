package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The repository_access migration on a stand-in agents table in a throwaway
// schema: Up adds two NULL columns without touching existing rows (legacy),
// the shape check refuses half-configured or non-array states, Down drops the
// columns and keeps the rows, and Up is repeatable after Down.
func TestAgentRepositoryAccessMigration_UpDownUp(t *testing.T) {
	db := agentDigestTestDB(t)
	raw, err := os.ReadFile("../../../migrations/20261111300_agent_repository_access.sql")
	require.NoError(t, err)
	body := string(raw)
	upStart, downStart := strings.Index(body, "-- +goose Up"), strings.Index(body, "-- +goose Down")
	require.True(t, upStart >= 0 && downStart > upStart)
	up, down := body[upStart:downStart], body[downStart:]

	ctx := context.Background()
	schema := "agent_repo_access_mig_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	conn, err := db.Connx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close()
	})
	run := func(sql string, args ...any) error {
		_, err := conn.ExecContext(ctx, sql, args...)
		return err
	}
	require.NoError(t, run("CREATE SCHEMA "+schema))
	require.NoError(t, run("SET search_path TO "+schema))
	require.NoError(t, run(`CREATE TABLE agents (id UUID PRIMARY KEY, name TEXT NOT NULL)`))
	existing := uuid.New()
	require.NoError(t, run(`INSERT INTO agents (id, name) VALUES ($1, 'before')`, existing))

	columns := func() int {
		var n int
		require.NoError(t, conn.GetContext(ctx, &n, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = 'agents'
			AND column_name IN ('repository_access', 'repository_access_revision')`, schema))
		return n
	}

	require.NoError(t, run(up))
	require.Equal(t, 2, columns())
	var legacy bool
	require.NoError(t, conn.GetContext(ctx, &legacy, `SELECT repository_access IS NULL AND repository_access_revision IS NULL FROM agents WHERE id = $1`, existing))
	require.True(t, legacy, "an existing agent stays not configured (legacy)")

	require.NoError(t, run(`UPDATE agents SET repository_access = '[]', repository_access_revision = 1 WHERE id = $1`, existing),
		"positive control: a configured deny-all is a valid state")
	for name, bad := range map[string]string{
		"revision without list":  `UPDATE agents SET repository_access = NULL, repository_access_revision = 2 WHERE id = $1`,
		"list without revision":  `UPDATE agents SET repository_access = '[]', repository_access_revision = NULL WHERE id = $1`,
		"revision zero":          `UPDATE agents SET repository_access = '[]', repository_access_revision = 0 WHERE id = $1`,
		"object instead of list": `UPDATE agents SET repository_access = '{}', repository_access_revision = 2 WHERE id = $1`,
	} {
		require.Error(t, run(bad, existing), name)
	}

	require.NoError(t, run(down))
	require.Zero(t, columns(), "Down drops both columns")
	var name string
	require.NoError(t, conn.GetContext(ctx, &name, `SELECT name FROM agents WHERE id = $1`, existing))
	require.Equal(t, "before", name, "Down keeps the agent rows")

	require.NoError(t, run(up))
	require.Equal(t, 2, columns(), "Up is repeatable after Down")
}
