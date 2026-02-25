package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ActivityLogRepo implements repository.ActivityLogRepository with PostgreSQL.
type ActivityLogRepo struct {
	db *sqlx.DB
}

// NewActivityLogRepo creates a new ActivityLogRepo.
func NewActivityLogRepo(db *sqlx.DB) *ActivityLogRepo {
	return &ActivityLogRepo{db: db}
}

func (r *ActivityLogRepo) Create(ctx context.Context, entry *domain.ActivityLog) error {
	const q = `
		INSERT INTO activity_log (id, workspace_id, entity_type, entity_id, action, actor_id, actor_type, changes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	changes := entry.Changes
	if changes == nil {
		changes = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		entry.ID, entry.WorkspaceID, entry.EntityType, entry.EntityID,
		entry.Action, entry.ActorID, entry.ActorType, changes, entry.CreatedAt,
	)
	return err
}

func (r *ActivityLogRepo) List(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	pg.Normalize()

	args := []interface{}{workspaceID} // $1
	conditions := []string{"workspace_id = $1"}
	argIdx := 2

	if filter.EntityType != nil {
		conditions = append(conditions, fmt.Sprintf("entity_type = $%d", argIdx))
		args = append(args, *filter.EntityType)
		argIdx++
	}
	if filter.EntityID != nil {
		conditions = append(conditions, fmt.Sprintf("entity_id = $%d", argIdx))
		args = append(args, *filter.EntityID)
		argIdx++
	}
	if filter.ActorID != nil {
		conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argIdx))
		args = append(args, *filter.ActorID)
		argIdx++
	}
	if filter.ActorType != nil {
		conditions = append(conditions, fmt.Sprintf("actor_type = $%d", argIdx))
		args = append(args, *filter.ActorType)
		argIdx++
	}
	if filter.Action != nil {
		conditions = append(conditions, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, *filter.Action)
		argIdx++
	}
	if filter.From != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.From)
		argIdx++
	}
	if filter.To != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.To)
		argIdx++
	}
	_ = argIdx // suppress ineffassign after last conditional block

	where := "WHERE " + joinAnd(conditions)

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM activity_log %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data
	dataQ := fmt.Sprintf(`SELECT * FROM activity_log %s ORDER BY created_at DESC %s`, where, paginationClause(pg))
	var entries []domain.ActivityLog
	if err := r.db.SelectContext(ctx, &entries, dataQ, args...); err != nil {
		return nil, err
	}

	return pagination.NewPage(entries, totalCount, pg), nil
}

// Export returns all matching activity log entries up to the given limit without pagination.
// It re-uses the same dynamic WHERE-clause builder as List.
func (r *ActivityLogRepo) Export(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, limit int) ([]domain.ActivityLog, error) {
	args := []interface{}{workspaceID} // $1
	conditions := []string{"workspace_id = $1"}
	argIdx := 2

	if filter.EntityType != nil {
		conditions = append(conditions, fmt.Sprintf("entity_type = $%d", argIdx))
		args = append(args, *filter.EntityType)
		argIdx++
	}
	if filter.EntityID != nil {
		conditions = append(conditions, fmt.Sprintf("entity_id = $%d", argIdx))
		args = append(args, *filter.EntityID)
		argIdx++
	}
	if filter.ActorID != nil {
		conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argIdx))
		args = append(args, *filter.ActorID)
		argIdx++
	}
	if filter.ActorType != nil {
		conditions = append(conditions, fmt.Sprintf("actor_type = $%d", argIdx))
		args = append(args, *filter.ActorType)
		argIdx++
	}
	if filter.Action != nil {
		conditions = append(conditions, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, *filter.Action)
		argIdx++
	}
	if filter.From != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *filter.From)
		argIdx++
	}
	if filter.To != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *filter.To)
		argIdx++
	}
	_ = argIdx // suppress unused variable warning after last conditional block

	where := "WHERE " + joinAnd(conditions)
	dataQ := fmt.Sprintf(
		`SELECT * FROM activity_log %s ORDER BY created_at DESC LIMIT $%d`,
		where, len(args)+1,
	)
	args = append(args, limit)

	var entries []domain.ActivityLog
	if err := r.db.SelectContext(ctx, &entries, dataQ, args...); err != nil {
		return nil, err
	}
	return entries, nil
}

// ListByTask returns a paginated list of activity log entries for a specific task.
func (r *ActivityLogRepo) ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	pg.Normalize()

	const countQ = `SELECT COUNT(*) FROM activity_log WHERE entity_type = 'task' AND entity_id = $1`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, taskID); err != nil {
		return nil, err
	}

	dataQ := fmt.Sprintf(
		`SELECT * FROM activity_log WHERE entity_type = 'task' AND entity_id = $1 ORDER BY created_at DESC %s`,
		paginationClause(pg),
	)
	var entries []domain.ActivityLog
	if err := r.db.SelectContext(ctx, &entries, dataQ, taskID); err != nil {
		return nil, err
	}

	return pagination.NewPage(entries, totalCount, pg), nil
}
