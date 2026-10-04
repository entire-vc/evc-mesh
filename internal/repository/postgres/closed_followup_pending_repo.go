package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ClosedFollowUpPendingRepo implements repository.ClosedFollowUpPendingRepository
// with PostgreSQL (#db1c6c7a).
type ClosedFollowUpPendingRepo struct {
	db *sqlx.DB
}

// NewClosedFollowUpPendingRepo creates a new ClosedFollowUpPendingRepo.
func NewClosedFollowUpPendingRepo(db *sqlx.DB) *ClosedFollowUpPendingRepo {
	return &ClosedFollowUpPendingRepo{db: db}
}

const closedFollowUpPendingSelectCols = `comment_id, source_task_id, finding_key, attempts, last_error, created_at, escalated_at, noticed_at`

// Enqueue upserts the pending row on the comment PK. A row that already
// exists keeps its attempts and only refreshes last_error: the counter is the
// reconcile budget, and a re-enqueued finding must not start the 10 attempts
// over — it failed inline again, which costs it nothing here, not a reset.
func (r *ClosedFollowUpPendingRepo) Enqueue(ctx context.Context, p *domain.ClosedFollowUpPending) error {
	const q = `
		INSERT INTO closed_followup_pending (comment_id, source_task_id, finding_key, attempts, last_error, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (comment_id) DO UPDATE SET last_error = EXCLUDED.last_error
	`
	_, err := r.db.ExecContext(ctx, q, p.CommentID, p.SourceTaskID, p.FindingKey, p.Attempts, p.LastError, p.CreatedAt)
	return err
}

// ListDue returns the rows the reconcile job should retry: attempts below the
// max, oldest first — the oldest finding has been waiting longest, so it goes
// first both in delivery and in escalation order. A row AT or past the max is
// listed too while EITHER notice stamp is still NULL: its notices — not the
// delivery — are what those rows are retried for, and the row retires only
// when both have landed (round 3 made the escalation durable, round 4 the
// park notice, round 5 closed their intersection: retiring on escalated_at
// alone stranded a park notice that never landed).
func (r *ClosedFollowUpPendingRepo) ListDue(ctx context.Context, maxAttempts, limit int) ([]domain.ClosedFollowUpPending, error) {
	const q = `
		SELECT ` + closedFollowUpPendingSelectCols + `
		FROM closed_followup_pending
		WHERE attempts < $1 OR escalated_at IS NULL OR noticed_at IS NULL
		ORDER BY created_at, comment_id
		LIMIT $2
	`
	var rows []domain.ClosedFollowUpPending
	if err := r.db.SelectContext(ctx, &rows, q, maxAttempts, limit); err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkEscalated stamps the row's escalation as DELIVERED: the «нужен человек»
// notice exists on the source card. Written only after the notice comment,
// never before — the flag, not the counter, is what retires the row from the
// due set. Idempotent: a second stamp never moves the first landing time.
func (r *ClosedFollowUpPendingRepo) MarkEscalated(ctx context.Context, commentID uuid.UUID, at time.Time) error {
	const q = `
		UPDATE closed_followup_pending
		SET escalated_at = $2
		WHERE comment_id = $1 AND escalated_at IS NULL
	`
	_, err := r.db.ExecContext(ctx, q, commentID, at)
	return err
}

// MarkNoticed stamps the park's visibility: the «не подтверждена» notice has
// landed on the source card. Written only after the notice comment, never
// before — while it is NULL every reconcile pass that works the row retries
// the notice (codex-review P1, round 4). Idempotent, first write wins, same
// shape as MarkEscalated.
func (r *ClosedFollowUpPendingRepo) MarkNoticed(ctx context.Context, commentID uuid.UUID, at time.Time) error {
	const q = `
		UPDATE closed_followup_pending
		SET noticed_at = $2
		WHERE comment_id = $1 AND noticed_at IS NULL
	`
	_, err := r.db.ExecContext(ctx, q, commentID, at)
	return err
}

// MarkAttempt increments attempts atomically and returns the new value — the
// increment lives in the UPDATE itself, not in a Go-side read-modify-write,
// so two concurrently running reconcile passes can only ever count more
// attempts, never lose one or clobber the counter.
func (r *ClosedFollowUpPendingRepo) MarkAttempt(ctx context.Context, commentID uuid.UUID, lastErr string) (int, error) {
	const q = `
		UPDATE closed_followup_pending
		SET attempts = attempts + 1, last_error = $2
		WHERE comment_id = $1
		RETURNING attempts
	`
	var attempts int
	err := r.db.GetContext(ctx, &attempts, q, commentID, lastErr)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // delivered and deleted by a concurrent pass: nothing to count
	}
	if err != nil {
		return 0, err
	}
	return attempts, nil
}

// Delete removes the pending row. Idempotent by nature: deleting a row a
// concurrent reconcile already deleted is a no-op, not an error.
func (r *ClosedFollowUpPendingRepo) Delete(ctx context.Context, commentID uuid.UUID) error {
	const q = `DELETE FROM closed_followup_pending WHERE comment_id = $1`
	_, err := r.db.ExecContext(ctx, q, commentID)
	return err
}
