package postgres

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/simhash"
)

// content_simhash after the clustering fix against the migrated
// test database: the review-triage match needs equal text besides an equal
// hash, the recompute is batched, re-runnable and skips rows changed under
// it, the memory_base_daily dup columns are recounted once on the new hashes,
// and the repair migration restores collision supersedes reversibly.

type simhashFx struct {
	t   *testing.T
	db  *sqlx.DB
	ws  uuid.UUID
	ctx context.Context
}

func newSimhashFx(t *testing.T) *simhashFx {
	db := userRepoTestDB(t)
	fx := newCoverageFixture(t, db)
	f := &simhashFx{t: t, db: db, ws: fx.workspaceID, ctx: context.Background()}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, f.ws) })
	return f
}

// newSimhashIsolatedFx returns a fixture whose unqualified memories and
// memory_simhash_backfill are private copies in a throwaway schema: the
// recompute scans the whole table, so on the shared database it would rewrite
// other tests' rows, and its counts would not be exact.
func newSimhashIsolatedFx(t *testing.T) *simhashFx {
	shared := userRepoTestDB(t)
	schema := "mem_simhash_bf_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	_, err := shared.Exec(`CREATE SCHEMA ` + schema + `;
		CREATE TABLE ` + schema + `.memories (LIKE public.memories INCLUDING DEFAULTS);
		CREATE TABLE ` + schema + `.memory_simhash_backfill (LIKE public.memory_simhash_backfill INCLUDING ALL)`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = shared.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	db, err := sqlx.Connect("postgres", dsnWithSearchPath(t, userRepoTestDSN(), schema))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &simhashFx{t: t, db: db, ws: uuid.New(), ctx: context.Background()}
}

// mem inserts a workspace-scope memory; hash nil stores NULL.
func (f *simhashFx) mem(key, content string, hash *int64, created time.Time) uuid.UUID {
	var id uuid.UUID
	require.NoError(f.t, f.db.Get(&id, `INSERT INTO memories (workspace_id, key, content, scope, status, content_simhash, created_at, updated_at)
		VALUES ($1, $2, $3, 'workspace', 'active', $4, $5, $5) RETURNING id`, f.ws, key, content, hash, created))
	return id
}

func (f *simhashFx) hash(id uuid.UUID) sql.NullInt64 {
	var h sql.NullInt64
	require.NoError(f.t, f.db.Get(&h, `SELECT content_simhash FROM memories WHERE id = $1`, id))
	return h
}

const (
	simhashTextA = "The nightly export job writes one archive per workspace into the object store, " +
		"named by date, and keeps the last fourteen of them; older archives are removed by the same job."
	simhashTextB = "Release notes for the board view: columns can be collapsed, the card counter respects filters, " +
		"and dragging a card into a collapsed column expands it for the drop."
)

func TestFindNewerMatch_SimhashNeedsEqualText(t *testing.T) {
	f := newSimhashFx(t)
	repo := NewMemoryRepo(f.db)
	base := time.Now().Add(-48 * time.Hour)
	shared := int64(-4408558833229370888)
	var zero int64

	old := f.mem("old-note", simhashTextA, &shared, base)
	find := func(key string, h *int64, content string) uuid.UUID {
		t.Helper()
		got, err := repo.FindNewerMatch(f.ctx, f.ws, key, h, content, old, base)
		require.NoError(t, err)
		if got == nil {
			return uuid.Nil
		}
		return got.ID
	}

	// Unrelated text with the same hash (a collision): no match.
	unrelated := f.mem("other-note", simhashTextB, &shared, base.Add(time.Hour))
	require.Equal(t, uuid.Nil, find("old-note", &shared, simhashTextA), "equal hash, different text and key")

	// Zero hash on short different texts: no match.
	f.mem("zero-new", "no", &zero, base.Add(2*time.Hour))
	require.Equal(t, uuid.Nil, find("zero-old", &zero, "ok"), "zero hash, different text")

	// Positive control: the same text (whitespace aside) with the same hash matches.
	same := f.mem("copy-note", "  "+strings.ReplaceAll(simhashTextA, " ", "\n ")+"\t", &shared, base.Add(3*time.Hour))
	require.Equal(t, same, find("old-note", &shared, simhashTextA), "equal hash and equal text")

	// The key branch is unchanged: same key matches whatever the hash and text.
	// (old is archived only to free its key under uq_mem_*_key for the newer row.)
	_, err := f.db.Exec(`DELETE FROM memories WHERE id = $1`, same)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE memories SET archived = true WHERE id = $1`, old)
	require.NoError(t, err)
	sameKey := f.mem("old-note", simhashTextB, nil, base.Add(4*time.Hour))
	require.Equal(t, sameKey, find("old-note", &shared, simhashTextA), "same key")
	require.NotEqual(t, unrelated, sameKey)
}

