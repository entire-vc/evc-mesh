package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const qStartAfterReceiptCols = `id, task_id, op, start_after, origin, actor_id, actor_type, source_offset,
	event_ref, evidence_source, evidence_why, evidence_status, cleared_receipt_id,
	idempotency_key, task_version, reason, created_at`

// insertStartAfterReceipt records the set of a new date in the writer's
// transaction; version is the task version the write produced.
func insertStartAfterReceipt(ctx context.Context, tx rowQueryer, r *domain.StartAfterReceipt, version int64) error {
	r.TaskVersion = &version
	return tx.QueryRowContext(ctx, `
		INSERT INTO start_after_receipts
			(id, task_id, op, start_after, origin, actor_id, actor_type, source_offset,
			 event_ref, evidence_source, evidence_why, evidence_status, task_version,
			 cleared_receipt_id, idempotency_key, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING created_at`,
		r.ID, r.TaskID, r.Op, r.StartAfter, r.Origin, r.ActorID, r.ActorType, r.SourceOffset,
		r.EventRef, r.Source, r.Why, r.EvidenceStatus, version,
		r.ClearedReceiptID, r.IdempotencyKey, r.Reason).Scan(&r.CreatedAt)
}

// rowQueryer is what both *sql.Tx (TaskRepo.Create) and *sqlx.Tx have.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CheckStartAfterEvidenceSource confirms a comment:/artifact: source of an
// agent's start_after exists in the workspace and is not the agent's own.
func (r *TaskRepo) CheckStartAfterEvidenceSource(ctx context.Context, workspaceID uuid.UUID, source string, writer uuid.UUID) error {
	return checkEvidenceSource(ctx, r.db, workspaceID, source, writer)
}

// CompareClearStartAfter removes start_after only if it is still exactly the
// schedule the caller read: the same instant, task version and, when given,
// origin and receipt. Everything happens under the task row lock in one
// transaction: the check, the clear, the receipt and the activity entry. A
// mismatch changes nothing and returns the current values.
func (r *TaskRepo) CompareClearStartAfter(ctx context.Context, taskID uuid.UUID, in domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
	actor, kind := actorctx.FromContext(ctx)
	if actor == uuid.Nil || kind == "" {
		return nil, apierror.Forbidden("authenticated task writer required")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	task, err := lockParkedTask(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}

	// A repeat of a request that already went through returns its receipt,
	// whatever the task looks like now. The row lock above serializes it with
	// the first one, so two copies of one request never both clear.
	var prior domain.StartAfterReceipt
	err = tx.GetContext(ctx, &prior, `SELECT `+qStartAfterReceiptCols+` FROM start_after_receipts
		WHERE task_id = $1 AND op = 'clear' AND idempotency_key = $2`, taskID, in.IdempotencyKey)
	switch {
	case err == nil:
		return &domain.StartAfterClearResult{Receipt: prior, Replayed: true, Version: task.Version}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}

	if conflict := in.Mismatch(task); conflict != nil {
		return nil, conflict
	}
	if kind != domain.ActorTypeUser && *task.StartAfterOrigin == domain.StartAfterHumanExplicit {
		// A person's date is preserved: an agent cannot clear it, even when it
		// read it correctly. The person (or a lead acting as one) clears it.
		return nil, apierror.Forbidden("start_after was set by a person; an agent cannot clear it")
	}

	var newVersion int64
	err = tx.GetContext(ctx, &newVersion, `UPDATE tasks
		SET start_after = NULL, start_after_origin = NULL, start_after_receipt_id = NULL, updated_at = now()
		WHERE id = $1 AND version = $2 RETURNING version`, taskID, task.Version)
	if err != nil {
		return nil, err
	}

	receipt := domain.StartAfterReceipt{
		ID: uuid.New(), TaskID: taskID, Op: "clear", StartAfter: *task.StartAfter,
		Origin: *task.StartAfterOrigin, ActorID: actor, ActorType: kind,
		ClearedReceiptID: task.StartAfterReceiptID, IdempotencyKey: &in.IdempotencyKey,
		TaskVersion: &newVersion, Reason: in.Reason,
	}
	err = tx.GetContext(ctx, &receipt.CreatedAt, `
		INSERT INTO start_after_receipts
			(id, task_id, op, start_after, origin, actor_id, actor_type, cleared_receipt_id,
			 idempotency_key, task_version, reason)
		VALUES ($1, $2, 'clear', $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING created_at`,
		receipt.ID, taskID, receipt.StartAfter, receipt.Origin, actor, kind, receipt.ClearedReceiptID,
		in.IdempotencyKey, newVersion, in.Reason)
	if err != nil {
		return nil, err
	}

	changes, err := json.Marshal(actorctx.AnnotateChanges(ctx, map[string]any{
		"start_after":        map[string]any{"old": task.StartAfter, "new": nil},
		"start_after_origin": map[string]any{"old": task.StartAfterOrigin, "new": nil},
		"source":             "start-after-compare-clear",
		"receipt_id":         receipt.ID,
		"cleared_receipt_id": task.StartAfterReceiptID,
		"old_version":        task.Version,
		"new_version":        newVersion,
	}))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activity_log(id,workspace_id,entity_type,entity_id,action,actor_id,actor_type,changes,created_at)
		SELECT $1,workspace_id,'task',$2,'task.updated',$3,$4,$5,$6 FROM projects WHERE id=$7`,
		uuid.New(), taskID, actor, kind, changes, time.Now(), task.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.StartAfterClearResult{Receipt: receipt, Version: newVersion}, nil
}

// ListStartAfterReceipts returns a task's receipts, oldest first.
func (r *TaskRepo) ListStartAfterReceipts(ctx context.Context, taskID uuid.UUID) ([]domain.StartAfterReceipt, error) {
	var out []domain.StartAfterReceipt
	err := r.db.SelectContext(ctx, &out, `SELECT `+qStartAfterReceiptCols+` FROM start_after_receipts
		WHERE task_id = $1 ORDER BY created_at, id`, taskID)
	return out, err
}
