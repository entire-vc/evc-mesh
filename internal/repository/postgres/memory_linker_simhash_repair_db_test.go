package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// The linker supersede repair and its migration 20261121400 on
// stand-in tables in a throwaway schema: the repair waits for the finished
// simhash recompute, returns to review_needed exactly the supersedes the linker
// made with the hash signal of the pre-fix hashes and would not make on the new
// ones, re-points the ones that cannot come back
// (key current elsewhere) to the current row of their key, keeps the
// legitimate and the out-of-scope ones, logs every change, runs once; Down puts
// the changed rows back unless they were edited or superseded again since; Up
// and the repair are repeatable after Down.

const linkerRepairAlgo = "test-algo"

type linkerRepairFx struct {
	t    *testing.T
	ctx  context.Context
	conn *sqlx.DB
	repo *MemoryRepo
	ws   uuid.UUID
	t0   time.Time
	up   string
	down string
}

func newLinkerRepairFx(t *testing.T) *linkerRepairFx {
	shared := userRepoTestDB(t)
	raw, err := os.ReadFile("../../../migrations/20261121400_memory_linker_simhash_repair.sql")
	require.NoError(t, err)
	body := string(raw)
	upStart, downStart := strings.Index(body, "-- +goose Up"), strings.Index(body, "-- +goose Down")
	require.True(t, upStart >= 0 && downStart > upStart)

	schema := "mem_linker_repair_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	_, err = shared.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = shared.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	db, err := sqlx.Connect("postgres", dsnWithSearchPath(t, userRepoTestDSN(), schema))
	require.NoError(t, err)
	db.SetMaxOpenConns(1) // one session: search_path and the advisory lock stay on it
	t.Cleanup(func() { _ = db.Close() })

	f := &linkerRepairFx{t: t, ctx: context.Background(), conn: db, repo: NewMemoryRepo(db), ws: uuid.New(),
		t0: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), up: body[upStart:downStart], down: body[downStart:]}
	f.run(`CREATE TABLE memories (
		id UUID PRIMARY KEY, workspace_id UUID NOT NULL, scope TEXT NOT NULL DEFAULT 'workspace',
		project_id UUID, agent_id UUID, key TEXT NOT NULL, content TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', archived BOOLEAN NOT NULL DEFAULT false,
		freshness_score REAL NOT NULL DEFAULT 1.0, superseded_by UUID,
		content_simhash BIGINT, tags TEXT[] DEFAULT '{}', importance_score REAL NOT NULL DEFAULT 0.5,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`)
	f.run(`CREATE UNIQUE INDEX uq_mem_ws_key ON memories (workspace_id, key)
		WHERE scope = 'workspace' AND status <> 'superseded' AND archived = false`)
	f.run(`CREATE TABLE memory_edges (
		memory_from_id UUID NOT NULL, memory_to_id UUID NOT NULL, relationship_type TEXT NOT NULL,
		weight REAL NOT NULL DEFAULT 1.0, workspace_id UUID NOT NULL,
		UNIQUE (memory_from_id, memory_to_id, relationship_type))`)
	f.run(`CREATE TABLE memory_simhash_backfill (algorithm TEXT PRIMARY KEY, finished_at TIMESTAMPTZ NULL)`)
	f.run(`CREATE TABLE memory_simhash_v1_anchor (memory_id UUID PRIMARY KEY, content_simhash BIGINT, content_md5 TEXT NOT NULL)`)
	return f
}

func (f *linkerRepairFx) run(q string, args ...any) {
	f.t.Helper()
	_, err := f.conn.ExecContext(f.ctx, q, args...)
	require.NoError(f.t, err)
}

