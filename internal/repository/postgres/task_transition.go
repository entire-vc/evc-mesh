package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func (r *TaskRepo) taskConflict(ctx context.Context, id uuid.UUID) error {
	current, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if current == nil {
		return apierror.NotFound("Task")
	}
	return &domain.TaskConflict{CurrentVersion: current.Version, CurrentStatusID: current.StatusID, CurrentUpdatedAt: current.UpdatedAt}
}

// UpdateTransition performs the decision and its assignment/alarm/lease effects
// at one SQL linearization point. It never copies title, gates or other fields
// from the caller's snapshot. All legacy writes also invalidate task.version.
func (r *TaskRepo) UpdateTransition(ctx context.Context, task *domain.Task, input domain.TaskTransition) error {
	q := `UPDATE tasks t SET status_id=$2, position=$3, completed_at=$4,
 status_changed_at=$5, assignee_id=$6, assignee_type=$7,
 pre_review_assignee_id=$8, pre_review_assignee_type=$9, updated_at=$10,
 due_date=CASE WHEN $14::timestamptz IS NULL THEN due_date ELSE $14 END,
 labels=CASE WHEN $14::timestamptz IS NULL THEN labels ELSE $15 END`
	args := []any{task.ID, task.StatusID, task.Position, task.CompletedAt, task.StatusChangedAt,
		task.AssigneeID, task.AssigneeType, task.PreReviewAssigneeID, task.PreReviewAssigneeType,
		task.UpdatedAt, input.ExpectedVersion, input.ExpectedStatusID, input.ExpectedUpdatedAt, input.AlarmDue, pq.Array(input.AlarmLabels)}
	if input.Reaper != nil && input.Reaper.Mode == "expired" {
		q += `, checked_out_by=NULL,checkout_token=NULL,checkout_expires=NULL,checkout_acquired_at=NULL,checkout_session_id=NULL,checkout_request_id=NULL`
	}
	q += ` WHERE t.id=$1 AND t.deleted_at IS NULL AND t.version=$11
 AND ($12::uuid IS NULL OR t.status_id=$12)
 AND ($13::timestamptz IS NULL OR t.updated_at=$13)`
	if g := input.Reaper; g != nil {
		q += ` AND NOT t.human_gate AND NOT t.is_shipped
 AND t.status_id IN (SELECT id FROM task_statuses WHERE category='in_progress')`
		switch g.Mode {
		case "expired":
			q += ` AND t.checkout_generation=$16 AND t.checkout_token IS NOT DISTINCT FROM $17::uuid
 AND t.checkout_expires=$18 AND t.checkout_expires<clock_timestamp()`
			args = append(args, g.Generation, g.Token, g.ExpiresAt)
		case "unleased":
			if g.QuietGrace <= 0 {
				return fmt.Errorf("reaper quiet grace must be positive")
			}
			q += ` AND t.checked_out_by IS NULL AND t.checkout_token IS NULL AND t.checkout_expires IS NULL
 AND GREATEST(t.updated_at,
 COALESCE((SELECT max(created_at) FROM comments WHERE task_id=t.id),t.updated_at),
 COALESCE((SELECT max(created_at) FROM artifacts WHERE task_id=t.id),t.updated_at),
 COALESCE((SELECT max(created_at) FROM vcs_links WHERE task_id=t.id),t.updated_at)) < clock_timestamp()-$16::interval`
			args = append(args, fmt.Sprintf("%f seconds", g.QuietGrace.Seconds()))
		default:
			return fmt.Errorf("unknown reaper mode %q", g.Mode)
		}
	}
	q += ` RETURNING version,checked_out_by,checkout_token,checkout_expires,checkout_acquired_at,checkout_session_id,checkout_request_id,checkout_generation,false AS changed`
	var result struct {
		domain.CheckoutLease
		Version int64 `db:"version"`
	}
	if err := r.db.GetContext(ctx, &result, q, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r.taskConflict(ctx, task.ID)
		}
		return err
	}
	task.Version = result.Version
	if input.AlarmDue != nil {
		task.DueDate, task.Labels = input.AlarmDue, input.AlarmLabels
	}
	lease := result.CheckoutLease
	task.CheckedOutBy, task.CheckoutToken, task.CheckoutExpires, task.CheckoutAcquiredAt = lease.Holder, lease.Token, lease.ExpiresAt, lease.AcquiredAt
	task.CheckoutSessionID, task.CheckoutRequestID, task.CheckoutGeneration = lease.SessionID, lease.RequestID, lease.Generation
	return nil
}
