package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Search misses and repeated empty questions (U7, plan §3 «Промах поиска»,
// migration 20261120500) against the migrated test database. Each scenario
// has its own agent, so the per-agent miss counts of the day are independent;
// the recall usage of the re-found reads comes from the real U3 job
// (MemoryReadUsageRepo.ProcessOnce), not from hand-written usage rows.

type missRead struct {
	agent   uuid.UUID
	ts      time.Time
	op      string
	session string
	task    *uuid.UUID
	query   string
	results []domain.MemoryReadResult
	err     string
}

// read journals a read with its usage still pending (usage_final_at NULL), so
// that the U3 job attributes it.
func (f *rollupFx) missRead(r missRead) uuid.UUID {
	if r.op == "" {
		r.op = "recall-final"
	}
	if r.results == nil {
		r.results = []domain.MemoryReadResult{}
	}
	raw, err := json.Marshal(r.results)
	require.NoError(f.t, err)
	var session, query, errText *string
	if r.session != "" {
		session = &r.session
	}
	if r.query != "" {
		query = &r.query
	}
	if r.err != "" {
		errText = &r.err
	}
	var id uuid.UUID
	require.NoError(f.t, f.db.Get(&id, `
		INSERT INTO memory_read_events (ts, workspace_id, project_id, agent_id, session_id, task_id, caller_kind,
			route, op, query_hash, result_count, results, error)
		VALUES ($1, $2, $3, $4, $5, $6, 'agent_tool', 'GET /test', $7, $8, $9, $10::jsonb, $11) RETURNING id`,
		r.ts, f.fx.workspaceID, f.fx.projectID, r.agent, session, r.task, r.op, query, len(r.results), string(raw), errText))
	return id
}

type missMem struct {
	key      string
	simhash  int64
	created  time.Time
	scope    string
	agent    *uuid.UUID // owner of a scope=agent row
	task     *uuid.UUID // source_task_id
	archived bool
	status   string
	// project overrides the row's project (default: the fixture project the
	// reads are journaled under); noProject leaves it NULL.
	project   *uuid.UUID
	noProject bool
	tags      []string
	// writer, when set, writes the 'created' revision as that agent at created.
	writer *uuid.UUID
}

func (f *rollupFx) missMemory(m missMem) uuid.UUID {
	if m.scope == "" {
		m.scope = "workspace"
	}
	if m.status == "" {
		m.status = "active"
	}
	if m.tags == nil {
		m.tags = []string{"kind:fact"}
	}
	project := &f.fx.projectID
	if m.project != nil {
		project = m.project
	}
	if m.noProject {
		project = nil
	}
	var id uuid.UUID
	require.NoError(f.t, f.db.Get(&id, `
		INSERT INTO memories (workspace_id, project_id, agent_id, key, content, scope, tags, status, archived,
			content_simhash, source_task_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		f.fx.workspaceID, project, m.agent, m.key, "content of "+m.key, m.scope, pq.Array(m.tags),
		m.status, m.archived, m.simhash, m.task, m.created))
	if m.writer != nil {
		f.revision(id, 1, "created", *m.writer, m.created)
	}
	return id
}

func (f *rollupFx) revision(memory uuid.UUID, version int, action string, agent uuid.UUID, at time.Time) {
	_, err := f.db.Exec(`INSERT INTO memory_revisions (memory_id, version, content, action, actor_agent_id, workspace_id, created_at)
		VALUES ($1, $2, 'c', $3, $4, $5, $6)`, memory, version, action, agent, f.fx.workspaceID, at)
	require.NoError(f.t, err)
}

// finishUsage runs the U3 job past every window of the fixture day.
func (f *rollupFx) finishUsage() {
	_, err := NewMemoryReadUsageRepo(f.db).ProcessOnce(f.ctx, f.day.AddDate(0, 0, 2))
	require.NoError(f.t, err)
}

// writeDay writes the fixture day through the nightly writer and returns the
// misses per agent from memory_stats_daily and the number of the day's rows
// whose misses_reason is set (0 once misses are collected).
func (f *rollupFx) writeDay() (misses map[uuid.UUID]int64, reasoned int64) {
	_, err := NewMemoryDailyRollupRepo(f.db).WriteStatsDay(f.ctx, f.day)
	require.NoError(f.t, err)
	var rows []struct {
		Agent    uuid.UUID `db:"agent_id"`
		Misses   int64     `db:"misses"`
		Reasoned int64     `db:"reasoned"`
	}
	require.NoError(f.t, f.db.Select(&rows, `SELECT agent_id, sum(misses) AS misses,
		count(*) FILTER (WHERE misses_reason IS NOT NULL) AS reasoned
		FROM memory_stats_daily WHERE day = $1::date AND workspace_id = $2 GROUP BY agent_id`, f.day, f.fx.workspaceID))
	require.NotEmpty(f.t, rows)
	misses = map[uuid.UUID]int64{}
	for _, r := range rows {
		misses[r.Agent] = r.Misses
		reasoned += r.Reasoned
	}
	return misses, reasoned
}

type missDetail struct {
	Read     uuid.UUID `db:"read_event_id"`
	Memory   uuid.UUID `db:"memory_id"`
	Key      string    `db:"memory_key"`
	Reason   string    `db:"reason"`
	EvKind   string    `db:"evidence_kind"`
	Evidence uuid.UUID `db:"evidence_id"`
	Distance *int      `db:"distance"`
}

func (f *rollupFx) missDetails(agent uuid.UUID) []missDetail {
	var out []missDetail
	require.NoError(f.t, f.db.Select(&out, `SELECT read_event_id, memory_id, memory_key, reason, evidence_kind,
		evidence_id, distance FROM memory_search_misses($1, $2) WHERE agent_id = $3 ORDER BY read_ts, memory_key`,
		f.day, f.day.AddDate(0, 0, 1), agent))
	return out
}

// (a) A recall without m, then in the same session a recall with m that the
// agent uses (get_memory of m): the first recall is a miss.
func TestMemorySearchMisses_Refound(t *testing.T) {
	f := newRollupFx(t)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, f.fx.workspaceID) })
	before := f.day.Add(-time.Hour)
	other := f.missMemory(missMem{key: "miss-other", simhash: 0x0F0F, created: before})
	otherRow := memRow(other, "miss-other", domain.ReadSourceMemory)

	// scenario: r1 lacks m; r2 in the same session returns m; get_memory(m)
	// in the same session makes r2 useful by m.
	type sc struct {
		name       string
		r2Session  string
		r2At       time.Time
		used       bool
		r1HasM     bool
		mCreatedAt time.Time
		mScope     string
		mOwner     bool // scope=agent row of another agent
		archivedAt *time.Time
		mProject   string // "other": another project; "none"/"none-pinned": no project
		want       int64
	}
	other2 := f.at(0, 30)
	elsewhere := uuid.New()
	_, err := f.db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'misses elsewhere', $3)`,
		elsewhere, f.fx.workspaceID, "misses-"+elsewhere.String()[:8])
	require.NoError(t, err)
	cases := []sc{
		{name: "miss: found on the second try", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: before, want: 1},
		{name: "second recall not used", r2Session: "s", r2At: f.at(1, 30), used: false, mCreatedAt: before},
		{name: "second recall in another session", r2Session: "other", r2At: f.at(1, 30), used: true, mCreatedAt: before},
		{name: "second recall after the miss window", r2Session: "s", r2At: f.at(3, 1), used: true, mCreatedAt: before},
		{name: "m was served by r1", r2Session: "s", r2At: f.at(1, 30), used: true, r1HasM: true, mCreatedAt: before},
		{name: "m created after t_r", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: f.at(1, 10)},
		{name: "m is another agent's private row", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: before,
			mScope: "agent", mOwner: true},
		{name: "m archived before t_r", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: before, archivedAt: &other2},
		// The reads are journaled under the fixture project; recall-final
		// shows them that project's rows and pinned workspace rows only.
		{name: "m belongs to another project", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: before,
			mScope: "project", mProject: "other"},
		{name: "m has no project and is not pinned", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: before,
			mProject: "none"},
		{name: "m is a pinned workspace row", r2Session: "s", r2At: f.at(1, 30), used: true, mCreatedAt: before,
			mProject: "none-pinned", want: 1},
	}
	agents := make([]uuid.UUID, len(cases))
	r1s := make([]uuid.UUID, len(cases))
	r2s := make([]uuid.UUID, len(cases))
	mems := make([]uuid.UUID, len(cases))
	for i, c := range cases {
		a := f.agent()
		agents[i] = a
		session := "sess-" + a.String()[:8]
		mm := missMem{key: "miss-a-" + a.String()[:8], simhash: int64(0x7000 + i), created: c.mCreatedAt, scope: c.mScope}
		if c.mOwner {
			owner := f.agent()
			mm.agent = &owner
		}
		switch c.mProject {
		case "other":
			mm.project = &elsewhere
		case "none":
			mm.noProject = true
		case "none-pinned":
			mm.noProject = true
			mm.tags = []string{"kind:pinned"}
		}
		m := f.missMemory(mm)
		mems[i] = m
		mRow := memRow(m, mm.key, domain.ReadSourceMemory)
		if c.archivedAt != nil {
			// Archived at 00:30 and unarchived after r2, so it is live now.
			f.revision(m, 2, "archived", a, *c.archivedAt)
			f.revision(m, 3, "unarchived", a, f.at(5, 0))
		}
		r1res := []domain.MemoryReadResult{otherRow}
		if c.r1HasM {
			r1res = append(r1res, mRow)
		}
		r1s[i] = f.missRead(missRead{agent: a, ts: f.at(1, 0), session: session, results: r1res})
		s2 := session
		if c.r2Session == "other" {
			s2 = "elsewhere-" + a.String()[:8]
		}
		r2s[i] = f.missRead(missRead{agent: a, ts: c.r2At, session: s2, results: []domain.MemoryReadResult{mRow}})
		if c.used {
			f.missRead(missRead{agent: a, ts: c.r2At.Add(5 * time.Minute), session: s2, op: "get_memory",
				results: []domain.MemoryReadResult{mRow}})
		}
	}
	// A second use of m by r2 (the agent updates m): still one evidence row.
	f.revision(mems[0], 2, "updated", agents[0], cases[0].r2At.Add(10*time.Minute))
	f.finishUsage()
	var signals int
	require.NoError(t, f.db.Get(&signals, `SELECT count(*) FROM memory_read_usage WHERE read_event_id = $1`, r2s[0]))
	require.Equal(t, 2, signals, "r2 is useful by m twice: get_memory and revision")

	got, reasoned := f.writeDay()
	for i, c := range cases {
		require.Equal(t, c.want, got[agents[i]], c.name)
	}
	require.Zero(t, reasoned, "misses are collected: misses_reason is NULL")

	d := f.missDetails(agents[0])
	require.Len(t, d, 1, "one row per (read, memory, evidence), whatever the number of usage signals")
	require.Equal(t, missDetail{Read: r1s[0], Memory: mems[0], Key: "miss-a-" + agents[0].String()[:8],
		Reason: "refound", EvKind: "read_event", Evidence: r2s[0]}, d[0])
	for i := 1; i < len(cases); i++ {
		if cases[i].want == 0 {
			require.Empty(t, f.missDetails(agents[i]), cases[i].name)
		}
	}
}

// (b) A recall without m, then the agent writes m' near m by simhash: a miss
// at distance 2, not at distance 6; never for an m created after t_r.
func TestMemorySearchMisses_Rewritten(t *testing.T) {
	f := newRollupFx(t)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, f.fx.workspaceID) })
	before := f.day.Add(-time.Hour)
	task := f.fx.taskID

	type sc struct {
		name      string
		distance  int // bits flipped between m and m'
		mCreated  time.Time
		writtenAt time.Time
		byOther   bool       // m' written by another agent
		rTask     *uuid.UUID // r's card
		mPTask    *uuid.UUID // m' source_task_id
		want      int64
	}
	otherTask := uuid.New()
	cases := []sc{
		{name: "distance 2: miss", distance: 2, mCreated: before, writtenAt: f.at(1, 20), want: 1},
		{name: "distance 3: miss", distance: 3, mCreated: before, writtenAt: f.at(1, 20), want: 1},
		{name: "distance 6: no miss", distance: 6, mCreated: before, writtenAt: f.at(1, 20)},
		{name: "m created after t_r", distance: 2, mCreated: f.at(1, 10), writtenAt: f.at(1, 20)},
		{name: "m' by another agent", distance: 2, mCreated: before, writtenAt: f.at(1, 20), byOther: true},
		{name: "m' after the miss window", distance: 2, mCreated: before, writtenAt: f.at(3, 1)},
		{name: "m' written for another card", distance: 2, mCreated: before, writtenAt: f.at(1, 20),
			rTask: &task, mPTask: &otherTask},
		{name: "m' written for the same card", distance: 2, mCreated: before, writtenAt: f.at(1, 20),
			rTask: &task, mPTask: &task, want: 1},
	}
	agents := make([]uuid.UUID, len(cases))
	reads := make([]uuid.UUID, len(cases))
	mems := make([]uuid.UUID, len(cases))
	primes := make([]uuid.UUID, len(cases))
	for i, c := range cases {
		a := f.agent()
		agents[i] = a
		// Far apart base hashes per scenario: no scenario's m' lands near another's m.
		base := int64(i+1) * 0x0101010101 << 8
		m := f.missMemory(missMem{key: "miss-b-" + a.String()[:8], simhash: base, created: c.mCreated})
		mems[i] = m
		reads[i] = f.missRead(missRead{agent: a, ts: f.at(1, 0), task: c.rTask})
		writer := a
		if c.byOther {
			writer = f.agent()
		}
		flipped := base ^ (int64(1)<<c.distance - 1)
		primes[i] = f.missMemory(missMem{key: "miss-b2-" + a.String()[:8], simhash: flipped, created: c.writtenAt,
			task: c.mPTask, writer: &writer})
	}
	f.finishUsage()

	got, reasoned := f.writeDay()
	for i, c := range cases {
		require.Equal(t, c.want, got[agents[i]], c.name)
	}
	require.Zero(t, reasoned, "misses are collected: misses_reason is NULL")

	two := 2
	d := f.missDetails(agents[0])
	require.Len(t, d, 1)
	require.Equal(t, missDetail{Read: reads[0], Memory: mems[0], Key: "miss-b-" + agents[0].String()[:8],
		Reason: "rewritten", EvKind: "memory", Evidence: primes[0], Distance: &two}, d[0])
}