// ins inserts a workspace-scope active memory created at t0+offset.
func (f *linkerRepairFx) ins(key, content string, hash int64, offset time.Duration, tags ...string) uuid.UUID {
	id := uuid.New()
	if tags == nil {
		tags = []string{}
	}
	f.run(`INSERT INTO memories (id, workspace_id, key, content, content_simhash, tags, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, id, f.ws, key, content, hash, tags, f.t0.Add(offset))
	return id
}

// anchor records a row's pre-fix hash, as migration 20261121240 does.
func (f *linkerRepairFx) anchor(id uuid.UUID, oldHash int64) {
	f.run(`INSERT INTO memory_simhash_v1_anchor (memory_id, content_simhash, content_md5)
		SELECT id, $2, md5(content) FROM memories WHERE id = $1
		ON CONFLICT (memory_id) DO UPDATE SET content_simhash = EXCLUDED.content_simhash`, id, oldHash)
}

// supersede does what the linker's supersede does; edge nil = no surviving edge.
func (f *linkerRepairFx) supersede(o, n uuid.UUID, edge *float64) {
	f.run(`UPDATE memories SET status = 'superseded', freshness_score = 0.1, superseded_by = $2, updated_at = $3 WHERE id = $1`,
		o, n, f.t0.Add(72*time.Hour))
	if edge != nil {
		f.run(`INSERT INTO memory_edges (memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
			VALUES ($1, $2, 'supersedes', $3, $4)`, n, o, *edge, f.ws)
	}
}

type linkerRow struct {
	Status       string     `db:"status"`
	Freshness    float64    `db:"freshness_score"`
	SupersededBy *uuid.UUID `db:"superseded_by"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

func (f *linkerRepairFx) get(id uuid.UUID) linkerRow {
	var r linkerRow
	require.NoError(f.t, f.conn.GetContext(f.ctx, &r, `SELECT status, freshness_score, superseded_by, updated_at FROM memories WHERE id = $1`, id))
	return r
}

func (f *linkerRepairFx) edges(from, to uuid.UUID) []float64 {
	var ws []float64
	require.NoError(f.t, f.conn.SelectContext(f.ctx, &ws, `SELECT weight FROM memory_edges WHERE memory_from_id = $1 AND memory_to_id = $2 AND relationship_type = 'supersedes'`, from, to))
	return ws
}

// ac2 is the card's acceptance query: linker supersedes between different keys
// still standing on a hash distance over the linker threshold.
func (f *linkerRepairFx) ac2() int {
	var n int
	require.NoError(f.t, f.conn.GetContext(f.ctx, &n, `SELECT count(*) FROM memories o JOIN memories n ON n.id=o.superseded_by
		JOIN memory_edges e ON e.memory_from_id=n.id AND e.memory_to_id=o.id AND e.relationship_type='supersedes'
		WHERE o.status='superseded' AND o.key<>n.key AND bit_count((o.content_simhash # n.content_simhash)::bit(64))>10`))
	return n
}

// Hashes: 0 against 0x3FF is 10 bits (within the linker threshold), against
// 0x7FF 11 bits (beyond it).
const (
	hNear = int64(0x3FF)
	hFar  = int64(0x7FF)
)

func TestRepairLinkerSimhashSupersedes_FailureIsAtomicAndRetryable(t *testing.T) {
	// Real PostgreSQL rejects each write in turn, including a deferred failure
	// at commit. No completed phase may leak through a failed repair or prevent
	// a later attempt from repairing the same two memories.
	for _, stage := range []string{"journal", "restore", "repoint", "edges", "finish", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := newLinkerRepairFx(t)
			weight := 0.4
			older := f.ins("restore-old", "old restore text", 0, 0, "x")
			newer := f.ins("restore-new", "new restore text", hFar, 2*time.Hour, "x")
			f.supersede(older, newer, &weight)
			taken := f.ins("taken-key", "old taken text", 0, 0, "x")
			target := f.ins("taken-target", "new taken text", hFar, 2*time.Hour, "x")
			f.supersede(taken, target, &weight)
			holder := f.ins("taken-key", "current key holder", 5, 3*time.Hour)
			for _, id := range []uuid.UUID{older, taken} {
				f.anchor(id, 0)
			}
			for _, id := range []uuid.UUID{newer, target} {
				f.anchor(id, hNear)
			}
			before := map[uuid.UUID]linkerRow{}
			for _, id := range []uuid.UUID{older, newer, taken, target, holder} {
				before[id] = f.get(id)
			}
			f.run(f.up)
			f.run(`INSERT INTO memory_simhash_backfill (algorithm, finished_at) VALUES ($1, NOW())`, linkerRepairAlgo)

			var removeFault string
			switch stage {
			case "journal":
				f.run(`ALTER TABLE memory_linker_simhash_repair_log ADD CONSTRAINT repair_fault CHECK (false)`)
				removeFault = `ALTER TABLE memory_linker_simhash_repair_log DROP CONSTRAINT repair_fault`
			case "restore":
				f.run(`ALTER TABLE memories ADD CONSTRAINT repair_fault CHECK (status <> 'review_needed')`)
				removeFault = `ALTER TABLE memories DROP CONSTRAINT repair_fault`
			case "repoint":
				f.run(fmt.Sprintf(`ALTER TABLE memories ADD CONSTRAINT repair_fault CHECK (superseded_by IS DISTINCT FROM '%s'::uuid)`, holder))
				removeFault = `ALTER TABLE memories DROP CONSTRAINT repair_fault`
			case "edges", "commit":
				f.run(`CREATE FUNCTION fail_repair() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN RAISE EXCEPTION 'repair_fault'; END $$`)
				if stage == "edges" {
					f.run(`CREATE TRIGGER repair_fault BEFORE DELETE ON memory_edges FOR EACH ROW EXECUTE FUNCTION fail_repair()`)
					removeFault = `DROP TRIGGER repair_fault ON memory_edges`
				} else {
					f.run(`CREATE CONSTRAINT TRIGGER repair_fault AFTER INSERT ON memory_linker_simhash_repair_run
						DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_repair()`)
					removeFault = `DROP TRIGGER repair_fault ON memory_linker_simhash_repair_run`
				}
			case "finish":
				f.run(`ALTER TABLE memory_linker_simhash_repair_run ADD CONSTRAINT repair_fault CHECK (false)`)
				removeFault = `ALTER TABLE memory_linker_simhash_repair_run DROP CONSTRAINT repair_fault`
			}

			_, err := f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
			require.Error(t, err)
			require.Contains(t, err.Error(), "repair_fault")
			phase := stage
			if phase == "journal" {
				phase = "log"
			}
			require.Contains(t, err.Error(), "linker simhash repair: "+phase+":", "the intended phase must reject the write")
			for id, row := range before {
				require.Equal(t, row, f.get(id), "a failed repair must roll back every memory")
			}
			require.Equal(t, []float64{weight}, f.edges(newer, older))
			require.Equal(t, []float64{weight}, f.edges(target, taken))
			var logged, runs int
			require.NoError(t, f.conn.Get(&logged, `SELECT count(*) FROM memory_linker_simhash_repair_log`))
			require.NoError(t, f.conn.Get(&runs, `SELECT count(*) FROM memory_linker_simhash_repair_run`))
			require.Zero(t, logged, "no partial journal survives failure")
			require.Zero(t, runs, "failure cannot mark the repair as completed")

			f.run(removeFault)
			res, err := f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
			require.NoError(t, err)
			require.Equal(t, LinkerSimhashRepairResult{Restored: 1, Repointed: 1}, res)
			restored := f.get(older)
			require.Equal(t, "review_needed", restored.Status)
			require.Nil(t, restored.SupersededBy)
			require.InDelta(t, 0.5, restored.Freshness, 1e-6)
			require.Equal(t, &holder, f.get(taken).SupersededBy)
			require.Empty(t, f.edges(newer, older))
			require.Empty(t, f.edges(target, taken))
			require.NoError(t, f.conn.Get(&logged, `SELECT count(*) FROM memory_linker_simhash_repair_log`))
			require.Equal(t, 2, logged)
			var persisted LinkerSimhashRepairResult
			require.NoError(t, f.conn.Get(&persisted, `SELECT restored, repointed FROM memory_linker_simhash_repair_run WHERE algorithm = $1`, linkerRepairAlgo))
			require.Equal(t, res, persisted, "successful retry persists both counters")
		})
	}
}

func TestRepairLinkerSimhashSupersedes_SkipsEditedAnchors(t *testing.T) {
	for _, edited := range []string{"older", "newer"} {
		t.Run(edited, func(t *testing.T) {
			f := newLinkerRepairFx(t)
			w := 0.4
			older := f.ins("anchor-old", "original old text", 0, 0, "x")
			newer := f.ins("anchor-new", "original new text", hFar, 2*time.Hour, "x")
			f.anchor(older, 0)
			f.anchor(newer, hNear)
			changed := older
			if edited == "newer" {
				changed = newer
			}
			f.run(`UPDATE memories SET content = 'text changed after anchor' WHERE id = $1`, changed)
			f.supersede(older, newer, &w)
			before := f.get(older)
			f.run(f.up)
			f.run(`INSERT INTO memory_simhash_backfill (algorithm, finished_at) VALUES ($1, NOW())`, linkerRepairAlgo)
			res, err := f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
			require.NoError(t, err)
			require.Equal(t, LinkerSimhashRepairResult{}, res)
			require.Equal(t, before, f.get(older), "stale anchors cannot justify a repair")
			require.Equal(t, []float64{w}, f.edges(newer, older))
			var logged int
			require.NoError(t, f.conn.GetContext(f.ctx, &logged, `SELECT count(*) FROM memory_linker_simhash_repair_log`))
			require.Zero(t, logged)
		})
	}
}

func TestRepairLinkerSimhashSupersedes_DownPreservesLaterResupersede(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited=%t", edited), func(t *testing.T) {
			f := newLinkerRepairFx(t)
			w := 0.4
			older := f.ins("edited-old", "original old text", 0, 0, "x")
			newer := f.ins("edited-new", "original new text", hFar, 2*time.Hour, "x")
			f.supersede(older, newer, &w)
			f.anchor(older, 0)
			f.anchor(newer, hNear)
			f.run(f.up)
			f.run(`INSERT INTO memory_simhash_backfill (algorithm, finished_at) VALUES ($1, NOW())`, linkerRepairAlgo)
			res, err := f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
			require.NoError(t, err)
			require.Equal(t, LinkerSimhashRepairResult{Restored: 1}, res)
			require.Empty(t, f.edges(newer, older), "repair removes the original edge")

			// A later supersede to the same target may have no edge. Down must
			// preserve that operation, including when the content did not change.
			if edited {
				f.run(`UPDATE memories SET content = 'edited and superseded again' WHERE id = $1`, older)
			}
			f.supersede(older, newer, nil)
			before := f.get(older)
			f.run(f.down)
			require.Equal(t, before, f.get(older), "Down preserves the later supersede")
			require.Empty(t, f.edges(newer, older), "Down must not recreate an edge for the original text")
		})
	}
}

