package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskDependencyRepo implements repository.TaskDependencyRepository with PostgreSQL.
type TaskDependencyRepo struct {
	db *sqlx.DB
}

// NewTaskDependencyRepo creates a new TaskDependencyRepo.
func NewTaskDependencyRepo(db *sqlx.DB) *TaskDependencyRepo {
	return &TaskDependencyRepo{db: db}
}

func (r *TaskDependencyRepo) Create(ctx context.Context, dep *domain.TaskDependency) error {
	const q = `
		INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := r.db.ExecContext(ctx, q,
		dep.ID, dep.TaskID, dep.DependsOnTaskID, dep.DependencyType, dep.CreatedAt,
	)
	return err
}

func (r *TaskDependencyRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM task_dependencies WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("TaskDependency")
	}
	return nil
}

// ListByTask returns all dependencies where the given task depends on another.
func (r *TaskDependencyRepo) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	const q = `SELECT * FROM task_dependencies WHERE task_id = $1 ORDER BY created_at ASC`
	var deps []domain.TaskDependency
	if err := r.db.SelectContext(ctx, &deps, q, taskID); err != nil {
		return nil, err
	}
	if deps == nil {
		deps = []domain.TaskDependency{}
	}
	return deps, nil
}

// ListDependents returns all dependencies where another task depends on the given task.
func (r *TaskDependencyRepo) ListDependents(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	const q = `SELECT * FROM task_dependencies WHERE depends_on_task_id = $1 ORDER BY created_at ASC`
	var deps []domain.TaskDependency
	if err := r.db.SelectContext(ctx, &deps, q, taskID); err != nil {
		return nil, err
	}
	if deps == nil {
		deps = []domain.TaskDependency{}
	}
	return deps, nil
}

func (r *TaskDependencyRepo) Exists(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id = $1 AND depends_on_task_id = $2)`
	var exists bool
	if err := r.db.GetContext(ctx, &exists, q, taskID, dependsOnTaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return exists, nil
}
