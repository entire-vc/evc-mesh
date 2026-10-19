package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Runs the user_ui_state migration's Down and Up sections against a real
// database: up -> down -> up must leave the table in place and the Down must
// really remove it. Skipped when no Postgres is reachable.
func TestUserUIStateMigration_UpDownUp(t *testing.T) {
	db := pmFKTestDB(t)

	raw, err := os.ReadFile("../../../migrations/20261018001_user_ui_state.sql")
	require.NoError(t, err)
	body := string(raw)
	upStart := strings.Index(body, "-- +goose Up")
	downStart := strings.Index(body, "-- +goose Down")
	require.True(t, upStart >= 0 && downStart > upStart)
	up, down := body[upStart:downStart], body[downStart:]

	// Run in a throwaway schema on one pinned connection, so the real
	// user_ui_state table (and any state saved in it) is never touched. `users`
	// still resolves to the public schema through the search path.
	ctx := context.Background()
	schema := "uis_mig_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
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

	exists := func() bool {
		var n int
		require.NoError(t, conn.GetContext(ctx, &n,
			`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'user_ui_state'`, schema))
		return n == 1
	}
	publicExists := func() bool {
		var n int
		require.NoError(t, conn.GetContext(ctx, &n,
			`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'user_ui_state'`))
		return n == 1
	}
	run := func(sql string) {
		_, err := conn.ExecContext(ctx, sql)
		require.NoError(t, err)
	}

	// Never run Down first: while the throwaway schema is empty, an
	// unqualified DROP TABLE would resolve to public.user_ui_state.
	publicBefore := publicExists()
	require.False(t, exists())
	run(up)
	require.True(t, exists(), "Up must create the table")
	run(down)
	require.False(t, exists(), "Down must remove the table")
	run(up)
	require.True(t, exists())
	require.Equal(t, publicBefore, publicExists(), "the shared public table must be untouched")
}
