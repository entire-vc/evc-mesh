package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// Daily memory aggregates (U4) against the migrated test database. Each test
// has its own workspace and its own random past UTC day, so neither the
// journal rows nor the day markers of one test reach another's numbers.

type rollupFx struct {
	t   *testing.T
	db  *sqlx.DB
	fx  coverageFixture
	ctx context.Context
	day time.Time
}

func newRollupFx(t *testing.T) *rollupFx {
	db := userRepoTestDB(t)
	id := uuid.New()
	day := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, (int(id[0])<<8|int(id[1]))%4000)
	f := &rollupFx{t: t, db: db, fx: newCoverageFixture(t, db), ctx: context.Background(), day: day}
	clean := func() {
		_, _ = db.Exec(`DELETE FROM memory_stats_daily WHERE day = $1::date`, day)
		_, _ = db.Exec(`DELETE FROM memory_base_daily WHERE day = $1::date`, day)
		_, _ = db.Exec(`DELETE FROM memory_daily_rollups WHERE day = $1::date`, day)
	}
	clean()
	t.Cleanup(func() {
		clean()
		_, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id = $1`, f.fx.workspaceID)
	})
	return f
}

func (f *rollupFx) agent() uuid.UUID {
	s := uuid.NewString()[:8]
	return seedAgent(f.t, f.db, f.fx.workspaceID, "rollup-"+s, "rollup-"+s, nil).ID
}

func (f *rollupFx) at(h, m int) time.Time {
	return f.day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

type rollupEv struct {
	ts         time.Time
	agent      *uuid.UUID
	project    *uuid.UUID
	callerKind string
	op         string
	query      string
	results    []domain.MemoryReadResult
	chars      int
	err        string
}

func (f *rollupFx) ev(e rollupEv) uuid.UUID {
	if e.callerKind == "" {
		e.callerKind = domain.CallerKindAgentTool
	}
	if e.results == nil {
		e.results = []domain.MemoryReadResult{}
	}
	raw, err := json.Marshal(e.results)
	require.NoError(f.t, err)
	var qh, errText *string
	if e.query != "" {
		qh = &e.query
	}
	if e.err != "" {
		errText = &e.err
	}
	id := uuid.New()
	_, err = f.db.Exec(`
		INSERT INTO memory_read_events (id, ts, workspace_id, project_id, agent_id, caller_kind, route, op,
			query_hash, result_count, results, response_chars, error, usage_final_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'GET /test', $7, $8, $9, $10::jsonb, $11, $12, $2)`,
		id, e.ts, f.fx.workspaceID, e.project, e.agent, e.callerKind, e.op, qh, len(e.results), string(raw), e.chars, errText)
	require.NoError(f.t, err)
	return id
}

func (f *rollupFx) usage(read, memory, agent uuid.UUID, at time.Time) {
	_, err := f.db.Exec(`INSERT INTO memory_read_usage (read_event_id, memory_id, signal, ref_kind, signal_ref, ts, agent_id, caller_kind)
		VALUES ($1, $2, 'get_memory', 'read_event', $3, $4, $5, 'agent_tool')`, read, memory, uuid.New(), at, agent)
	require.NoError(f.t, err)
}

func row(id uuid.UUID, source string, score float64, superseded bool) domain.MemoryReadResult {
	s := score
	return domain.MemoryReadResult{ID: id, Key: "k-" + id.String()[:8], Source: source, Score: &s, Superseded: superseded}
}

type statsSums struct {
	Reads            int64 `db:"reads"`
	Errors           int64 `db:"errors"`
	Hits             int64 `db:"hits"`
	Useful           int64 `db:"useful"`
	EmptyRepeats     int64 `db:"empty_repeats"`
	ServedRows       int64 `db:"served_rows"`
	ServedSuperseded int64 `db:"served_superseded"`
	ResponseChars    int64 `db:"response_chars"`
}

func (f *rollupFx) dailySums() statsSums {
	var s statsSums
	require.NoError(f.t, f.db.Get(&s, `SELECT
		COALESCE(sum(reads),0) AS reads, COALESCE(sum(errors),0) AS errors, COALESCE(sum(hits),0) AS hits,
		COALESCE(sum(useful),0) AS useful, COALESCE(sum(empty_repeats),0) AS empty_repeats,
		COALESCE(sum(served_rows),0) AS served_rows, COALESCE(sum(served_superseded),0) AS served_superseded,
		COALESCE(sum(response_chars),0) AS response_chars
		FROM memory_stats_daily WHERE day = $1::date AND workspace_id = $2`, f.day, f.fx.workspaceID))
	return s
}

// rawSums recomputes the counters straight from memory_read_events and
// memory_read_usage of the day, written independently of memory_stats_rollup.
func (f *rollupFx) rawSums() statsSums {
	var s statsSums
	require.NoError(f.t, f.db.Get(&s, `
		WITH e AS (
			SELECT * FROM memory_read_events
			WHERE workspace_id = $2 AND ts >= $1 AND ts < $1 + interval '1 day'
		), open AS (
			SELECT e.id, x
			FROM e, jsonb_array_elements(e.results) x
			WHERE e.error IS NULL
			  AND (jsonb_typeof(x->'score') IS DISTINCT FROM 'number' OR (x->>'score')::numeric < 2)
		), hit AS (SELECT DISTINCT id FROM open)
		SELECT
			(SELECT count(*) FROM e) AS reads,
			(SELECT count(*) FROM e WHERE error IS NOT NULL) AS errors,
			(SELECT count(*) FROM hit) AS hits,
			(SELECT count(DISTINCT u.read_event_id) FROM memory_read_usage u
			   JOIN hit ON hit.id = u.read_event_id
			   JOIN e ON e.id = u.read_event_id
			  WHERE e.op IN ('recall', 'recall-final', 'recall_graph')) AS useful,
			0::bigint AS empty_repeats,
			(SELECT count(*) FROM open) AS served_rows,
			(SELECT count(*) FROM open WHERE x->>'superseded' = 'true') AS served_superseded,
			(SELECT COALESCE(sum(response_chars), 0) FROM e WHERE error IS NULL) AS response_chars`,
		f.day, f.fx.workspaceID))
	return s
}

// The acceptance check of U4: the sums of a written day equal the sums over
// the raw journal of that day (reads, hits, useful, served_rows,
// served_superseded), with the fixture covering every rule of the contract.
func TestMemoryStatsDaily_ReconcilesWithRawJournal(t *testing.T) {
	f := newRollupFx(t)
	a, b := f.agent(), f.agent()
	p := f.fx.projectID
	m1, m2, m3, m4, m5 := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	// Multi-source hit with a pinned row, a superseded row and a row without
	// a source; two usage rows on the one read.
	e1 := f.ev(rollupEv{ts: f.at(1, 0), agent: &a, project: &p, op: "recall-final", chars: 1000, results: []domain.MemoryReadResult{
		row(m1, "memory", 2.0, false), row(m2, "memory", 0.015, true), row(m3, "project_knowledge", 0.012, false),
		row(m4, "doc", 0.010, false), row(m5, "", 0.005, false)}})
	f.usage(e1, m2, a, f.at(1, 10))
	f.usage(e1, m3, a, f.at(1, 20))
	// Pinned-only page: no hit, and its usage row does not make it useful.
	e2 := f.ev(rollupEv{ts: f.at(1, 30), agent: &a, project: &p, op: "recall-final", chars: 100,
		results: []domain.MemoryReadResult{row(m1, "memory", 2.0, false)}})
	f.usage(e2, m1, a, f.at(1, 40))
	// Failed call: volume only; its characters do not count.
	f.ev(rollupEv{ts: f.at(1, 50), agent: &a, project: &p, op: "recall", chars: 50, err: "boom",
		results: []domain.MemoryReadResult{row(m2, "memory", 0.01, false)}})
	// The same empty question twice the same day: the second is a repeat.
	f.ev(rollupEv{ts: f.at(2, 0), agent: &a, project: &p, op: "recall-final", query: "h1", chars: 10})
	f.ev(rollupEv{ts: f.at(3, 0), agent: &a, project: &p, op: "recall-final", query: "h1", chars: 10})
	// Earlier empty answers: 8 days back is outside the 7-day window, 2 days back inside.
	f.ev(rollupEv{ts: f.day.AddDate(0, 0, -8), agent: &a, project: &p, op: "recall-final", query: "h2"})
	f.ev(rollupEv{ts: f.at(4, 0), agent: &a, project: &p, op: "recall-final", query: "h2", chars: 10})
	f.ev(rollupEv{ts: f.day.AddDate(0, 0, -2), agent: &a, project: &p, op: "recall-final", query: "h3"})
	f.ev(rollupEv{ts: f.at(5, 0), agent: &a, project: &p, op: "recall-final", query: "h3", chars: 10})
	// Another agent asking h1 is not a repeat of agent a.
	f.ev(rollupEv{ts: f.at(5, 30), agent: &b, project: &p, op: "recall-final", query: "h1", chars: 10})
	// Graph recall without a project.
	f.ev(rollupEv{ts: f.at(6, 0), agent: &b, op: "recall_graph", chars: 200,
		results: []domain.MemoryReadResult{row(m2, "memory", 0.02, true)}})
	// Direct reads: one row per call on the op's source.
	f.ev(rollupEv{ts: f.at(7, 0), agent: &a, project: &p, op: "get_memory", chars: 300,
		results: []domain.MemoryReadResult{{ID: m2, Key: "k", Source: "memory", Superseded: true}}})
	f.ev(rollupEv{ts: f.at(8, 0), agent: &a, project: &p, op: "get_project_updates", chars: 400,
		results: []domain.MemoryReadResult{{ID: uuid.New(), Source: "project_update"}, {ID: uuid.New(), Source: "project_update"}}})
	// Caller without an agent.
	f.ev(rollupEv{ts: f.at(9, 0), project: &p, callerKind: domain.CallerKindUnknown, op: "recall-final", chars: 20,
		results: []domain.MemoryReadResult{row(m3, "project_knowledge", 0.013, false)}})
	// Neighbouring days stay out of this day.
	f.ev(rollupEv{ts: f.day.Add(-time.Millisecond), agent: &a, project: &p, op: "recall-final", chars: 7,
		results: []domain.MemoryReadResult{row(m2, "memory", 0.01, false)}})
	f.ev(rollupEv{ts: f.day.AddDate(0, 0, 1), agent: &a, project: &p, op: "recall-final", chars: 7,
		results: []domain.MemoryReadResult{row(m2, "memory", 0.01, false)}})

	repo := NewMemoryDailyRollupRepo(f.db)
	n, err := repo.WriteStatsDay(f.ctx, f.day.Add(13*time.Hour))
	require.NoError(t, err)
	require.Positive(t, n)

	got := f.dailySums()
	raw := f.rawSums()
	raw.EmptyRepeats = got.EmptyRepeats // not a raw column; asserted below
	require.Equal(t, raw, got, "daily sums equal the raw journal of the day")
	require.Equal(t, statsSums{Reads: 12, Errors: 1, Hits: 5, Useful: 1, EmptyRepeats: 2,
		ServedRows: 9, ServedSuperseded: 3, ResponseChars: 2070}, got)

	// Call counters live on '*' for recall ops; served rows on their sources.
	type srcRow struct {
		Op         string `db:"op"`
		Source     string `db:"source"`
		Reads      int64  `db:"reads"`
		ServedRows int64  `db:"served_rows"`
		Superseded int64  `db:"served_superseded"`
	}
	var bySource []srcRow
	require.NoError(t, f.db.Select(&bySource, `SELECT op, source, sum(reads) AS reads, sum(served_rows) AS served_rows,
		sum(served_superseded) AS served_superseded
		FROM memory_stats_daily WHERE day = $1::date AND workspace_id = $2 GROUP BY 1, 2 ORDER BY 1, 2`, f.day, f.fx.workspaceID))
	require.Equal(t, []srcRow{
		{"get_memory", "memory", 1, 1, 1},
		{"get_project_updates", "project_update", 1, 2, 0},
		{"recall", "*", 9, 0, 0},
		{"recall", "doc", 0, 1, 0},
		{"recall", "memory", 0, 1, 1},
		{"recall", "project_knowledge", 0, 2, 0},
		{"recall", "unknown", 0, 1, 0},
		{"recall_with_graph", "*", 1, 0, 0},
		{"recall_with_graph", "memory", 0, 1, 1},
	}, bySource)

	// Top-score histogram: one observation per error-free recall call.
	var buckets []int64
	require.NoError(t, f.db.Select(&buckets, `SELECT sum(s.top_score_hist[i])::bigint FROM generate_series(1, 17) i
		CROSS JOIN memory_stats_daily s WHERE s.day = $1::date AND s.workspace_id = $2 GROUP BY i ORDER BY i`,
		f.day, f.fx.workspaceID))
	want := make([]int64, 17)
	want[0] = 6  // pinned-only and five empty pages observe 0
	want[9] = 1  // 0.013: 0.012 < s <= 0.013
	want[11] = 1 // 0.015: 0.014 < s <= 0.015 (the pinned 2.0 is skipped)
	want[14] = 1 // 0.02: 0.017 < s <= 0.020
	require.Equal(t, want, buckets)

	// Serving noise: the generated column and the rollup agree.
	var noise float64
	require.NoError(t, f.db.Get(&noise, `SELECT served_noise FROM memory_stats_daily
		WHERE day = $1::date AND workspace_id = $2 AND op = 'recall' AND source = 'memory'`, f.day, f.fx.workspaceID))
	require.InDelta(t, 1.0, noise, 1e-9)
	// Misses are collected since U7 (misses_reason NULL); this journal has no
	// memories to miss. Their own fixtures: memory_search_misses_db_test.go.
	var misses, reasoned int64
	require.NoError(t, f.db.QueryRow(`SELECT sum(misses), count(*) FILTER (WHERE misses_reason IS NOT NULL)
		FROM memory_stats_daily WHERE day = $1::date AND workspace_id = $2`, f.day, f.fx.workspaceID).Scan(&misses, &reasoned))
	require.Zero(t, misses)
	require.Zero(t, reasoned)

	// Positive control: an extra raw read must move both sides.
	f.ev(rollupEv{ts: f.at(10, 0), agent: &a, project: &p, op: "recall-final", chars: 1,
		results: []domain.MemoryReadResult{row(m4, "doc", 0.01, false)}})
	require.NotEqual(t, got, f.rawSums())
	_, err = repo.WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	got2 := f.dailySums()
	raw2 := f.rawSums()
	raw2.EmptyRepeats = got2.EmptyRepeats
	require.Equal(t, raw2, got2)
	require.Equal(t, got.Reads+1, got2.Reads)
}

// A repeated run over the same closed day leaves exactly the same rows, and
// they are the rows the live rollup gives for that day.
func TestMemoryStatsDaily_RewriteIsIdempotent(t *testing.T) {
	f := newRollupFx(t)
	a := f.agent()
	p := f.fx.projectID
	m := uuid.New()
	e := f.ev(rollupEv{ts: f.at(10, 0), agent: &a, project: &p, op: "recall-final", chars: 10,
		results: []domain.MemoryReadResult{row(m, "memory", 0.016, true), row(uuid.New(), "doc", 0.01, false)}})
	f.usage(e, m, a, f.at(10, 5))
	f.ev(rollupEv{ts: f.at(11, 0), agent: &a, op: "get_project_knowledge", chars: 5,
		results: []domain.MemoryReadResult{{ID: m, Source: "project_knowledge"}}})

	snapshot := func(q string) string {
		var s string
		require.NoError(t, f.db.Get(&s, `SELECT COALESCE(json_agg(t ORDER BY t::text), '[]')::text FROM (`+q+`) t`,
			f.day, f.fx.workspaceID))
		return s
	}
	const daily = `SELECT ` + memoryStatsColumns + `, served_noise FROM memory_stats_daily WHERE day = $1::date AND workspace_id = $2`
	const live = `SELECT ` + memoryStatsColumns + `, served_noise FROM memory_stats_rollup($1::timestamptz, $1::timestamptz + interval '1 day') WHERE workspace_id = $2`

	repo := NewMemoryDailyRollupRepo(f.db)
	_, err := repo.WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	first := snapshot(daily)
	require.NotEqual(t, "[]", first)
	_, err = repo.WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	require.Equal(t, first, snapshot(daily), "a second run over the same day changes no row")
	require.Equal(t, first, snapshot(live), "the stored day equals the live rollup of the day")

	written, err := repo.StatsWrittenDays(f.ctx, f.day, f.day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.True(t, written[f.day])
}

func TestMemoryTopScoreBounds_MatchPrometheusBuckets(t *testing.T) {
	db := userRepoTestDB(t)
	var bounds pq.Float64Array
	require.NoError(t, db.Get(&bounds, `SELECT memory_top_score_bounds()`))
	require.Equal(t, metrics.MemoryRecallTopScoreBuckets(), []float64(bounds))
}

type baseRow struct {
	ProjectID        *uuid.UUID `db:"project_id"`
	Total            int64      `db:"total"`
	Active           int64      `db:"active"`
	Archived         int64      `db:"archived"`
	SupersededActive int64      `db:"superseded_active"`
	Conflicted       int64      `db:"conflicted"`
	NoKind           int64      `db:"no_kind"`
	StatuslogLike    int64      `db:"statuslog_like"`
	DupGroups        int64      `db:"dup_groups"`
	DupRows          int64      `db:"dup_rows"`
	Dead30           *int64     `db:"dead30"`
	Dead90           *int64     `db:"dead90"`
	Eligible30       *int64     `db:"eligible30"`
	Eligible90       *int64     `db:"eligible90"`
	Dead30Reason     *string    `db:"dead30_reason"`
	Dead90Reason     *string    `db:"dead90_reason"`
}

func i64p(v int64) *int64 { return &v }

func TestMemoryBaseDaily_SnapshotAndDeadWeight(t *testing.T) {
	f := newRollupFx(t)
	p := f.fx.projectID
	end := f.day.AddDate(0, 0, 1)
	old := f.day.AddDate(0, 0, -100)
	mem := func(key string, project *uuid.UUID, tags []string, status string, archived bool, simhash *int64, created time.Time) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, f.db.Get(&id, `INSERT INTO memories (workspace_id, project_id, key, content, scope, tags, status, archived,
			content_simhash, created_at) VALUES ($1, $2, $3, 'c', CASE WHEN $2::uuid IS NULL THEN 'workspace' ELSE 'project' END,
			$4, $5, $6, $7, $8) RETURNING id`,
			f.fx.workspaceID, project, key, pq.Array(tags), status, archived, simhash, created))
		return id
	}
	fact := []string{"kind:fact"}
	var dup int64 = 4242
	b1 := mem("b1", &p, []string{"kind:decision"}, "active", false, nil, old)
	b2 := mem("b2", &p, fact, "active", false, nil, old)
	b3 := mem("b3", &p, nil, "active", false, nil, old)
	mem("b4", &p, fact, "active", true, nil, old)
	mem("b5", &p, fact, "archived", false, nil, old)
	mem("b6", &p, fact, "superseded", false, nil, old)
	b7 := mem("b7", &p, fact, "conflicted", false, nil, old)
	mem("checkpoint-b8", &p, fact, "active", false, nil, f.day.AddDate(0, 0, -10))
	mem("b9", &p, fact, "active", false, &dup, old.Add(-time.Hour))
	mem("b10", &p, fact, "active", false, &dup, old)
	mem("b11", nil, fact, "active", false, &dup, old)
	mem("b12", &p, fact, "active", false, nil, end.Add(time.Hour))
	mem("b13", &p, []string{"kind:session-checkpoint"}, "active", false, nil, old)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, f.fx.workspaceID) })

	a := f.agent()
	served := func(id uuid.UUID, at time.Time, caller string) {
		f.ev(rollupEv{ts: at, agent: &a, callerKind: caller, op: "get_memory",
			results: []domain.MemoryReadResult{{ID: id, Source: "memory"}}})
	}
	served(b1, end.AddDate(0, 0, -5), domain.CallerKindAgentTool)
	served(b2, end.AddDate(0, 0, -60), domain.CallerKindAgentTool)
	served(b7, end.AddDate(0, 0, -1), domain.CallerKindUnknown) // not an agent tool: still dead
	served(b3, end.Add(time.Hour), domain.CallerKindAgentTool)  // after the day: still dead

	repo := NewMemoryDailyRollupRepo(f.db)
	read := func() []baseRow {
		var rows []baseRow
		require.NoError(t, f.db.Select(&rows, `SELECT project_id, total, active, archived, superseded_active, conflicted,
			no_kind, statuslog_like, dup_groups, dup_rows, dead30, dead90, eligible30, eligible90, dead30_reason, dead90_reason
			FROM memory_base_daily WHERE day = $1::date AND workspace_id = $2 ORDER BY project_id NULLS FIRST`, f.day, f.fx.workspaceID))
		return rows
	}
	reset := func() {
		_, _ = f.db.Exec(`DELETE FROM memory_base_daily WHERE day = $1::date`, f.day)
		_, _ = f.db.Exec(`DELETE FROM memory_daily_rollups WHERE day = $1::date`, f.day)
	}

	// Journal covering 90 days.
	since := end.AddDate(0, 0, -100)
	_, wrote, err := repo.WriteBaseDay(f.ctx, f.day, &since)
	require.NoError(t, err)
	require.True(t, wrote)
	require.Equal(t, []baseRow{
		{ProjectID: nil, Total: 1, Active: 1, DupRows: 1,
			Dead30: i64p(1), Dead90: i64p(1), Eligible30: i64p(1), Eligible90: i64p(1)},
		{ProjectID: &p, Total: 11, Active: 9, Archived: 2, SupersededActive: 1, Conflicted: 1, NoKind: 1,
			StatuslogLike: 2, DupGroups: 1, DupRows: 2,
			Dead30: i64p(6), Dead90: i64p(5), Eligible30: i64p(7), Eligible90: i64p(7)},
	}, read())

	// Written once: a later change of the base does not touch the snapshot.
	_, err = f.db.Exec(`UPDATE memories SET archived = true WHERE id = $1`, b1)
	require.NoError(t, err)
	_, wrote, err = repo.WriteBaseDay(f.ctx, f.day, &since)
	require.NoError(t, err)
	require.False(t, wrote)
	require.EqualValues(t, 9, read()[1].Active)
	_, err = f.db.Exec(`UPDATE memories SET archived = false WHERE id = $1`, b1)
	require.NoError(t, err)

	// Journal of 40 days: dead90/eligible90 are NULL with the reason.
	reset()
	since = end.AddDate(0, 0, -40)
	_, _, err = repo.WriteBaseDay(f.ctx, f.day, &since)
	require.NoError(t, err)
	rows := read()
	require.Equal(t, i64p(6), rows[1].Dead30)
	require.Nil(t, rows[1].Dead30Reason)
	require.Nil(t, rows[1].Dead90)
	require.Nil(t, rows[1].Eligible90)
	require.Equal(t, strp("no_journal_before"), rows[1].Dead90Reason)

	// No journal at all: both windows uncovered.
	reset()
	_, _, err = repo.WriteBaseDay(f.ctx, f.day, nil)
	require.NoError(t, err)
	rows = read()
	require.Nil(t, rows[1].Dead30)
	require.Nil(t, rows[1].Eligible30)
	require.Equal(t, strp("no_journal_before"), rows[1].Dead30Reason)
	require.EqualValues(t, 11, rows[1].Total, "the snapshot itself does not need the journal")
}

func TestMemoryWritesDaily_View(t *testing.T) {
	f := newRollupFx(t)
	p := f.fx.projectID
	a := f.agent()
	var kept, gone uuid.UUID
	require.NoError(t, f.db.Get(&kept, `INSERT INTO memories (workspace_id, project_id, key, content, scope)
		VALUES ($1, $2, 'w-kept', 'c', 'project') RETURNING id`, f.fx.workspaceID, p))
	require.NoError(t, f.db.Get(&gone, `INSERT INTO memories (workspace_id, project_id, key, content, scope)
		VALUES ($1, $2, 'w-gone', 'c', 'project') RETURNING id`, f.fx.workspaceID, p))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM memory_revisions WHERE workspace_id = $1`, f.fx.workspaceID)
		_, _ = f.db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, f.fx.workspaceID)
		_, _ = f.db.Exec(`DELETE FROM project_updates WHERE project_id = $1`, p)
	})
	late := f.at(23, 30) // 23:30 UTC is the next day east of UTC
	rev := func(memory uuid.UUID, version int, action string, tags []string, reason *string) {
		_, err := f.db.Exec(`INSERT INTO memory_revisions (memory_id, version, content, tags, action, reason, actor_agent_id, created_at, workspace_id)
			VALUES ($1, $2, 'c', $3, $4, $5, $6, $7, $8)`, memory, version, pq.Array(tags), action, reason, a, late, f.fx.workspaceID)
		require.NoError(t, err)
	}
	rev(kept, 1, "created", []string{"team:x", "kind:decision", "kind:fact"}, nil)
	rev(kept, 2, "updated", []string{"kind:fact"}, strp("fixed a typo"))
	rev(kept, 3, "archived", []string{}, nil)
	rev(gone, 1, "created", []string{"kind:fact"}, nil)
	_, err := f.db.Exec(`DELETE FROM memories WHERE id = $1`, gone)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO project_updates (project_id, title, summary, created_by, created_at)
		VALUES ($1, 't', 's', $2, $3)`, p, a, late)
	require.NoError(t, err)

	type w struct {
		Day        string     `db:"day"`
		ProjectID  *uuid.UUID `db:"project_id"`
		AgentID    *uuid.UUID `db:"agent_id"`
		Action     string     `db:"action"`
		WithReason bool       `db:"with_reason"`
		Kind       *string    `db:"kind"`
		Scope      *string    `db:"scope"`
	}
	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`SET LOCAL TIME ZONE 'Asia/Tokyo'`)
	require.NoError(t, err)
	var rows []w
	require.NoError(t, tx.Select(&rows, `SELECT day::text AS day, project_id, agent_id, action, with_reason, kind, scope
		FROM memory_writes_daily WHERE workspace_id = $1 ORDER BY action, kind NULLS FIRST`, f.fx.workspaceID))
	d := f.day.Format(time.DateOnly)
	require.Len(t, rows, 5)
	want := []w{
		{d, &p, &a, "archived", false, nil, strp("project")},
		{d, &p, &a, "created", false, strp("decision"), strp("project")},
		{d, nil, &a, "created", false, strp("fact"), nil},
		{d, &p, &a, "project_update", false, strp("project-update"), strp("project")},
		{d, &p, &a, "updated", true, strp("fact"), strp("project")},
	}
	require.Equal(t, want, rows)
}

// The readers the nightly job decides on: when the journal starts, which days
// are written, and which recall reads of a day still wait for final usage.
func TestMemoryDailyRollup_JobReaders(t *testing.T) {
	f := newRollupFx(t)
	a := f.agent()
	p := f.fx.projectID
	repo := NewMemoryDailyRollupRepo(f.db)

	pendingRecall := f.ev(rollupEv{ts: f.at(9, 0), agent: &a, project: &p, op: "recall"})
	pendingGet := f.ev(rollupEv{ts: f.at(9, 5), agent: &a, project: &p, op: "get_memory"})
	f.ev(rollupEv{ts: f.at(9, 10), agent: &a, project: &p, op: "recall-final"}) // usage final
	_, err := f.db.Exec(`UPDATE memory_read_events SET usage_final_at = NULL WHERE id = ANY($1)`,
		pq.Array([]uuid.UUID{pendingRecall, pendingGet}))
	require.NoError(t, err)

	since, err := repo.JournalSince(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, since)
	require.False(t, since.After(f.at(9, 0)), "the journal starts no later than the oldest raw read")
	require.Equal(t, time.UTC, since.Location())

	n, err := repo.PendingUsageReads(f.ctx, f.at(23, 0))
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "only recall reads wait for usage; a final one does not")
	n, err = repo.PendingUsageReads(f.ctx, f.day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Zero(t, n, "the next day has no reads")

	_, err = f.db.Exec(`UPDATE memory_read_events SET usage_final_at = ts WHERE id = $1`, pendingRecall)
	require.NoError(t, err)
	n, err = repo.PendingUsageReads(f.ctx, f.day)
	require.NoError(t, err)
	require.Zero(t, n)

	written, err := repo.StatsWrittenDays(f.ctx, f.day, f.day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.False(t, written[f.day], "not written before WriteStatsDay")
	_, err = repo.WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	written, err = repo.StatsWrittenDays(f.ctx, f.day, f.day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.True(t, written[f.day])
	written, err = repo.StatsWrittenDays(f.ctx, f.day.AddDate(0, 0, 1), f.day.AddDate(0, 0, 2))
	require.NoError(t, err)
	require.False(t, written[f.day], "the range end is exclusive of other days")
}