// Repeated questions: the same query_hash of the same agent with an empty
// answer twice within 7 days is one repeat; once is none.
func TestMemoryEmptyRepeats_TwiceInSevenDays(t *testing.T) {
	f := newRollupFx(t)
	twice, once, apart := f.agent(), f.agent(), f.agent()
	f.missRead(missRead{agent: twice, ts: f.day.AddDate(0, 0, -6), query: "q-twice"})
	f.missRead(missRead{agent: twice, ts: f.at(2, 0), query: "q-twice"})
	f.missRead(missRead{agent: once, ts: f.at(2, 0), query: "q-once"})
	f.missRead(missRead{agent: apart, ts: f.day.AddDate(0, 0, -8), query: "q-apart"})
	f.missRead(missRead{agent: apart, ts: f.at(2, 0), query: "q-apart"})
	f.finishUsage()

	_, err := NewMemoryDailyRollupRepo(f.db).WriteStatsDay(f.ctx, f.day)
	require.NoError(t, err)
	var rows []struct {
		Agent   uuid.UUID `db:"agent_id"`
		Repeats int64     `db:"empty_repeats"`
	}
	require.NoError(t, f.db.Select(&rows, `SELECT agent_id, sum(empty_repeats) AS empty_repeats FROM memory_stats_daily
		WHERE day = $1::date AND workspace_id = $2 GROUP BY agent_id`, f.day, f.fx.workspaceID))
	got := map[uuid.UUID]int64{}
	for _, r := range rows {
		got[r.Agent] = r.Repeats
	}
	require.Equal(t, map[uuid.UUID]int64{twice: 1, once: 0, apart: 0}, got)
}

// The nightly writer waits for the usage of the reads in the miss window
// after the day: a pending recall at 01:00 of the next day holds the day.
func TestMemoryDailyRollup_PendingCoversMissWindow(t *testing.T) {
	f := newRollupFx(t)
	a := f.agent()
	next := f.day.AddDate(0, 0, 1)
	f.missRead(missRead{agent: a, ts: next.Add(time.Hour)})
	repo := NewMemoryDailyRollupRepo(f.db)
	n, err := repo.PendingUsageReads(f.ctx, f.day)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	f.missRead(missRead{agent: a, ts: next.Add(domain.MemoryMissWindow)})
	n, err = repo.PendingUsageReads(f.ctx, f.day)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "a read after the miss window does not hold the day")
}
