package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// retentionTestDB runs in the default coverage job; without a configured
// database (local unit-only runs) the test is skipped.
func retentionTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for extension retention controls")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPurgeExpiredExtensionData(t *testing.T) {
	db := retentionTestDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	// Literal statements only: no string-built SQL, even in a test.
	counts := map[string]string{
		"ext_rtn_gone_a":      `SELECT count(*) FROM ext_rtn_gone_a`,
		"ext_rtn_gone_notes":  `SELECT count(*) FROM ext_rtn_gone_notes`,
		"ext_rtn_gone_x_deep": `SELECT count(*) FROM ext_rtn_gone_x_deep`,
		"ext_rtn_keep_notes":  `SELECT count(*) FROM ext_rtn_keep_notes`,
		"ext_rtn_fresh_notes": `SELECT count(*) FROM ext_rtn_fresh_notes`,
		"rtn_neighbor":        `SELECT count(*) FROM rtn_neighbor`,
	}
	count := func(table string) int {
		var n int
		require.NoError(t, db.GetContext(ctx, &n, counts[table]))
		return n
	}
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS ext_rtn_gone_notes, ext_rtn_gone_a, ext_rtn_gone_x_deep, ext_rtn_keep_notes, ext_rtn_fresh_notes, rtn_neighbor`)
		db.Exec(`DELETE FROM extension_removals WHERE extension_id ILIKE 'rtn%'`)
		db.Exec(`DELETE FROM installed_extensions WHERE extension_id ILIKE 'rtn%'`)
	})
	// Parent sorts before child and the FK does not cascade: a per-table DELETE in
	// name order would violate it and roll the whole purge back.
	exec(`CREATE TABLE ext_rtn_gone_a (id int PRIMARY KEY)`)
	exec(`CREATE TABLE ext_rtn_gone_notes (id int REFERENCES ext_rtn_gone_a (id))`)
	exec(`INSERT INTO ext_rtn_gone_a VALUES (1), (2)`)
	exec(`CREATE TABLE ext_rtn_gone_x_deep (id int)`)
	exec(`CREATE TABLE ext_rtn_keep_notes (id int)`)
	exec(`CREATE TABLE ext_rtn_fresh_notes (id int)`)
	exec(`CREATE TABLE rtn_neighbor (id int)`)
	exec(`INSERT INTO ext_rtn_gone_notes VALUES (1), (2)`)
	exec(`INSERT INTO ext_rtn_gone_x_deep VALUES (1), (2)`)
	exec(`INSERT INTO ext_rtn_keep_notes VALUES (1), (2)`)
	exec(`INSERT INTO ext_rtn_fresh_notes VALUES (1), (2)`)
	exec(`INSERT INTO rtn_neighbor VALUES (1), (2)`)
	// rtn-gone: removed, retention over. rtn-gone-x shares the ext_rtn_gone_ prefix
	// and stays installed: its table must survive. rtn-keep is removed and then
	// installed again. rtn-fresh is removed but inside retention. rtn_BAD is an id
	// the job must refuse. Removals are recorded by the trigger on installed_extensions.
	exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source) VALUES
		('rtn-gone', '1', '{}', 'repo'), ('rtn-gone-x', '1', '{}', 'repo'), ('rtn-keep', '1', '{}', 'repo'),
		('rtn-fresh', '1', '{}', 'repo'), ('rtn_BAD', '1', '{}', 'repo')`)
	exec(`DELETE FROM installed_extensions WHERE extension_id IN ('rtn-gone', 'rtn-keep', 'rtn-fresh', 'rtn_BAD')`)
	var recorded int
	require.NoError(t, db.GetContext(ctx, &recorded, `SELECT count(*) FROM extension_removals WHERE extension_id IN ('rtn-gone', 'rtn-keep', 'rtn-fresh', 'rtn_BAD')`))
	require.Equal(t, 4, recorded, "the trigger records every removal")
	exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source) VALUES ('rtn-keep', '2', '{}', 'repo')`)
	require.NoError(t, db.GetContext(ctx, &recorded, `SELECT count(*) FROM extension_removals WHERE extension_id = 'rtn-keep'`))
	require.Equal(t, 0, recorded, "a reinstall cancels the pending purge")
	exec(`UPDATE extension_removals SET removed_at = now() - interval '2 days' WHERE extension_id IN ('rtn-gone', 'rtn_BAD')`)
	const retention = 24 * time.Hour

	res, err := PurgeExpiredExtensionData(ctx, db, time.Now(), retention)
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "rtn-gone", res[0].ExtensionID)
	require.Equal(t, []ExtensionCleanupCount{{Table: "ext_rtn_gone_a", Rows: 2}, {Table: "ext_rtn_gone_notes", Rows: 2}}, res[0].Tables)

	require.Equal(t, 0, count("ext_rtn_gone_a"))
	require.Equal(t, 0, count("ext_rtn_gone_notes"))
	require.Equal(t, 2, count("ext_rtn_gone_x_deep"), "longer-prefix extension is not touched")
	require.Equal(t, 2, count("ext_rtn_keep_notes"), "reinstalled extension is not touched")
	require.Equal(t, 2, count("ext_rtn_fresh_notes"), "inside retention")
	require.Equal(t, 2, count("rtn_neighbor"), "neighbour table outside ext_ is not touched")

	var badPurged *time.Time
	require.NoError(t, db.GetContext(ctx, &badPurged, `SELECT purged_at FROM extension_removals WHERE extension_id = 'rtn_BAD'`))
	require.Nil(t, badPurged, "a malformed id is skipped, not purged")

	// The race: a due list was read, then the extension was installed again. The
	// stale id reaches purgeOneExtension and must be refused without deleting.
	exec(`INSERT INTO ext_rtn_keep_notes VALUES (3)`)
	exec(`INSERT INTO extension_removals (extension_id, removed_at) VALUES ('rtn-keep', now() - interval '2 days')`)
	stale, err := purgeOneExtension(ctx, db, "rtn-keep", time.Now(), retention)
	require.NoError(t, err)
	require.Nil(t, stale, "an installed extension is not purged even if its removal record is stale")
	require.Equal(t, 3, count("ext_rtn_keep_notes"))

	// Purge first, install second: an in-flight purge holds the per-id lock, so an
	// install of the same id must wait for it, not slip in between its check and
	// its deletes.
	purgeTx, err := db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	_, err = purgeTx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('ext-purge:' || $1::text))`, "rtn-race")
	require.NoError(t, err)
	installed := make(chan error, 1)
	go func() {
		_, e := db.ExecContext(ctx, `INSERT INTO installed_extensions (extension_id, version, manifest, source) VALUES ('rtn-race', '1', '{}', 'repo')`)
		installed <- e
	}()
	select {
	case e := <-installed:
		t.Fatalf("install did not wait for the in-flight purge (err=%v)", e)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, purgeTx.Commit())
	select {
	case e := <-installed:
		require.NoError(t, e)
	case <-time.After(10 * time.Second):
		t.Fatal("install still blocked after the purge finished")
	}

	again, err := PurgeExpiredExtensionData(ctx, db, time.Now(), retention)
	require.NoError(t, err)
	require.Empty(t, again, "a purged extension is not processed twice")
}

