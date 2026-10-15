package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TaskExternalRefRepo implements repository.TaskExternalRefRepository.
type TaskExternalRefRepo struct {
	db *sqlx.DB
}

// NewTaskExternalRefRepo creates a TaskExternalRefRepo.
func NewTaskExternalRefRepo(db *sqlx.DB) *TaskExternalRefRepo {
	return &TaskExternalRefRepo{db: db}
}

const taskExternalRefCols = `task_id, ref, hash, set_by, set_at`

func (r *TaskExternalRefRepo) Get(ctx context.Context, taskID uuid.UUID) (*domain.TaskExternalRef, error) {
	var e domain.TaskExternalRef
	err := r.db.GetContext(ctx, &e, `SELECT `+taskExternalRefCols+` FROM task_external_refs WHERE task_id = $1`, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Put inserts the binding, or updates ref when the pinned hash is unchanged.
// A different hash leaves the row untouched (RETURNING yields no row).
func (r *TaskExternalRefRepo) Put(ctx context.Context, in *domain.TaskExternalRef) (*domain.TaskExternalRef, bool, error) {
	const q = `
		INSERT INTO task_external_refs (task_id, ref, hash, set_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (task_id) DO UPDATE SET ref = EXCLUDED.ref, set_by = EXCLUDED.set_by, set_at = now()
		WHERE task_external_refs.hash = EXCLUDED.hash
		RETURNING ` + taskExternalRefCols
	var e domain.TaskExternalRef
	err := r.db.GetContext(ctx, &e, q, in.TaskID, in.Ref, in.Hash, in.SetBy)
	if errors.Is(err, sql.ErrNoRows) {
		cur, gerr := r.Get(ctx, in.TaskID)
		return cur, false, gerr
	}
	if err != nil {
		return nil, false, err
	}
	return &e, true, nil
}
