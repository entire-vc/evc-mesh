package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MemoryReadUsageRepo attributes recall answers to their use
// (memory_read_usage, plan §3 «Полезный recall»). See
// migrations/20261120300_memory_read_usage.sql for the signals.
type MemoryReadUsageRepo struct {
	db *sqlx.DB
	// BatchSize bounds the reads one pass loads; Grace is how long after
	// W(r) closes a read stays open, so late journal flushes (15 s batches)
	// and asynchronous document indexing still land in it.
	BatchSize int
	Grace     time.Duration
}

// NewMemoryReadUsageRepo creates a MemoryReadUsageRepo.
func NewMemoryReadUsageRepo(db *sqlx.DB) *MemoryReadUsageRepo {
	return &MemoryReadUsageRepo{db: db, BatchSize: 500, Grace: 5 * time.Minute}
}

// UsagePassResult is what one ProcessOnce did.
type UsagePassResult struct {
	Reads     int   // recall reads evaluated
	Inserted  int64 // new memory_read_usage rows
	Finalized int64 // reads whose window closed in this pass
}

type pendingUsageRead struct {
	ID         uuid.UUID      `db:"id"`
	TS         time.Time      `db:"ts"`
	AgentID    *uuid.UUID     `db:"agent_id"`
	SessionID  sql.NullString `db:"session_id"`
	TaskID     *uuid.UUID     `db:"task_id"`
	CallerKind string         `db:"caller_kind"`
	Results    []byte         `db:"results"`
	Failed     bool           `db:"failed"`
}

// ProcessOnce evaluates every recall read whose usage is not final, as of
// now: it writes the usage rows found so far (idempotent) and stamps
// usage_final_at on the reads whose window closed (ts + 2h + Grace <= now).
// Reads still inside their window are evaluated again by the next pass, so a
// signal is caught while its source still shows it. It loops until no
// pending read can be finalized.
func (r *MemoryReadUsageRepo) ProcessOnce(ctx context.Context, now time.Time) (UsagePassResult, error) {
	var total UsagePassResult
	var after time.Time
	var afterID uuid.UUID
	for {
		res, lastTS, lastID, full, err := r.processBatch(ctx, now, after, afterID)
		total.Reads += res.Reads
		total.Inserted += res.Inserted
		total.Finalized += res.Finalized
		if err != nil || !full {
			return total, err
		}
		after, afterID = lastTS, lastID
	}
}