func TestRepairLinkerSimhashSupersedes_UpRepairDownUp(t *testing.T) {
	f := newLinkerRepairFx(t)
	w, w2 := 0.4, 0.7
	hour2 := 2 * time.Hour

	// Signals T+N, hash now 11 bits: the old hash made the third signal -> restored.
	farO := f.ins("export-retention", "older text one", 0, 0, "deploy", "api")
	farN := f.ins("board-release", "newer text one", hFar, hour2, "deploy", "api")
	f.supersede(farO, farN, &w)
	// The same with the edge missing -> restored, no edge logged.
	bareO := f.ins("bare-old", "older text two", 0, 0, "x")
	bareN := f.ins("bare-new", "newer text two", hFar, hour2, "x")
	f.supersede(bareO, bareN, nil)
	// Within 10 bits on the new hash: T+H+N, the linker would supersede now -> kept.
	nearO := f.ins("near-old", "older text three", 0, 0, "x")
	nearN := f.ins("near-new", "newer text three", hNear, hour2, "x")
	f.supersede(nearO, nearN, &w2)
	// Keys share two segments, tags, over an hour: P+T+N without the hash -> kept.
	ptnO := f.ins("episode:gary:a", "older text four", 0, 0, "x")
	ptnN := f.ins("episode:gary:b", "newer text four", hFar, hour2, "x")
	f.supersede(ptnO, ptnN, &w2)
	// Importance >= 0.8: the linker leaves the pair alone -> not its supersede now -> restored.
	impO := f.ins("imp-old", "older text five", 0, 0, "x")
	impN := f.ins("imp-new", "newer text five", hNear, hour2, "x")
	f.run(`UPDATE memories SET importance_score = 0.9 WHERE id = $1`, impO)
	f.supersede(impO, impN, &w)
	// Same key (versioning, collapse-memories) -> out of scope, kept.
	skO := f.ins("same-key", "older text six", 0, 0)
	f.run(`UPDATE memories SET status = 'superseded' WHERE id = $1`, skO) // one current row per key
	skN := f.ins("same-key", "newer text six", hFar, hour2)
	f.supersede(skO, skN, &w)
	// Equal text modulo whitespace (review triage) -> kept; a hash of the
	// normalized text would be equal, these differ to show the text decides.
	eqO := f.ins("dup-a", "the same  note", 0, 0)
	eqN := f.ins("dup-b", " the same\nnote ", hFar, hour2)
	f.supersede(eqO, eqN, &w)
	// Archived: an explicit act -> kept.
	arO := f.ins("arch-a", "older text seven", 0, 0)
	arN := f.ins("arch-b", "newer text seven", hFar, hour2)
	f.supersede(arO, arN, &w)
	f.run(`UPDATE memories SET archived = true WHERE id = $1`, arO)
	// Key held by a current row of the scope -> superseded by that row instead.
	tkO := f.ins("taken-key", "older text eight", 0, 0)
	tkN := f.ins("taken-other", "newer text eight", hFar, hour2)
	f.supersede(tkO, tkN, &w)
	tkL := f.ins("taken-key", "live holder", 5, 3*time.Hour)
	// Two candidates of one key: the newer comes back, the older is superseded by it.
	twA := f.ins("twin", "older twin", 0, 0)
	twB := f.ins("twin-src", "newer twin source", hFar, hour2)
	f.supersede(twA, twB, &w)
	twC := f.ins("twin", "newer twin", 0, time.Hour)
	twD := f.ins("twin-src2", "newest twin source", hFar, 3*time.Hour)
	f.supersede(twC, twD, &w)
	// A restored row edited afterwards: Down leaves it.
	edO := f.ins("edit-old", "older text nine", 0, 0)
	edN := f.ins("edit-new", "newer text nine", hFar, hour2)
	f.supersede(edO, edN, &w)
	// A restored row the triage moves to active: Down still reverts it.
	trO := f.ins("triage-old", "older text ten", 0, 0)
	trN := f.ins("triage-new", "newer text ten", hFar, hour2)
	f.supersede(trO, trN, &w)
	// A restored row the triage supersedes again: Down leaves it.
	reO := f.ins("resup-old", "older text eleven", 0, 0)
	reN := f.ins("resup-new", "newer text eleven", hFar, hour2)
	f.supersede(reO, reN, &w)
	// Pre-fix hashes over 10 bits apart: superseded without the hash signal
	// (the signals of that day), legitimate -> kept, stays in AC2 with a reason.
	olO := f.ins("old-far-old", "older text twelve", 0, 0, "x")
	olN := f.ins("old-far-new", "newer text twelve", hFar, hour2, "x")
	f.supersede(olO, olN, &w2)
	// Equal pre-fix hashes: the review triage's collision, migration
	// 20261121240's scope -> kept.
	ehO := f.ins("eqhash-old", "older text thirteen", 0, 0)
	ehN := f.ins("eqhash-new", "newer text thirteen", hFar, hour2)
	f.supersede(ehO, ehN, &w)
	// No anchor: written after the fix -> kept.
	naO := f.ins("noanchor-old", "older text fourteen", 0, 0)
	naN := f.ins("noanchor-new", "newer text fourteen", hFar, hour2)
	f.supersede(naO, naN, &w)

	// Pre-fix hashes: every row 0 or 10 bits from 0 (the linker's hash signal
	// held), except the cases above.
	f.run(`INSERT INTO memory_simhash_v1_anchor (memory_id, content_simhash, content_md5)
		SELECT id, CASE WHEN content_simhash = 0 THEN 0 ELSE $1 END, md5(content) FROM memories
		WHERE id NOT IN ($2, $3)`, hNear, naO, naN)
	f.anchor(olN, hFar)
	f.anchor(ehN, 0)

	restoredIDs := []uuid.UUID{farO, bareO, impO, twC, edO, trO, reO}
	repointedIDs := []uuid.UUID{tkO, twA}
	keptIDs := []uuid.UUID{nearO, ptnO, skO, eqO, arO, olO, ehO, naO}
	before := map[uuid.UUID]linkerRow{}
	for _, id := range append(append(append([]uuid.UUID{}, restoredIDs...), repointedIDs...), keptIDs...) {
		before[id] = f.get(id)
	}
	require.Equal(t, 13, f.ac2(), "red before the repair: supersedes between keys over 10 bits")

	f.run(f.up)

	// Not before the recompute has finished.
	res, err := f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
	require.NoError(t, err)
	require.Equal(t, LinkerSimhashRepairResult{NotReady: true}, res)
	f.run(`INSERT INTO memory_simhash_backfill (algorithm, finished_at) VALUES ($1, NULL)`, linkerRepairAlgo)
	res, err = f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
	require.NoError(t, err)
	require.Equal(t, LinkerSimhashRepairResult{NotReady: true}, res, "an unfinished recompute is not ready")
	for id, s := range before {
		require.Equal(t, s, f.get(id), "nothing changes before the recompute")
	}

	f.run(`UPDATE memory_simhash_backfill SET finished_at = NOW() WHERE algorithm = $1`, linkerRepairAlgo)
	res, err = f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
	require.NoError(t, err)
	require.Equal(t, LinkerSimhashRepairResult{Restored: 7, Repointed: 2}, res)

	for _, id := range restoredIDs {
		s := f.get(id)
		require.Equal(t, "review_needed", s.Status, "restored where the linker would send it now")
		require.InDelta(t, 0.5, s.Freshness, 1e-6)
		require.Nil(t, s.SupersededBy)
	}
	require.Empty(t, f.edges(farN, farO), "the linker edge of a restored pair is removed")
	for _, id := range keptIDs {
		require.Equal(t, before[id], f.get(id), "legitimate or out-of-scope supersede is kept")
	}
	require.Equal(t, []float64{w2}, f.edges(nearN, nearO))
	require.Equal(t, []float64{w2}, f.edges(olN, olO), "a supersede made without the hash signal keeps its edge")
	for id, holder := range map[uuid.UUID]uuid.UUID{tkO: tkL, twA: twC} {
		got := f.get(id)
		require.Equal(t, "superseded", got.Status, "a row whose key is current elsewhere stays an old version")
		require.Equal(t, holder, *got.SupersededBy, "superseded by the current row of its key")
	}
	require.Empty(t, f.edges(tkN, tkO), "the linker edge of a re-pointed pair is removed")
	// Left over: the linker's own P+T+N supersede, the equal-text one, the
	// archived one, the one made without the hash signal, the triage's
	// equal-hash one and the unanchored one; none rests on the linker's
	// pre-fix hash signal.
	require.Equal(t, 6, f.ac2())

	type logRow struct {
		ID     uuid.UUID `db:"memory_id"`
		Action string    `db:"action"`
		Reason string    `db:"reason"`
		Edge   *float64  `db:"edge_weight"`
		Dist   *int      `db:"hash_distance"`
		Old    *int      `db:"old_hash_distance"`
	}
	var logged []logRow
	require.NoError(t, f.conn.SelectContext(f.ctx, &logged, `SELECT memory_id, action, reason, edge_weight, hash_distance, old_hash_distance FROM memory_linker_simhash_repair_log`))
	byID := map[uuid.UUID]logRow{}
	for _, l := range logged {
		byID[l.ID] = l
	}
	require.Len(t, byID, 9)
	require.Equal(t, "repointed", byID[tkO].Action)
	require.Contains(t, byID[tkO].Reason, "key held by a current row")
	require.Equal(t, "repointed", byID[twA].Action)
	require.Contains(t, byID[twA].Reason, "a newer row of its key comes back")
	require.Equal(t, "restored", byID[farO].Action)
	require.NotNil(t, byID[farO].Edge)
	require.InDelta(t, w, *byID[farO].Edge, 1e-6)
	require.Equal(t, 11, *byID[farO].Dist)
	require.Equal(t, 10, *byID[farO].Old)
	require.Nil(t, byID[bareO].Edge)

	again, err := f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
	require.NoError(t, err)
	require.Equal(t, LinkerSimhashRepairResult{AlreadyDone: true}, again, "runs once")

	// After the repair: one row edited, one moved to active by the triage, one
	// superseded again by the triage.
	f.run(`UPDATE memories SET content = 'edited after the repair' WHERE id = $1`, edO)
	f.run(`UPDATE memories SET status = 'active', freshness_score = 1.0 WHERE id = $1`, trO)
	reS := f.ins("resup-dup", "older text eleven", 0, 4*time.Hour)
	f.run(`UPDATE memories SET status = 'superseded', superseded_by = $2 WHERE id = $1`, reO, reS)

	f.run(f.down)
	for _, id := range []uuid.UUID{farO, bareO, impO, twC, trO, tkO, twA} {
		got := f.get(id)
		require.Equal(t, before[id].Status, got.Status, "Down puts the changed row back")
		require.InDelta(t, before[id].Freshness, got.Freshness, 1e-6)
		require.Equal(t, before[id].SupersededBy, got.SupersededBy)
		require.True(t, before[id].UpdatedAt.Equal(got.UpdatedAt))
	}
	require.Equal(t, []float64{w}, f.edges(farN, farO), "the removed edge is back")
	require.Equal(t, []float64{w}, f.edges(tkN, tkO), "the removed edge of a re-pointed pair is back")
	require.Empty(t, f.edges(bareN, bareO), "no edge invented where none was")
	require.Equal(t, "review_needed", f.get(edO).Status, "Down leaves a row edited since")
	require.Empty(t, f.edges(edN, edO))
	require.Equal(t, reS, *f.get(reO).SupersededBy, "Down leaves a row superseded again since")
	require.Empty(t, f.edges(reN, reO))
	for _, id := range keptIDs {
		require.Equal(t, before[id], f.get(id))
	}
	var tables int
	require.NoError(t, f.conn.GetContext(f.ctx, &tables, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name LIKE 'memory_linker_simhash_repair_%'`))
	require.Zero(t, tables)

	f.run(f.up)
	res, err = f.repo.RepairLinkerSimhashSupersedes(f.ctx, linkerRepairAlgo, 10)
	require.NoError(t, err)
	require.Equal(t, LinkerSimhashRepairResult{Restored: 5, Repointed: 2}, res, "Up and the repair are repeatable after Down")
	require.Equal(t, "review_needed", f.get(farO).Status)
	require.Equal(t, 6, f.ac2())
}
