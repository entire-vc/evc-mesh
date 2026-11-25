package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Recall usage attribution (U3) against the migrated test database. Every case
// has its own agent, so the agent-and-window fallback of S(r) never lets one
// case's signals reach another's read.

type usageFx struct {
	t   *testing.T
	db  *sqlx.DB
	fx  coverageFixture
	ctx context.Context
}

func newUsageFx(t *testing.T) *usageFx {
	db := userRepoTestDB(t)
	return &usageFx{t: t, db: db, fx: newCoverageFixture(t, db), ctx: context.Background()}
}

func (u *usageFx) agent() uuid.UUID {
	s := uuid.NewString()[:8]
	return seedAgent(u.t, u.db, u.fx.workspaceID, "usage-"+s, "usage-"+s, nil).ID
}

func (u *usageFx) memory(key string) uuid.UUID {
	var id uuid.UUID
	require.NoError(u.t, u.db.Get(&id, `
		INSERT INTO memories (workspace_id, key, content, scope) VALUES ($1, $2, $3, 'workspace') RETURNING id`,
		u.fx.workspaceID, key, "content of "+key))
	return id
}

type usageRead struct {
	agent      uuid.UUID
	ts         time.Time
	op         string
	session    *string
	task       *uuid.UUID
	callerKind string
	results    []domain.MemoryReadResult
	err        *string
}

func (u *usageFx) read(r usageRead) uuid.UUID {
	if r.op == "" {
		r.op = "recall-final"
	}
	if r.callerKind == "" {
		r.callerKind = domain.CallerKindAgentTool
	}
	raw, err := json.Marshal(r.results)
	require.NoError(u.t, err)
	var id uuid.UUID
	require.NoError(u.t, u.db.Get(&id, `
		INSERT INTO memory_read_events (ts, workspace_id, agent_id, session_id, task_id, caller_kind, route, op, result_count, results, error)
		VALUES ($1, $2, $3, $4, $5, $6, 'GET /api/v1/memories/recall-final/v1', $7, $8, $9::jsonb, $10) RETURNING id`,
		r.ts, u.fx.workspaceID, r.agent, r.session, r.task, r.callerKind, r.op, len(r.results), string(raw), r.err))
	return id
}

func (u *usageFx) comment(agent, task uuid.UUID, body string, at time.Time) uuid.UUID {
	c := &domain.Comment{ID: uuid.New(), TaskID: task, AuthorID: agent, AuthorType: domain.ActorTypeAgent,
		Body: body, Metadata: json.RawMessage(`{}`), CreatedAt: at, UpdatedAt: at}
	require.NoError(u.t, NewCommentRepo(u.db).Create(u.ctx, c))
	return c.ID
}

type usageRow struct {
	MemoryID   uuid.UUID `db:"memory_id"`
	Signal     string    `db:"signal"`
	RefKind    string    `db:"ref_kind"`
	SignalRef  uuid.UUID `db:"signal_ref"`
	CallerKind string    `db:"caller_kind"`
	AgentID    uuid.UUID `db:"agent_id"`
	TS         time.Time `db:"ts"`
}

func (u *usageFx) usage(readID uuid.UUID) []usageRow {
	var rows []usageRow
	require.NoError(u.t, u.db.Select(&rows, `
		SELECT memory_id, signal, ref_kind, signal_ref, caller_kind, agent_id, ts
		FROM memory_read_usage WHERE read_event_id = $1 ORDER BY signal, ref_kind`, readID))
	return rows
}

func (u *usageFx) finalAt(readID uuid.UUID) *time.Time {
	var at *time.Time
	require.NoError(u.t, u.db.Get(&at, `SELECT usage_final_at FROM memory_read_events WHERE id = $1`, readID))
	return at
}

func memRow(id uuid.UUID, key, source string) domain.MemoryReadResult {
	return domain.MemoryReadResult{ID: id, Key: key, Source: source, Rank: 1}
}

