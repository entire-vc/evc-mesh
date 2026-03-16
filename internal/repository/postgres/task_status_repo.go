package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskStatusRepo implements repository.TaskStatusRepository with PostgreSQL.
type TaskStatusRepo struct {
	db *sqlx.DB
}

// NewTaskStatusRepo creates a new TaskStatusRepo.
func NewTaskStatusRepo(db *sqlx.DB) *TaskStatusRepo {
	return &TaskStatusRepo{db: db}
}

func (r *TaskStatusRepo) Create(ctx context.Context, status *domain.TaskStatus) error {
	const q = `
		INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	autoTrans := status.AutoTransition
	if autoTrans == nil {
		autoTrans = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		status.ID, status.ProjectID, status.Name, status.Slug,
		status.Color, status.Position, status.Category,
		status.IsDefault, autoTrans,
	)
	return err
}

func (r *TaskStatusRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
	const q = `SELECT * FROM task_statuses WHERE id = $1`
	var status domain.TaskStatus
	if err := r.db.GetContext(ctx, &status, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &status, nil
}

func (r *TaskStatusRepo) Update(ctx context.Context, status *domain.TaskStatus) error {
	const q = `
		UPDATE task_statuses
		SET name = $2, slug = $3, color = $4, position = $5, category = $6, is_default = $7, auto_transition = $8
		WHERE id = $1
	`
	autoTrans := status.AutoTransition
	if autoTrans == nil {
		autoTrans = json.RawMessage(`{}`)
	}
	res, err := r.db.ExecContext(ctx, q,
		status.ID, status.Name, status.Slug, status.Color,
		status.Position, status.Category, status.IsDefault, autoTrans,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("TaskStatus")
	}
	return nil
}

func (r *TaskStatusRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM task_statuses WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("TaskStatus")
	}
	return nil
}

func (r *TaskStatusRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error) {
	const q = `SELECT * FROM task_statuses WHERE project_id = $1 ORDER BY position ASC`
	var statuses []domain.TaskStatus
	if err := r.db.SelectContext(ctx, &statuses, q, projectID); err != nil {
		return nil, err
	}
	if statuses == nil {
		statuses = []domain.TaskStatus{}
	}
	return statuses, nil
}

func (r *TaskStatusRepo) GetDefaultForProject(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error) {
	const q = `SELECT * FROM task_statuses WHERE project_id = $1 AND is_default = TRUE LIMIT 1`
	var status domain.TaskStatus
	if err := r.db.GetContext(ctx, &status, q, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &status, nil
}

// Reorder updates the position of each status in the given order.
// statusIDs[0] gets position 0, statusIDs[1] gets position 1, etc.
func (r *TaskStatusRepo) Reorder(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is a no-op after commit; error is intentionally ignored

	// Lock rows to prevent concurrent reorder.
	const lockQ = `SELECT id FROM task_statuses WHERE project_id = $1 FOR UPDATE`
	var ids []uuid.UUID
	if err := tx.SelectContext(ctx, &ids, lockQ, projectID); err != nil {
		return err
	}

	const q = `UPDATE task_statuses SET position = $1 WHERE id = $2 AND project_id = $3`
	for i, id := range statusIDs {
		res, err := tx.ExecContext(ctx, q, i, id, projectID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return apierror.NotFound(fmt.Sprintf("TaskStatus %s", id))
		}
	}

	return tx.Commit()
}
