package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MemoryReadEventRepo writes memory_read_events and agent_tool_calls_daily.
type MemoryReadEventRepo struct {
	db *sqlx.DB
}

// NewMemoryReadEventRepo creates a MemoryReadEventRepo.
func NewMemoryReadEventRepo(db *sqlx.DB) *MemoryReadEventRepo {
	return &MemoryReadEventRepo{db: db}
}

const insertMemoryReadEventSQL = `
	INSERT INTO memory_read_events (
		ts, workspace_id, project_id, agent_id, user_id, session_id, task_id,
		caller_kind, route, user_agent, op, status_code, query_hash, query_text,
		search_mode, result_count, pinned_count, superseded_count, top_score,
		results, response_chars, duration_ms, error
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7,
		$8, $9, $10, $11, $12, $13, $14,
		$15, $16, $17, $18, $19,
		$20::jsonb, $21, $22, $23
	)`

// InsertMemoryReadEvents writes one batch in one transaction.
func (r *MemoryReadEventRepo) InsertMemoryReadEvents(ctx context.Context, events []domain.MemoryReadEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PreparexContext(ctx, insertMemoryReadEventSQL)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for i := range events {
		ev := &events[i]
		results := ev.Results
		if results == nil {
			results = []domain.MemoryReadResult{}
		}
		raw, mErr := json.Marshal(results)
		if mErr != nil {
			return fmt.Errorf("marshal results: %w", mErr)
		}
		callerKind := ev.CallerKind
		if !domain.ValidCallerKind(callerKind) {
			callerKind = domain.CallerKindUnknown
		}
		ts := ev.TS
		if ts.IsZero() {
			ts = time.Now()
		}
		if _, err = stmt.ExecContext(ctx,
			ts, ev.WorkspaceID, ev.ProjectID, ev.AgentID, ev.UserID, ev.SessionID, ev.TaskID,
			callerKind, ev.Route, ev.UserAgent, ev.Op, ev.StatusCode, ev.QueryHash, ev.QueryText,
			ev.SearchMode, ev.ResultCount, ev.PinnedCount, ev.SupersededCount, ev.TopScore,
			string(raw), ev.ResponseChars, ev.DurationMS, ev.Error,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PurgeMemoryReadEvents deletes rows older than rawBefore and clears
// query_text on rows older than queryTextBefore.
func (r *MemoryReadEventRepo) PurgeMemoryReadEvents(ctx context.Context, rawBefore, queryTextBefore time.Time) (deleted, cleared int64, err error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM memory_read_events WHERE ts < $1`, rawBefore)
	if err != nil {
		return 0, 0, err
	}
	deleted, _ = res.RowsAffected()
	res, err = r.db.ExecContext(ctx,
		`UPDATE memory_read_events SET query_text = NULL WHERE ts < $1 AND query_text IS NOT NULL`, queryTextBefore)
	if err != nil {
		return deleted, 0, err
	}
	cleared, _ = res.RowsAffected()
	return deleted, cleared, nil
}

// incrementToolCallsDailySQL adds calls to one (day, workspace, project, agent,
// caller_kind, tool) bucket. A delta without a project takes the project of its
// task when it has one.
const incrementToolCallsDailySQL = `
	INSERT INTO agent_tool_calls_daily (day, workspace_id, project_id, agent_id, caller_kind, tool, calls)
	VALUES ($1, $2, COALESCE($3::uuid, (SELECT t.project_id FROM tasks t WHERE t.id = $4::uuid)), $5, $6, $7, $8)
	ON CONFLICT (day, workspace_id, (COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid)), agent_id, caller_kind, tool)
	DO UPDATE SET calls = agent_tool_calls_daily.calls + EXCLUDED.calls, updated_at = now()`

// IncrementToolCallsDaily applies a batch of deltas in one transaction.
func (r *MemoryReadEventRepo) IncrementToolCallsDaily(ctx context.Context, deltas []domain.ToolCallsDailyDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PreparexContext(ctx, incrementToolCallsDailySQL)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, d := range deltas {
		if d.Calls == 0 {
			continue
		}
		callerKind := d.CallerKind
		if !domain.ValidCallerKind(callerKind) {
			callerKind = domain.CallerKindUnknown
		}
		if _, err = stmt.ExecContext(ctx,
			d.Day.UTC().Format("2006-01-02"), d.WorkspaceID, d.ProjectID, d.TaskID, d.AgentID, callerKind, d.Tool, d.Calls,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