func TestBackfillContentSimhash_RecomputesOnceAndSkipsChangedRows(t *testing.T) {
	f := newSimhashIsolatedFx(t)
	repo := NewMemoryRepo(f.db)
	algo := "test-" + uuid.NewString()[:8]

	created := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	wrong := int64(12345)
	var zero int64
	good := simhash.Compute(simhashTextB)
	ids := map[string]uuid.UUID{
		"stale": f.mem("stale", simhashTextA, &wrong, created),
		"null":  f.mem("null", "short note", nil, created),
		"zero":  f.mem("zero", "another note", &zero, created),
		"good":  f.mem("good", simhashTextB, &good, created),
	}
	contents := map[string]string{"stale": simhashTextA, "null": "short note", "zero": "another note", "good": simhashTextB}

	// Locked by another holder: nothing is done.
	holder, err := f.db.Connx(f.ctx)
	require.NoError(t, err)
	_, err = holder.ExecContext(f.ctx, `SELECT pg_advisory_lock($1)`, memorySimhashBackfillLock)
	require.NoError(t, err)
	res, err := repo.BackfillContentSimhash(f.ctx, algo, simhash.Compute, 2)
	require.NoError(t, err)
	require.True(t, res.Locked)
	require.Equal(t, wrong, f.hash(ids["stale"]).Int64, "a locked run writes nothing")
	_, err = holder.ExecContext(f.ctx, `SELECT pg_advisory_unlock($1)`, memorySimhashBackfillLock)
	require.NoError(t, err)
	require.NoError(t, holder.Close())

	// Batches of 2 cover every row; stored == computed afterwards.
	res, err = repo.BackfillContentSimhash(f.ctx, algo, simhash.Compute, 2)
	require.NoError(t, err)
	require.Equal(t, MemorySimhashBackfillResult{Scanned: 4, Updated: 3}, res, "stale, NULL and zero rows are rewritten, the good one is not")
	for name, id := range ids {
		h := f.hash(id)
		require.True(t, h.Valid, name)
		require.Equal(t, simhash.Compute(contents[name]), h.Int64, name)
	}
	var untouched bool
	require.NoError(t, f.db.Get(&untouched, `SELECT bool_and(updated_at = $2) FROM memories WHERE workspace_id = $1`, f.ws, created))
	require.True(t, untouched, "the recompute does not touch updated_at")

	var st struct {
		Finished sql.NullTime `db:"finished_at"`
		Scanned  int64        `db:"rows_scanned"`
		Updated  int64        `db:"rows_updated"`
	}
	require.NoError(t, f.db.Get(&st, `SELECT finished_at, rows_scanned, rows_updated FROM memory_simhash_backfill WHERE algorithm = $1`, algo))
	require.True(t, st.Finished.Valid)
	require.Equal(t, res.Scanned, st.Scanned)
	require.Equal(t, res.Updated, st.Updated)

	// Re-run: done once per algorithm, a later wrong value stays for the next algorithm.
	_, err = f.db.Exec(`UPDATE memories SET content_simhash = $2 WHERE id = $1`, ids["good"], wrong)
	require.NoError(t, err)
	res, err = repo.BackfillContentSimhash(f.ctx, algo, simhash.Compute, 2)
	require.NoError(t, err)
	require.True(t, res.AlreadyDone)
	require.Equal(t, wrong, f.hash(ids["good"]).Int64)

	// Concurrent edit: a row whose content changed after it was read is skipped.
	conn, err := f.db.Connx(f.ctx)
	require.NoError(t, err)
	defer conn.Close()
	ups := staleSimhashes([]simhashRow{{ID: ids["good"], Content: simhashTextB, Simhash: sql.NullInt64{Int64: wrong, Valid: true}}}, simhash.Compute)
	require.Len(t, ups, 1)
	_, err = f.db.Exec(`UPDATE memories SET content = 'edited meanwhile' WHERE id = $1`, ids["good"])
	require.NoError(t, err)
	var batch MemorySimhashBackfillResult
	require.NoError(t, repo.backfillSimhashBatch(f.ctx, conn, ups, &batch))
	require.Equal(t, MemorySimhashBackfillResult{Skipped: 1}, batch)
	require.Equal(t, wrong, f.hash(ids["good"]).Int64, "the hash of the old text is not written onto the new text")
}

