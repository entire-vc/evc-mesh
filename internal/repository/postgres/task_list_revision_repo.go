package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// TaskListRevisionRepo implements repository.TaskListRevisionRepository.
// See that interface's doc comment for why there is no write method here --
// the counter is incremented entirely by the DB triggers installed in
// migration 20260821004 (ADR-0004), not by any Go code path.
type TaskListRevisionRepo struct {
	db *sqlx.DB
}

// NewTaskListRevisionRepo creates a new TaskListRevisionRepo.
func NewTaskListRevisionRepo(db *sqlx.DB) *TaskListRevisionRepo {
	return &TaskListRevisionRepo{db: db}
}

// GetRevision returns the current task_list_revision for projectID, or 0 if
// no row exists yet -- see the interface doc comment for why that is a safe,
// self-healing default rather than an error.
func (r *TaskListRevisionRepo) GetRevision(ctx context.Context, projectID uuid.UUID) (int64, error) {
	const q = `SELECT revision FROM task_list_revisions WHERE project_id = $1`
	var revision int64
	err := r.db.GetContext(ctx, &revision, q, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return revision, nil
}

var _ repository.TaskListRevisionRepository = (*TaskListRevisionRepo)(nil)
