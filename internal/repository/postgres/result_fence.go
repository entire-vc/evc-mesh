package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// checkResultFenceTx locks the fenced task row (FOR UPDATE, the lock every
// checkout writer takes) and checks the caller's precondition against it, in
// the transaction of the write it guards: a takeover, release or reassignment
// either commits before this read and refuses the write, or waits for the
// write to commit. A nil fence is a no-op.
func checkResultFenceTx(ctx context.Context, tx *sqlx.Tx, f *domain.ResultFence) error {
	return checkResultFence(ctx, tx, f, " FOR UPDATE")
}

const resultFenceSelect = `SELECT checked_out_by, checkout_token, checkout_expires,
 checkout_session_id, checkout_generation, input_revision, clock_timestamp() AS now
 FROM tasks WHERE id=$1 AND deleted_at IS NULL`

func checkResultFence(ctx context.Context, q sqlx.QueryerContext, f *domain.ResultFence, lock string) error {
	if f == nil {
		return nil
	}
	var s domain.ResultFenceState
	err := sqlx.GetContext(ctx, q, &s, resultFenceSelect+lock, f.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound("Task")
	}
	if err != nil {
		return err
	}
	return f.Check(s)
}

// CheckResultFence is the early, unlocked check a write runs before work that
// is costly to undo (an artifact upload). It is advisory: the write itself
// checks again under the row lock.
func (r *TaskRepo) CheckResultFence(ctx context.Context, f *domain.ResultFence) error {
	return checkResultFence(ctx, r.db, f, "")
}

// CheckResultFence on the artifact repository: the early check of an upload,
// before any byte reaches storage.
func (r *ArtifactRepo) CheckResultFence(ctx context.Context, f *domain.ResultFence) error {
	return checkResultFence(ctx, r.db, f, "")
}