func TestMemoryReadUsageDB_AcceptanceFixtures(t *testing.T) {
	u := newUsageFx(t)
	repo := NewMemoryReadUsageRepo(u.db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	sfx := u.fx.suffix

	// (a) recall, then get_memory by key 10 minutes later: useful.
	agA := u.agent()
	keyA := "decision-usage-a-" + sfx
	memA := u.memory(keyA)
	readA := u.read(usageRead{agent: agA, ts: now.Add(-40 * time.Minute), results: []domain.MemoryReadResult{memRow(memA, keyA, domain.ReadSourceMemory)}})
	getA := u.read(usageRead{agent: agA, ts: now.Add(-30 * time.Minute), op: "get_memory",
		results: []domain.MemoryReadResult{{ID: memA, Key: keyA, Source: domain.ReadSourceMemory, Rank: 1}}})

	// (b) the key in a comment one hour later: useful.
	agB := u.agent()
	keyB := "lesson-usage-b-" + sfx
	memB := u.memory(keyB)
	readB := u.read(usageRead{agent: agB, ts: now.Add(-90 * time.Minute), results: []domain.MemoryReadResult{memRow(memB, keyB, domain.ReadSourceProjectKnowledge)}})
	commentB := u.comment(agB, u.fx.taskID, "по записи "+keyB+" делаю так", now.Add(-30*time.Minute))

	// (b) the 8-hex id prefix in finish_task evidence: useful. The comment is
	// written by finish_task's own writer, with its server-side timestamp.
	agC := u.agent()
	keyC := "fact-usage-c-" + sfx
	memC := u.memory(keyC)
	readC := u.read(usageRead{agent: agC, ts: now.Add(-20 * time.Minute), results: []domain.MemoryReadResult{memRow(memC, keyC, domain.ReadSourceMemory)}})
	evidence := &domain.Comment{TaskID: u.fx.taskID, AuthorID: agC, AuthorType: domain.ActorTypeAgent,
		Body: "**Result: done** · result kind `change`\n\nAC1: запись #" + memC.String()[:8] + " учтена"}
	tx, err := u.db.BeginTxx(u.ctx, nil)
	require.NoError(t, err)
	require.NoError(t, insertCommandComment(u.ctx, tx, evidence))
	require.NoError(t, tx.Commit())

	// (c) archive of the record by the same agent: useful.
	agD := u.agent()
	keyD := "status-usage-d-" + sfx
	memD := u.memory(keyD)
	readD := u.read(usageRead{agent: agD, ts: now.Add(-15 * time.Minute), results: []domain.MemoryReadResult{memRow(memD, keyD, domain.ReadSourceMemory)}})
	_, err = NewMemoryRepo(u.db).SetArchivedWithRevision(u.ctx, memD, true, "устарело", &agD, nil)
	require.NoError(t, err)

	// Mention three hours later: not useful.
	agE := u.agent()
	keyE := "decision-usage-e-" + sfx
	memE := u.memory(keyE)
	readE := u.read(usageRead{agent: agE, ts: now.Add(-4 * time.Hour), results: []domain.MemoryReadResult{memRow(memE, keyE, domain.ReadSourceMemory)}})
	u.comment(agE, u.fx.taskID, "вспомнил "+keyE, now.Add(-1*time.Hour))

	// Mention by another agent: not useful.
	agF, agF2 := u.agent(), u.agent()
	keyF := "decision-usage-f-" + sfx
	memF := u.memory(keyF)
	readF := u.read(usageRead{agent: agF, ts: now.Add(-30 * time.Minute), results: []domain.MemoryReadResult{memRow(memF, keyF, domain.ReadSourceMemory)}})
	u.comment(agF2, u.fx.taskID, "по "+keyF+" и #"+memF.String()[:8], now.Add(-20*time.Minute))

	// No mention (the agent writes, but not about the record): not useful.
	agG := u.agent()
	keyG := "decision-usage-g-" + sfx
	memG := u.memory(keyG)
	readG := u.read(usageRead{agent: agG, ts: now.Add(-30 * time.Minute), results: []domain.MemoryReadResult{memRow(memG, keyG, domain.ReadSourceMemory)}})
	u.comment(agG, u.fx.taskID, "сделал по-другому, "+keyG+"-v2 тут ни при чём", now.Add(-20*time.Minute))

	// First pass at "now": every read but E is still inside W(r)+grace.
	_, err = repo.ProcessOnce(u.ctx, now.Add(time.Second))
	require.NoError(t, err)
	require.Nil(t, u.finalAt(readA), "a read inside its window stays open")
	require.NotNil(t, u.finalAt(readE), "a read past its window is finalized")
	rowsA := u.usage(readA)
	require.Len(t, rowsA, 1, "open reads already carry the usage found so far")

	// Final pass after every window closed; a repeat pass adds nothing.
	later := now.Add(3 * time.Hour)
	_, err = repo.ProcessOnce(u.ctx, later)
	require.NoError(t, err)
	again, err := repo.ProcessOnce(u.ctx, later)
	require.NoError(t, err)
	require.Zero(t, again.Inserted)
	for _, id := range []uuid.UUID{readA, readB, readC, readD, readE, readF, readG} {
		require.NotNil(t, u.finalAt(id))
	}

	rowsA = u.usage(readA)
	require.Equal(t, []usageRow{{MemoryID: memA, Signal: domain.UsageSignalGetMemory, RefKind: domain.UsageRefReadEvent, SignalRef: getA,
		CallerKind: domain.CallerKindAgentTool, AgentID: agA, TS: rowsA[0].TS}}, rowsA)

	rowsB := u.usage(readB)
	require.Len(t, rowsB, 1)
	require.Equal(t, domain.UsageSignalMention, rowsB[0].Signal)
	require.Equal(t, domain.UsageRefComment, rowsB[0].RefKind)
	require.Equal(t, commentB, rowsB[0].SignalRef)
	require.Equal(t, memB, rowsB[0].MemoryID)

	rowsC := u.usage(readC)
	require.Len(t, rowsC, 1)
	require.Equal(t, domain.UsageSignalMention, rowsC[0].Signal)
	require.Equal(t, evidence.ID, rowsC[0].SignalRef)

	rowsD := u.usage(readD)
	require.Len(t, rowsD, 1)
	require.Equal(t, domain.UsageSignalRevision, rowsD[0].Signal)
	require.Equal(t, domain.UsageRefMemoryRevision, rowsD[0].RefKind)
	var archivedRev uuid.UUID
	require.NoError(t, u.db.Get(&archivedRev, `SELECT id FROM memory_revisions WHERE memory_id = $1 AND action = 'archived'`, memD))
	require.Equal(t, archivedRev, rowsD[0].SignalRef)

	require.Empty(t, u.usage(readE), "mention after 3h")
	require.Empty(t, u.usage(readF), "mention by another agent")
	require.Empty(t, u.usage(readG), "no mention")
}

func TestMemoryReadUsageDB_OtherSourcesSessionsAndCallers(t *testing.T) {
	u := newUsageFx(t)
	repo := NewMemoryReadUsageRepo(u.db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	sfx := u.fx.suffix
	t0 := now.Add(-time.Hour)

	// Task description edit naming the full id: mention, ref_kind task.
	agT := u.agent()
	memT := u.memory("decision-desc-" + sfx)
	readT := u.read(usageRead{agent: agT, ts: t0, results: []domain.MemoryReadResult{memRow(memT, "decision-desc-"+sfx, domain.ReadSourceMemory)}})
	changes, _ := json.Marshal(map[string]any{"description": map[string]any{"old": "", "new": "основание: " + memT.String()}})
	_, err := u.db.Exec(`INSERT INTO activity_log (id, workspace_id, entity_type, entity_id, action, actor_id, actor_type, changes, created_at)
		VALUES ($1, $2, 'task', $3, 'task.updated', $4, 'agent', $5, $6)`, uuid.New(), u.fx.workspaceID, u.fx.taskID, agT, string(changes), t0.Add(5*time.Minute))
	require.NoError(t, err)

	// Mesh Doc written by the agent naming the key: mention, ref_kind document.
	agDoc := u.agent()
	keyDoc := "decision-doc-" + sfx
	memDoc := u.memory(keyDoc)
	readDoc := u.read(usageRead{agent: agDoc, ts: t0, results: []domain.MemoryReadResult{memRow(memDoc, keyDoc, domain.ReadSourceMemory)}})
	docID := uuid.New()
	_, err = u.db.Exec(`INSERT INTO documents (id, project_id, slug, title, storage_key, created_by, created_by_type, created_at, updated_at, search_text)
		VALUES ($1, $2, $3, 'usage doc', $4, $5, 'agent', $6, $6, $7)`,
		docID, u.fx.projectID, "usage-doc-"+sfx, "docs/"+docID.String(), agDoc, t0.Add(10*time.Minute), "Опираемся на "+keyDoc+".")
	require.NoError(t, err)

	// remember content naming the key (another memory): mention, ref_kind memory_revision.
	// A supersede by a memory the agent wrote: revision.
	agRem := u.agent()
	keyRem := "decision-rem-" + sfx
	memRem := u.memory(keyRem)
	memSup := u.memory("decision-sup-" + sfx)
	readRem := u.read(usageRead{agent: agRem, ts: t0, results: []domain.MemoryReadResult{
		memRow(memRem, keyRem, domain.ReadSourceMemory), memRow(memSup, "decision-sup-"+sfx, domain.ReadSourceMemory)}})
	memNew := u.memory("decision-new-" + sfx)
	var remRev, supRev uuid.UUID
	require.NoError(t, u.db.Get(&remRev, `INSERT INTO memory_revisions (memory_id, version, content, action, actor_agent_id, workspace_id, created_at)
		VALUES ($1, 1, $2, 'created', $3, $4, $5) RETURNING id`, memNew, "уточняет "+keyRem, agRem, u.fx.workspaceID, t0.Add(15*time.Minute)))
	supRev = remRev // the creating revision of memNew is also the supersede signal for memSup
	_, err = u.db.Exec(`UPDATE memories SET status = 'superseded', superseded_by = $2 WHERE id = $1`, memSup, memNew)
	require.NoError(t, err)

	// Session tiers. With a session id, a get_memory of another session does not count.
	agS := u.agent()
	keyS := "decision-sess-" + sfx
	memS := u.memory(keyS)
	s1, s2 := "sess-1-"+sfx, "sess-2-"+sfx
	readS := u.read(usageRead{agent: agS, ts: t0, session: &s1, results: []domain.MemoryReadResult{memRow(memS, keyS, domain.ReadSourceMemory)}})
	u.read(usageRead{agent: agS, ts: t0.Add(time.Minute), session: &s2, op: "get_memory", results: []domain.MemoryReadResult{memRow(memS, keyS, domain.ReadSourceMemory)}})
	readS2 := u.read(usageRead{agent: agS, ts: t0.Add(2 * time.Minute), session: &s2, results: []domain.MemoryReadResult{memRow(memS, keyS, domain.ReadSourceMemory)}})

	// Without a session, a comment on another card does not count; on the read's card it does.
	agK := u.agent()
	keyK := "decision-task-" + sfx
	memK := u.memory(keyK)
	otherTask := newCoverageTask(t, u.db, u.fx, "usage other task")
	readK := u.read(usageRead{agent: agK, ts: t0, task: &u.fx.taskID, results: []domain.MemoryReadResult{memRow(memK, keyK, domain.ReadSourceMemory)}})
	u.comment(agK, otherTask, "другая карточка: "+keyK, t0.Add(time.Minute))
	agK2 := u.agent()
	readK2 := u.read(usageRead{agent: agK2, ts: t0, task: &u.fx.taskID, results: []domain.MemoryReadResult{memRow(memK, keyK, domain.ReadSourceMemory)}})
	onCard := u.comment(agK2, u.fx.taskID, "эта карточка: "+keyK, t0.Add(time.Minute))

	// A read placed in the prompt by a runner (caller_kind runner) is attributed with its kind.
	agR := u.agent()
	keyR := "decision-runner-" + sfx
	memR := u.memory(keyR)
	readR := u.read(usageRead{agent: agR, ts: t0, callerKind: domain.CallerKindRunner, results: []domain.MemoryReadResult{memRow(memR, keyR, domain.ReadSourceMemory)}})
	u.comment(agR, u.fx.taskID, keyR, t0.Add(time.Minute))

	// A doc row of the answer and a failed read are never attributed.
	agX := u.agent()
	keyX := "decision-docrow-" + sfx
	memX := u.memory(keyX)
	readX := u.read(usageRead{agent: agX, ts: t0, results: []domain.MemoryReadResult{memRow(memX, keyX, domain.ReadSourceDoc)}})
	failMsg := "boom"
	readErr := u.read(usageRead{agent: agX, ts: t0, err: &failMsg, results: []domain.MemoryReadResult{memRow(memX, keyX, domain.ReadSourceMemory)}})
	u.comment(agX, u.fx.taskID, keyX, t0.Add(time.Minute))

	// Window edges: a mention at t_r exactly does not count, at t_r + 2h it does.
	agW, agW2 := u.agent(), u.agent()
	keyW := "decision-edge-" + sfx
	memW := u.memory(keyW)
	tw := now.Add(-3 * time.Hour)
	readW := u.read(usageRead{agent: agW, ts: tw, results: []domain.MemoryReadResult{memRow(memW, keyW, domain.ReadSourceMemory)}})
	u.comment(agW, u.fx.taskID, keyW, tw)
	readW2 := u.read(usageRead{agent: agW2, ts: tw, results: []domain.MemoryReadResult{memRow(memW, keyW, domain.ReadSourceMemory)}})
	u.comment(agW2, u.fx.taskID, keyW, tw.Add(domain.UsageWindow))

	_, err = repo.ProcessOnce(u.ctx, now.Add(3*time.Hour))
	require.NoError(t, err)

	one := func(readID uuid.UUID, signal, refKind string, ref uuid.UUID) usageRow {
		t.Helper()
		rows := u.usage(readID)
		require.Len(t, rows, 1, "read %s", readID)
		require.Equal(t, signal, rows[0].Signal)
		require.Equal(t, refKind, rows[0].RefKind)
		require.Equal(t, ref, rows[0].SignalRef)
		return rows[0]
	}
	one(readT, domain.UsageSignalMention, domain.UsageRefTask, u.fx.taskID)
	one(readDoc, domain.UsageSignalMention, domain.UsageRefDocument, docID)

	remRows := u.usage(readRem)
	require.ElementsMatch(t, []string{memRem.String() + "/mention/" + remRev.String(), memSup.String() + "/revision/" + supRev.String()},
		[]string{remRows[0].MemoryID.String() + "/" + remRows[0].Signal + "/" + remRows[0].SignalRef.String(),
			remRows[1].MemoryID.String() + "/" + remRows[1].Signal + "/" + remRows[1].SignalRef.String()})

	require.Empty(t, u.usage(readS), "get_memory of another session")
	require.Len(t, u.usage(readS2), 0, "the get_memory came before this read")
	require.Empty(t, u.usage(readK), "comment on another card")
	one(readK2, domain.UsageSignalMention, domain.UsageRefComment, onCard)
	require.Equal(t, domain.CallerKindRunner, u.usage(readR)[0].CallerKind)
	require.Empty(t, u.usage(readX), "doc rows are not attributed")
	require.Empty(t, u.usage(readErr), "failed reads are not attributed")
	require.NotNil(t, u.finalAt(readErr))
	require.Empty(t, u.usage(readW), "a signal at t_r is outside (t_r, t_r+2h]")
	require.Len(t, u.usage(readW2), 1, "a signal at t_r+2h is inside")
}

func TestMemoryReadUsageDB_MigrationDown(t *testing.T) {
	admin := userRepoTestDB(t)
	schema := "memoryusage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := sqlx.Connect("postgres", dsnWithSearchPath(t, userRepoTestDSN(), schema))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	halves := func(path string) (string, string) {
		raw, rErr := os.ReadFile(path)
		require.NoError(t, rErr)
		parts := strings.Split(string(raw), "-- +goose Down")
		require.Len(t, parts, 2)
		return parts[0], parts[1]
	}
	u1Up, _ := halves("../../../migrations/20261119600_memory_read_events.sql")
	u3Up, u3Down := halves("../../../migrations/20261120300_memory_read_usage.sql")
	_, err = db.Exec(u1Up)
	require.NoError(t, err)
	_, err = db.Exec(u3Up)
	require.NoError(t, err)
	var readID uuid.UUID
	require.NoError(t, db.Get(&readID, `INSERT INTO memory_read_events (route, op) VALUES ('r', 'recall') RETURNING id`))
	_, err = db.Exec(`INSERT INTO memory_read_usage (read_event_id, memory_id, signal, ref_kind, signal_ref, ts, agent_id)
		VALUES ($1, $2, 'mention', 'comment', $3, now(), $4)`, readID, uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	// The journal purge takes the usage rows with it.
	_, err = db.Exec(`DELETE FROM memory_read_events WHERE id = $1`, readID)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM memory_read_usage`))
	require.Zero(t, n)

	_, err = db.Exec(u3Down)
	require.NoError(t, err)
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'memory_read_usage'`, schema))
	require.Zero(t, n)
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'memory_read_events' AND column_name = 'usage_final_at'`, schema))
	require.Zero(t, n)
	_, err = db.Exec(u3Up)
	require.NoError(t, err, "up again after down")
}