func TestRecountSimhashAggregates_RecountsOnNewHashesOnce(t *testing.T) {
	f := newRollupFx(t)
	p := f.fx.projectID
	algo := "test-" + uuid.NewString()[:8]
	// A stats day out of the window (before statsFrom) with collected misses.
	outDay := f.day.AddDate(0, 0, -400)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM memory_simhash_backfill WHERE algorithm = $1`, algo)
		_, _ = f.db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, f.fx.workspaceID)
		_, _ = f.db.Exec(`DELETE FROM memory_stats_daily WHERE day = $1::date`, outDay)
		_, _ = f.db.Exec(`DELETE FROM memory_daily_rollups WHERE day = $1::date`, outDay)
	})
	old := f.day.AddDate(0, 0, -3)
	mem := func(key string, hash int64, created time.Time) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, f.db.Get(&id, `INSERT INTO memories (workspace_id, project_id, key, content, scope, status, content_simhash, created_at)
			VALUES ($1, $2, $3, 'c', 'project', 'active', $4, $5) RETURNING id`, f.fx.workspaceID, p, key, hash, created))
		return id
	}
	// Old hashes: three unrelated rows collide on 7, two more on 9.
	a := mem("a", 7, old)
	b := mem("b", 7, old.Add(time.Minute))
	c := mem("c", 7, old.Add(2*time.Minute))
	d := mem("d", 9, old)
	e := mem("e", 9, old.Add(time.Minute))
	mem("late", 9, f.day.AddDate(0, 0, 2)) // after the day: not in the recount either

	repo := NewMemoryDailyRollupRepo(f.db)
	_, wrote, err := repo.WriteBaseDay(f.ctx, f.day, nil)
	require.NoError(t, err)
	require.True(t, wrote)
	type dupRow struct {
		Total  int64 `db:"total"`
		Groups int64 `db:"dup_groups"`
		Rows   int64 `db:"dup_rows"`
	}
	read := func() dupRow {
		var r dupRow
		require.NoError(t, f.db.Get(&r, `SELECT total, dup_groups, dup_rows FROM memory_base_daily
			WHERE day = $1::date AND workspace_id = $2 AND project_id = $3`, f.day, f.fx.workspaceID, p))
		return r
	}
	require.Equal(t, dupRow{Total: 5, Groups: 2, Rows: 5}, read(), "snapshot on the old hashes")

	// New hashes: a and b stay a real duplicate pair, the others separate.
	for id, h := range map[uuid.UUID]int64{a: 100, b: 100, c: 101, d: 102, e: 103} {
		_, err = f.db.Exec(`UPDATE memories SET content_simhash = $2 WHERE id = $1`, id, h)
		require.NoError(t, err)
	}

	// Stats of the day written from a journal that covers it (one event the
	// day before), with misses collected (U7): queued for a rewrite.
	ag := f.agent()
	f.ev(rollupEv{ts: f.at(-1, 0), agent: &ag, op: "get_memory"})
	f.ev(rollupEv{ts: f.at(10, 0), agent: &ag, op: "get_memory"})
	_, err = repo.WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO memory_stats_daily (day, workspace_id, caller_kind, op, source, misses_reason)
		VALUES ($1::date, $2, 'agent_tool', 'recall', '*', NULL)`, outDay, f.fx.workspaceID)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO memory_daily_rollups (day, stats_written_at) VALUES ($1::date, now())`, outDay)
	require.NoError(t, err)
	written := func(day time.Time) bool {
		var ok bool
		require.NoError(t, f.db.Get(&ok, `SELECT stats_written_at IS NOT NULL FROM memory_daily_rollups WHERE day = $1::date`, day))
		return ok
	}
	require.True(t, written(f.day))
	statsFrom := f.day.AddDate(0, 0, -30)

	// Before the recompute has finished: nothing.
	days, stats, err := repo.RecountSimhashAggregates(f.ctx, algo, statsFrom)
	require.NoError(t, err)
	require.Zero(t, days)
	require.Zero(t, stats)
	_, err = f.db.Exec(`INSERT INTO memory_simhash_backfill (algorithm) VALUES ($1)`, algo)
	require.NoError(t, err)
	days, _, err = repo.RecountSimhashAggregates(f.ctx, algo, statsFrom)
	require.NoError(t, err)
	require.Zero(t, days, "a started, unfinished recompute does not recount")
	require.Equal(t, dupRow{Total: 5, Groups: 2, Rows: 5}, read())
	require.True(t, written(f.day))

	_, err = f.db.Exec(`UPDATE memory_simhash_backfill SET finished_at = now() WHERE algorithm = $1`, algo)
	require.NoError(t, err)
	days, stats, err = repo.RecountSimhashAggregates(f.ctx, algo, statsFrom)
	require.NoError(t, err)
	require.GreaterOrEqual(t, days, 1)
	require.GreaterOrEqual(t, stats, int64(1))
	require.Equal(t, dupRow{Total: 5, Groups: 1, Rows: 2}, read(), "recounted on the new hashes; other columns kept")
	require.False(t, written(f.day), "stats day with collected misses is queued for the nightly rewrite")
	require.True(t, written(outDay), "a day before statsFrom stays written")
	var kept int
	require.NoError(t, f.db.Get(&kept, `SELECT count(*) FROM memory_stats_daily WHERE day = $1::date`, f.day))
	require.Positive(t, kept, "the rows stay until the job writes the day again")

	// Once: a later hash change is not recounted again under the same algorithm.
	_, err = repo.WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE memories SET content_simhash = 100 WHERE id = $1`, c)
	require.NoError(t, err)
	days, stats, err = repo.RecountSimhashAggregates(f.ctx, algo, statsFrom)
	require.NoError(t, err)
	require.Zero(t, days)
	require.Zero(t, stats)
	require.Equal(t, dupRow{Total: 5, Groups: 1, Rows: 2}, read())
	require.True(t, written(f.day))
}

