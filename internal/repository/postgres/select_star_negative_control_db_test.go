package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here.
//
// TestSelectStarBreaksOnAdditiveMigration_LiveDB is the negative control for
// the whole class of bug this package's readers were rewritten to avoid: it
// reproduces, against a real Postgres, the EXACT production failure mode of
// a `SELECT *` reader running against a binary compiled before an additive
// migration landed — see agentSelectCols in agent_repo.go, which first
// documented this for migration 20260817092 (api_key_sha256): sqlx's strict
// struct-scan (no Unsafe() anywhere in internal/) refuses to scan a column
// with no matching struct field, so ANY binary compiled before an additive
// `ALTER TABLE ADD COLUMN` starts failing every `SELECT *` read the instant
// the migration lands — a live production 401 window until redeploy.
//
// Uses a scratch table (dropped on cleanup) rather than any real repository
// table, so the control is independent of this package's current schema and
// keeps proving the failure mode even if individual tables change shape
// later.
func TestSelectStarBreaksOnAdditiveMigration_LiveDB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()

	suffix := strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	table := "select_star_negative_control_" + suffix

	_, err := db.ExecContext(ctx, `CREATE TABLE `+table+` (id integer PRIMARY KEY, name text NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+table)
	})

	_, err = db.ExecContext(ctx, `INSERT INTO `+table+` (id, name) VALUES (1, 'widget')`)
	require.NoError(t, err)

	// widgetRow mirrors the table's ORIGINAL two-column shape — the struct a
	// running binary would hold in memory before an additive migration
	// lands. It is never changed across the three phases below: that's the
	// whole point — a running binary can't recompile itself when a
	// migration lands underneath it.
	type widgetRow struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}

	// 1. struct "as-is" + SELECT * → err=nil. This is the pre-migration
	// baseline: struct and table agree, so `SELECT *` (misleadingly) works.
	var before widgetRow
	err = db.GetContext(ctx, &before, `SELECT * FROM `+table+` WHERE id = 1`)
	require.NoError(t, err, "SELECT * must succeed before the migration — struct and table still agree")
	assert.Equal(t, "widget", before.Name)

	// 2. ALTER TABLE ADD COLUMN (an ordinary additive migration) + the SAME
	// SELECT * against the SAME (unchanged) struct. This is the production
	// failure mode itself: the already-running binary has no idea the new
	// column exists, and sqlx's strict struct-scan refuses to scan a column
	// with no matching struct field rather than silently dropping it.
	_, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN color text`)
	require.NoError(t, err)

	var afterOldStruct widgetRow
	err = db.GetContext(ctx, &afterOldStruct, `SELECT * FROM `+table+` WHERE id = 1`)
	require.Error(t, err, "SELECT * against a struct that predates the migration must fail — this IS the prod outage window")
	assert.Contains(t, err.Error(), "missing destination name",
		"must fail with sqlx's strict-scan error specifically, not some other failure")

	// 3. Explicit column list, same unchanged struct, same post-migration
	// table — unaffected by the new column. This is the fix applied across
	// this package: naming the columns makes an added column invisible to
	// an old reader instead of fatal to it.
	var afterExplicit widgetRow
	err = db.GetContext(ctx, &afterExplicit, `SELECT id, name FROM `+table+` WHERE id = 1`)
	require.NoError(t, err, "an explicit column list must survive the additive migration unaffected — this is the fix")
	assert.Equal(t, "widget", afterExplicit.Name)
}