func (r *MemoryReadUsageRepo) processBatch(ctx context.Context, now, after time.Time, afterID uuid.UUID) (res UsagePassResult, lastTS time.Time, lastID uuid.UUID, full bool, err error) {
	batch := r.BatchSize
	if batch <= 0 {
		batch = 500
	}
	var pending []pendingUsageRead
	if err = r.db.SelectContext(ctx, &pending, `
		SELECT id, ts, agent_id, session_id, task_id, caller_kind, results,
		       (error IS NOT NULL) AS failed
		FROM memory_read_events
		WHERE usage_final_at IS NULL
		  AND op IN ('recall', 'recall-final', 'recall_graph')
		  AND ts <= $1
		  AND (ts, id) > ($2, $3)
		ORDER BY ts, id
		LIMIT $4`, now, after, afterID, batch); err != nil {
		return res, lastTS, lastID, false, fmt.Errorf("load pending recall reads: %w", err)
	}
	if len(pending) == 0 {
		return res, lastTS, lastID, false, nil
	}
	last := pending[len(pending)-1]
	lastTS, lastID, full = last.TS, last.ID, len(pending) == batch

	closeBefore := now.Add(-domain.UsageWindow - r.Grace)
	var reads []domain.UsageRead
	var closing []uuid.UUID
	for _, p := range pending {
		// A read without an agent or with an error is never attributed: close it.
		if p.AgentID == nil || p.Failed {
			closing = append(closing, p.ID)
			continue
		}
		if !p.TS.After(closeBefore) {
			closing = append(closing, p.ID)
		}
		var results []domain.MemoryReadResult
		if len(p.Results) > 0 {
			if uErr := json.Unmarshal(p.Results, &results); uErr != nil {
				return res, lastTS, lastID, false, fmt.Errorf("decode results of read %s: %w", p.ID, uErr)
			}
		}
		ur := domain.UsageRead{ID: p.ID, TS: p.TS, AgentID: *p.AgentID, TaskID: p.TaskID, CallerKind: p.CallerKind, Results: results}
		if p.SessionID.Valid && p.SessionID.String != "" {
			s := p.SessionID.String
			ur.SessionID = &s
		}
		reads = append(reads, ur)
	}
	res.Reads = len(reads)

	var usage []domain.MemoryReadUsage
	if len(reads) > 0 {
		sig, lErr := r.loadUsageSignals(ctx, reads, now)
		if lErr != nil {
			return res, lastTS, lastID, false, lErr
		}
		usage = domain.AttributeMemoryReadUsage(reads, sig)
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return res, lastTS, lastID, false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, u := range usage {
		callerKind := u.CallerKind
		if !domain.ValidCallerKind(callerKind) {
			callerKind = domain.CallerKindUnknown
		}
		out, iErr := tx.ExecContext(ctx, `
			INSERT INTO memory_read_usage (read_event_id, memory_id, signal, ref_kind, signal_ref, ts, agent_id, caller_kind)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (read_event_id, memory_id, signal, signal_ref) DO NOTHING`,
			u.ReadEventID, u.MemoryID, u.Signal, u.RefKind, u.SignalRef, u.TS, u.AgentID, callerKind)
		if iErr != nil {
			return res, lastTS, lastID, false, fmt.Errorf("insert memory_read_usage: %w", iErr)
		}
		n, _ := out.RowsAffected()
		res.Inserted += n
	}
	if len(closing) > 0 {
		out, uErr := tx.ExecContext(ctx,
			`UPDATE memory_read_events SET usage_final_at = $2 WHERE id = ANY($1) AND usage_final_at IS NULL`,
			pq.Array(uuidStrings(closing)), now)
		if uErr != nil {
			return res, lastTS, lastID, false, fmt.Errorf("finalize recall reads: %w", uErr)
		}
		res.Finalized, _ = out.RowsAffected()
	}
	if cErr := tx.Commit(); cErr != nil {
		return res, lastTS, lastID, false, cErr
	}
	return res, lastTS, lastID, full, nil
}

// loadUsageSignals reads the candidate signals of the reads' agents in
// (min t_r, min(max t_r + 2h, now)]; AttributeMemoryReadUsage narrows each to
// its read's own window and session.
func (r *MemoryReadUsageRepo) loadUsageSignals(ctx context.Context, reads []domain.UsageRead, now time.Time) (domain.UsageSignals, error) {
	var sig domain.UsageSignals
	from, to := reads[0].TS, reads[0].TS
	agentSet := map[uuid.UUID]bool{}
	memSet := map[uuid.UUID]bool{}
	for _, rd := range reads {
		if rd.TS.Before(from) {
			from = rd.TS
		}
		if rd.TS.After(to) {
			to = rd.TS
		}
		agentSet[rd.AgentID] = true
		for _, row := range rd.Results {
			if row.ID != uuid.Nil && domain.UsageAttributedSource(row.Source) {
				memSet[row.ID] = true
			}
		}
	}
	to = to.Add(domain.UsageWindow)
	if to.After(now) {
		to = now
	}
	agents := make([]string, 0, len(agentSet))
	for id := range agentSet {
		agents = append(agents, id.String())
	}
	mems := make([]string, 0, len(memSet))
	for id := range memSet {
		mems = append(mems, id.String())
	}
	agentArr := pq.Array(agents)

	// (a) get_memory reads.
	var gets []struct {
		ID        uuid.UUID      `db:"id"`
		TS        time.Time      `db:"ts"`
		AgentID   uuid.UUID      `db:"agent_id"`
		SessionID sql.NullString `db:"session_id"`
		TaskID    *uuid.UUID     `db:"task_id"`
		Results   []byte         `db:"results"`
	}
	if err := r.db.SelectContext(ctx, &gets, `
		SELECT id, ts, agent_id, session_id, task_id, results
		FROM memory_read_events
		WHERE agent_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3
		  AND op = 'get_memory' AND error IS NULL`, agentArr, from, to); err != nil {
		return sig, fmt.Errorf("load get_memory reads: %w", err)
	}
	for _, g := range gets {
		gm := domain.UsageGetMemory{ID: g.ID, TS: g.TS, AgentID: g.AgentID, TaskID: g.TaskID}
		if g.SessionID.Valid && g.SessionID.String != "" {
			s := g.SessionID.String
			gm.SessionID = &s
		}
		if err := json.Unmarshal(g.Results, &gm.Results); err != nil {
			return sig, fmt.Errorf("decode get_memory read %s: %w", g.ID, err)
		}
		sig.GetMemory = append(sig.GetMemory, gm)
	}

	// (b) texts the agents wrote. Each branch is shaped for an index:
	// comments by author (two partial indexes, public and internal),
	// activity_log by actor, memory_revisions by actor. documents has no
	// author index; the timestamp filter keeps the body reads to the matches.
	var texts []struct {
		RefKind  string     `db:"ref_kind"`
		Ref      uuid.UUID  `db:"ref"`
		TS       time.Time  `db:"ts"`
		AgentID  uuid.UUID  `db:"agent_id"`
		TaskID   *uuid.UUID `db:"task_id"`
		Text     string     `db:"body"`
		MemoryID *uuid.UUID `db:"memory_id"`
	}
	if err := r.db.SelectContext(ctx, &texts, `
		SELECT 'comment' AS ref_kind, c.id AS ref, c.created_at AS ts, c.author_id AS agent_id,
		       c.task_id AS task_id, c.body AS body, NULL::uuid AS memory_id
		FROM comments c
		WHERE c.author_id = ANY($1::uuid[]) AND c.created_at > $2 AND c.created_at <= $3 AND c.is_internal = false
		UNION ALL
		SELECT 'comment', c.id, c.created_at, c.author_id, c.task_id, c.body, NULL::uuid
		FROM comments c
		WHERE c.author_id = ANY($1::uuid[]) AND c.created_at > $2 AND c.created_at <= $3 AND c.is_internal = true
		UNION ALL
		SELECT 'task', t.id, a.created_at, a.actor_id, NULL::uuid, t.description, NULL::uuid
		FROM activity_log a JOIN tasks t ON t.id = a.entity_id
		WHERE a.actor_id = ANY($1::uuid[]) AND a.created_at > $2 AND a.created_at <= $3
		  AND a.entity_type = 'task' AND a.action = 'task.created'
		UNION ALL
		SELECT 'task', a.entity_id, a.created_at, a.actor_id, NULL::uuid,
		       COALESCE(a.changes->'description'->>'new', ''), NULL::uuid
		FROM activity_log a
		WHERE a.actor_id = ANY($1::uuid[]) AND a.created_at > $2 AND a.created_at <= $3
		  AND a.entity_type = 'task' AND a.action = 'task.updated' AND a.changes ? 'description'
		UNION ALL
		SELECT 'document', d.id, d.updated_at, d.updated_by, NULL::uuid, COALESCE(d.search_text, ''), NULL::uuid
		FROM documents d
		WHERE d.deleted_at IS NULL AND d.updated_by = ANY($1::uuid[]) AND d.updated_at > $2 AND d.updated_at <= $3
		UNION ALL
		SELECT 'document', d.id, d.created_at, d.created_by, NULL::uuid, COALESCE(d.search_text, ''), NULL::uuid
		FROM documents d
		WHERE d.deleted_at IS NULL AND d.created_by = ANY($1::uuid[]) AND d.created_at > $2 AND d.created_at <= $3
		UNION ALL
		SELECT 'memory_revision', mr.id, mr.created_at, mr.actor_agent_id, NULL::uuid, mr.content, mr.memory_id
		FROM memory_revisions mr
		WHERE mr.actor_agent_id = ANY($1::uuid[]) AND mr.created_at > $2 AND mr.created_at <= $3
		  AND mr.action IN ('created', 'updated')`, agentArr, from, to); err != nil {
		return sig, fmt.Errorf("load agent texts: %w", err)
	}
	for _, t := range texts {
		if t.Text == "" {
			continue
		}
		sig.Texts = append(sig.Texts, domain.UsageText{
			RefKind: t.RefKind, Ref: t.Ref, TS: t.TS, AgentID: t.AgentID, TaskID: t.TaskID, Text: t.Text, MemoryID: t.MemoryID,
		})
	}

	// (c) revisions of the served memories: update and archive of i, and the
	// memory that superseded i (its creating revision by the agent).
	if len(mems) > 0 {
		var revs []struct {
			Ref      uuid.UUID `db:"ref"`
			TS       time.Time `db:"ts"`
			AgentID  uuid.UUID `db:"agent_id"`
			MemoryID uuid.UUID `db:"memory_id"`
		}
		if err := r.db.SelectContext(ctx, &revs, `
			SELECT mr.id AS ref, mr.created_at AS ts, mr.actor_agent_id AS agent_id, mr.memory_id AS memory_id
			FROM memory_revisions mr
			WHERE mr.memory_id = ANY($2::uuid[]) AND mr.actor_agent_id = ANY($1::uuid[])
			  AND mr.created_at > $3 AND mr.created_at <= $4 AND mr.action IN ('updated', 'archived')
			UNION ALL
			SELECT mr.id, mr.created_at, mr.actor_agent_id, old.id
			FROM memories old
			JOIN memory_revisions mr ON mr.memory_id = old.superseded_by AND mr.action = 'created'
			WHERE old.id = ANY($2::uuid[]) AND mr.actor_agent_id = ANY($1::uuid[])
			  AND mr.created_at > $3 AND mr.created_at <= $4`, agentArr, pq.Array(mems), from, to); err != nil {
			return sig, fmt.Errorf("load memory revisions: %w", err)
		}
		for _, rv := range revs {
			sig.Revisions = append(sig.Revisions, domain.UsageRevision{Ref: rv.Ref, TS: rv.TS, AgentID: rv.AgentID, MemoryID: rv.MemoryID})
		}
	}
	return sig, nil
}