// The repair migration on stand-in memories/memory_edges tables in a
// throwaway schema: Up restores exactly the collision supersedes and logs
// them, Down puts them back with their previous values and the old hashes,
// and Up is repeatable after Down.
func TestMemorySimhashRepairMigration_UpDownUp(t *testing.T) {
	db := userRepoTestDB(t)
	raw, err := os.ReadFile("../../../migrations/20261121240_memory_simhash_repair.sql")
	require.NoError(t, err)
	body := string(raw)
	upStart, downStart := strings.Index(body, "-- +goose Up"), strings.Index(body, "-- +goose Down")
	require.True(t, upStart >= 0 && downStart > upStart)
	up, down := body[upStart:downStart], body[downStart:]

	ctx := context.Background()
	schema := "mem_simhash_repair_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	conn, err := db.Connx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close()
	})
	run := func(q string, args ...any) {
		t.Helper()
		_, err := conn.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	run("CREATE SCHEMA " + schema)
	run("SET search_path TO " + schema)
	run(`CREATE TABLE memories (
		id UUID PRIMARY KEY, workspace_id UUID NOT NULL, scope TEXT NOT NULL DEFAULT 'workspace',
		project_id UUID, agent_id UUID, key TEXT NOT NULL, content TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', archived BOOLEAN NOT NULL DEFAULT false,
		freshness_score REAL NOT NULL DEFAULT 1.0, superseded_by UUID,
		content_simhash BIGINT, tags TEXT[] DEFAULT '{}', importance_score REAL NOT NULL DEFAULT 0.5,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`)
	run(`CREATE TABLE memory_edges (
		memory_from_id UUID NOT NULL, memory_to_id UUID NOT NULL, relationship_type TEXT NOT NULL,
		weight REAL NOT NULL DEFAULT 1.0, workspace_id UUID NOT NULL,
		UNIQUE (memory_from_id, memory_to_id, relationship_type))`)

	ws := uuid.New()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ins := func(key, content string, hash int64, created time.Time) uuid.UUID {
		id := uuid.New()
		run(`INSERT INTO memories (id, workspace_id, key, content, content_simhash, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)`, id, ws, key, content, hash, created)
		return id
	}
	supersede := func(o, n uuid.UUID, edge *float64) {
		run(`UPDATE memories SET status = 'superseded', freshness_score = 0.1, superseded_by = $2, updated_at = $3 WHERE id = $1`,
			o, n, t0.Add(48*time.Hour))
		if edge != nil {
			run(`INSERT INTO memory_edges (memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
				VALUES ($1, $2, 'supersedes', $3, $4)`, n, o, *edge, ws)
		}
	}
	w := 0.4

	// collision: different keys, equal hash, different text -> restored
	colO := ins("export-retention", simhashTextA, 7, t0)
	colN := ins("board-release", simhashTextB, 7, t0.Add(time.Hour))
	supersede(colO, colN, &w)
	// collision without an edge left (pruned by decay) -> restored, edge NULL
	bareO := ins("bare-old", "first unrelated note", 8, t0)
	bareN := ins("bare-new", "second unrelated note", 8, t0.Add(time.Hour))
	supersede(bareO, bareN, nil)
	// collision restored by Up and edited afterwards -> Down leaves it as edited
	edO := ins("edit-old", "third unrelated note", 16, t0)
	edN := ins("edit-new", "fourth unrelated note", 16, t0.Add(time.Hour))
	supersede(edO, edN, &w)
	// An edited row can later be superseded again by the same target, after
	// its new edge has been pruned. Down must not recreate the old text's edge.
	reO := ins("resupersede-old", "fifth unrelated note", 19, t0)
	reN := ins("resupersede-new", "sixth unrelated note", 19, t0.Add(time.Hour))
	supersede(reO, reN, &w)
	// same key -> kept
	skO := ins("same-key", "old text", 9, t0)
	skN := ins("same-key", "new text", 9, t0.Add(time.Hour))
	supersede(skO, skN, &w)
	// equal text modulo whitespace -> kept
	eqO := ins("dup-a", "the same  note", 10, t0)
	eqN := ins("dup-b", " the same\nnote ", 10, t0.Add(time.Hour))
	supersede(eqO, eqN, &w)
	// different hashes (linker path, not this defect) -> kept
	lnO := ins("linker-a", "alpha", 11, t0)
	lnN := ins("linker-b", "beta", 12, t0.Add(time.Hour))
	supersede(lnO, lnN, &w)
	// key meanwhile taken by a live row of the scope -> kept, not logged
	tkO := ins("taken-key", "gamma", 13, t0)
	tkN := ins("taken-other", "delta", 13, t0.Add(time.Hour))
	supersede(tkO, tkN, &w)
	ins("taken-key", "epsilon", 14, t0.Add(2*time.Hour))
	// archived -> kept
	arO := ins("arch-a", "zeta", 15, t0)
	arN := ins("arch-b", "eta", 15, t0.Add(time.Hour))
	supersede(arO, arN, &w)
	run(`UPDATE memories SET archived = true WHERE id = $1`, arO)
	// the linker reaches its threshold without the hash (key prefix, tags, >1h) -> kept
	lkO := ins("episode:gary:a", "kappa", 17, t0)
	lkN := ins("episode:gary:b", "lambda", 17, t0.Add(2*time.Hour))
	run(`UPDATE memories SET tags = '{deploy,api}' WHERE id IN ($1, $2)`, lkO, lkN)
	supersede(lkO, lkN, &w)
	// the same but within the hour: the hash was needed -> restored
	lhO := ins("episode:gary:c", "mu", 18, t0)
	lhN := ins("episode:gary:d", "nu", 18, t0.Add(30*time.Minute))
	run(`UPDATE memories SET tags = '{deploy,api}' WHERE id IN ($1, $2)`, lhO, lhN)
	supersede(lhO, lhN, &w)

	type state struct {
		Status       string        `db:"status"`
		Freshness    float64       `db:"freshness_score"`
		SupersededBy *uuid.UUID    `db:"superseded_by"`
		UpdatedAt    time.Time     `db:"updated_at"`
		Hash         sql.NullInt64 `db:"content_simhash"`
	}
	get := func(id uuid.UUID) state {
		var s state
		require.NoError(t, conn.GetContext(ctx, &s, `SELECT status, freshness_score, superseded_by, updated_at, content_simhash FROM memories WHERE id = $1`, id))
		return s
	}
	edges := func(from, to uuid.UUID) []float64 {
		var ws []float64
		require.NoError(t, conn.SelectContext(ctx, &ws, `SELECT weight FROM memory_edges WHERE memory_from_id = $1 AND memory_to_id = $2 AND relationship_type = 'supersedes'`, from, to))
		return ws
	}
	before := map[uuid.UUID]state{}
	for _, id := range []uuid.UUID{colO, bareO, skO, eqO, lnO, tkO, arO, lkO} {
		before[id] = get(id)
	}

	run(up)

	for _, id := range []uuid.UUID{colO, bareO, edO, reO, lhO} {
		s := get(id)
		require.Equal(t, "active", s.Status)
		require.InDelta(t, 1.0, s.Freshness, 1e-6)
		require.Nil(t, s.SupersededBy)
	}
	require.Empty(t, edges(colN, colO), "the collision edge is removed")
	for _, id := range []uuid.UUID{skO, eqO, lnO, tkO, arO, lkO} {
		require.Equal(t, before[id], get(id), "legitimate or out-of-scope supersede is kept")
	}
	require.Equal(t, []float64{w}, edges(skN, skO))

	var logged []struct {
		ID   uuid.UUID `db:"memory_id"`
		Edge *float64  `db:"edge_weight"`
	}
	require.NoError(t, conn.SelectContext(ctx, &logged, `SELECT memory_id, edge_weight FROM memory_simhash_repair_log`))
	loggedEdge := map[uuid.UUID]*float64{}
	for _, l := range logged {
		loggedEdge[l.ID] = l.Edge
	}
	require.Len(t, loggedEdge, 5)
	require.Contains(t, loggedEdge, colO)
	require.NotNil(t, loggedEdge[colO])
	require.InDelta(t, w, *loggedEdge[colO], 1e-6)
	require.Contains(t, loggedEdge, bareO)
	require.Nil(t, loggedEdge[bareO])
	require.Contains(t, loggedEdge, edO)
	require.Contains(t, loggedEdge, reO)
	var anchored int
	require.NoError(t, conn.GetContext(ctx, &anchored, `SELECT count(*) FROM memory_simhash_v1_anchor a JOIN memories m ON m.id = a.memory_id AND a.content_simhash = m.content_simhash`))
	require.Equal(t, 23, anchored, "every row's pre-fix hash is anchored")

	// The API recompute rewrites hashes; Down restores the old ones and the supersedes.
	run(`UPDATE memories SET content_simhash = content_simhash + 1000`)
	// A restored row edited after the migration stays as edited: active, no
	// supersede, no edge, the hash of its new text.
	run(`UPDATE memories SET content = 'edited after the migration', content_simhash = 4242 WHERE id = $1`, edO)
	run(`UPDATE memories SET content = 'edited and superseded again', content_simhash = 4343 WHERE id = $1`, reO)
	supersede(reO, reN, nil)
	resuperseded := get(reO)
	require.Empty(t, edges(reN, reO), "the later supersede has no remaining edge")
	run(down)
	require.Equal(t, resuperseded, get(reO), "Down preserves the edited row's later supersede and hash")
	require.Empty(t, edges(reN, reO), "Down must not restore the old text's edge onto the edited row")
	ed := get(edO)
	require.Equal(t, "active", ed.Status, "Down does not re-supersede an edited row")
	require.Nil(t, ed.SupersededBy)
	require.Empty(t, edges(edN, edO), "no supersedes edge back onto an edited row")
	require.Equal(t, int64(4242), ed.Hash.Int64, "Down does not put the old text's hash on an edited row")
	for id, s := range before {
		got := get(id)
		require.Equal(t, s.Status, got.Status)
		require.InDelta(t, s.Freshness, got.Freshness, 1e-6)
		require.Equal(t, s.SupersededBy, got.SupersededBy)
		require.True(t, s.UpdatedAt.Equal(got.UpdatedAt))
		require.Equal(t, s.Hash, got.Hash, "old hash restored")
	}
	require.Len(t, edges(colN, colO), 1, "the removed edge is back")
	require.InDelta(t, w, edges(colN, colO)[0], 1e-6)
	require.Empty(t, edges(bareN, bareO), "no edge invented where none was")
	var tables int
	require.NoError(t, conn.GetContext(ctx, &tables, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1
		AND table_name IN ('memory_simhash_v1_anchor', 'memory_simhash_repair_log', 'memory_simhash_backfill')`, schema))
	require.Zero(t, tables)

	run(up)
	require.Equal(t, "active", get(colO).Status, "Up is repeatable after Down")
}