// An install of a longer-prefix extension ("rtp-a-b") that is still in flight when
// the purge of "rtp-a" starts must be seen by the purge: it waits for that
// install to commit and then leaves ext_rtp_a_b_* alone.
func TestPurgeExpiredExtensionData_WaitsForInFlightLongerInstall(t *testing.T) {
	db := retentionTestDB(t)
	ctx := context.Background()
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS ext_rtp_a_notes, ext_rtp_a_b_notes`)
		db.Exec(`DELETE FROM extension_removals WHERE extension_id ILIKE 'rtp%'`)
		db.Exec(`DELETE FROM installed_extensions WHERE extension_id ILIKE 'rtp%'`)
	})
	for _, q := range []string{
		`CREATE TABLE ext_rtp_a_notes (id int)`,
		`CREATE TABLE ext_rtp_a_b_notes (id int)`,
		`INSERT INTO ext_rtp_a_notes VALUES (1)`,
		`INSERT INTO ext_rtp_a_b_notes VALUES (1)`,
		`INSERT INTO extension_removals (extension_id, removed_at) VALUES ('rtp-a', now() - interval '2 days')`,
	} {
		_, err := db.ExecContext(ctx, q)
		require.NoError(t, err)
	}

	install, err := db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = install.Rollback() }()
	_, err = install.ExecContext(ctx, `INSERT INTO installed_extensions (extension_id, version, manifest, source) VALUES ('rtp-a-b', '1', '{}', 'repo')`)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, e := purgeOneExtension(ctx, db, "rtp-a", time.Now(), 24*time.Hour)
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("purge finished while an install was still in flight: %v", e)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, install.Commit())
	require.NoError(t, <-done)

	var own, longer int
	require.NoError(t, db.GetContext(ctx, &own, `SELECT count(*) FROM ext_rtp_a_notes`))
	require.NoError(t, db.GetContext(ctx, &longer, `SELECT count(*) FROM ext_rtp_a_b_notes`))
	require.Equal(t, 0, own, "the purged extension's own table is emptied")
	require.Equal(t, 1, longer, "the table of the extension installed meanwhile is untouched")
}

// An extension removed before the removal log existed has no extension_removals
// row. Its id still shows in the audit journal, so the purge of a shorter-prefix
// extension ("rto-a") must leave ext_rto_a_b_* alone.
func TestPurgeExpiredExtensionData_LegacyRemovedLongerIDFromJournal(t *testing.T) {
	db := retentionTestDB(t)
	ctx := context.Background()
	exec := func(q string) {
		t.Helper()
		_, err := db.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS ext_rto_a_notes, ext_rto_a_b_notes`)
		db.Exec(`DELETE FROM extension_removals WHERE extension_id = 'rto-a'`)
		db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = 'rto-a-b'`)
	})
	exec(`CREATE TABLE ext_rto_a_notes (id int)`)
	exec(`CREATE TABLE ext_rto_a_b_notes (id int)`)
	exec(`INSERT INTO ext_rto_a_notes VALUES (1)`)
	exec(`INSERT INTO ext_rto_a_b_notes VALUES (1)`)
	exec(`INSERT INTO instance_extension_journal (extension_id, action) VALUES ('rto-a-b', 'uninstall')`)
	exec(`INSERT INTO extension_removals (extension_id, removed_at) VALUES ('rto-a', now() - interval '2 days')`)

	_, err := PurgeExpiredExtensionData(ctx, db, time.Now(), 24*time.Hour)
	require.NoError(t, err)

	var shorter, longer int
	require.NoError(t, db.GetContext(ctx, &shorter, `SELECT count(*) FROM ext_rto_a_notes`))
	require.NoError(t, db.GetContext(ctx, &longer, `SELECT count(*) FROM ext_rto_a_b_notes`))
	require.Equal(t, 0, shorter)
	require.Equal(t, 1, longer, "a legacy-removed longer id known only from the journal keeps its tables")
}
