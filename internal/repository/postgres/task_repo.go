package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// safeSlugRe validates that a custom field slug is a safe SQL identifier.
var safeSlugRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// taskRow is the DB row representation (includes task_number and deleted_at
// that the domain model does not have).
type taskRow struct {
	ID             uuid.UUID       `db:"id"`
	ProjectID      uuid.UUID       `db:"project_id"`
	StatusID       uuid.UUID       `db:"status_id"`
	Title          string          `db:"title"`
	Description    string          `db:"description"`
	AssigneeID     *uuid.UUID      `db:"assignee_id"`
	AssigneeType   domain.AssigneeType `db:"assignee_type"`
	Priority       domain.Priority `db:"priority"`
	ParentTaskID   *uuid.UUID      `db:"parent_task_id"`
	Position       float64         `db:"position"`
	DueDate        *time.Time      `db:"due_date"`
	EstimatedHours *float64        `db:"estimated_hours"`
	CustomFields   json.RawMessage `db:"custom_fields"`
	Labels         pq.StringArray  `db:"labels"`
	TaskNumber     int             `db:"task_number"`
	CreatedBy      uuid.UUID       `db:"created_by"`
	CreatedByType  domain.ActorType `db:"created_by_type"`
	CreatedAt      time.Time       `db:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at"`
	CompletedAt    *time.Time      `db:"completed_at"`
	DeletedAt      *time.Time      `db:"deleted_at"`
}

func (r *taskRow) toDomain() domain.Task {
	return domain.Task{
		ID:             r.ID,
		ProjectID:      r.ProjectID,
		StatusID:       r.StatusID,
		Title:          r.Title,
		Description:    r.Description,
		AssigneeID:     r.AssigneeID,
		AssigneeType:   r.AssigneeType,
		Priority:       r.Priority,
		ParentTaskID:   r.ParentTaskID,
		Position:       r.Position,
		DueDate:        r.DueDate,
		EstimatedHours: r.EstimatedHours,
		CustomFields:   r.CustomFields,
		Labels:         r.Labels,
		CreatedBy:      r.CreatedBy,
		CreatedByType:  r.CreatedByType,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		CompletedAt:    r.CompletedAt,
	}
}

func taskRowsToSlice(rows []taskRow) []domain.Task {
	result := make([]domain.Task, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result
}

// TaskRepo implements repository.TaskRepository with PostgreSQL.
type TaskRepo struct {
	db *sqlx.DB
}

// NewTaskRepo creates a new TaskRepo.
func NewTaskRepo(db *sqlx.DB) *TaskRepo {
	return &TaskRepo{db: db}
}

func (r *TaskRepo) Create(ctx context.Context, task *domain.Task) error {
	const q = `
		INSERT INTO tasks (
			id, project_id, status_id, title, description,
			assignee_id, assignee_type, priority, parent_task_id, position,
			due_date, estimated_hours, custom_fields, labels,
			task_number, created_by, created_by_type, created_at, updated_at, completed_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14,
			(SELECT COALESCE(MAX(task_number), 0) + 1 FROM tasks WHERE project_id = $2),
			$15, $16, $17, $18, $19
		)
	`
	customFields := task.CustomFields
	if customFields == nil {
		customFields = json.RawMessage(`{}`)
	}
	labels := task.Labels
	if labels == nil {
		labels = pq.StringArray{}
	}
	_, err := r.db.ExecContext(ctx, q,
		task.ID, task.ProjectID, task.StatusID, task.Title, task.Description,
		task.AssigneeID, task.AssigneeType, task.Priority, task.ParentTaskID, task.Position,
		task.DueDate, task.EstimatedHours, customFields, labels,
		task.CreatedBy, task.CreatedByType, task.CreatedAt, task.UpdatedAt, task.CompletedAt,
	)
	return err
}

func (r *TaskRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	const q = `SELECT * FROM tasks WHERE id = $1 AND deleted_at IS NULL`
	var row taskRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t := row.toDomain()
	return &t, nil
}

func (r *TaskRepo) Update(ctx context.Context, task *domain.Task) error {
	const q = `
		UPDATE tasks
		SET status_id = $2, title = $3, description = $4,
		    assignee_id = $5, assignee_type = $6, priority = $7,
		    parent_task_id = $8, position = $9, due_date = $10,
		    estimated_hours = $11, custom_fields = $12, labels = $13,
		    updated_at = $14, completed_at = $15
		WHERE id = $1 AND deleted_at IS NULL
	`
	customFields := task.CustomFields
	if customFields == nil {
		customFields = json.RawMessage(`{}`)
	}
	labels := task.Labels
	if labels == nil {
		labels = pq.StringArray{}
	}
	res, err := r.db.ExecContext(ctx, q,
		task.ID, task.StatusID, task.Title, task.Description,
		task.AssigneeID, task.AssigneeType, task.Priority,
		task.ParentTaskID, task.Position, task.DueDate,
		task.EstimatedHours, customFields, labels,
		task.UpdatedAt, task.CompletedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// Delete performs a soft delete by setting deleted_at.
func (r *TaskRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE tasks SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

func (r *TaskRepo) List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	args := []interface{}{projectID} // $1
	conditions := []string{"project_id = $1", "deleted_at IS NULL"}
	argIdx := 2

	if len(filter.StatusIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("status_id = ANY($%d)", argIdx))
		args = append(args, pq.Array(filter.StatusIDs))
		argIdx++
	}
	if filter.AssigneeID != nil {
		conditions = append(conditions, fmt.Sprintf("assignee_id = $%d", argIdx))
		args = append(args, *filter.AssigneeID)
		argIdx++
	}
	if filter.AssigneeType != nil {
		conditions = append(conditions, fmt.Sprintf("assignee_type = $%d", argIdx))
		args = append(args, *filter.AssigneeType)
		argIdx++
	}
	if filter.Priority != nil {
		conditions = append(conditions, fmt.Sprintf("priority = $%d", argIdx))
		args = append(args, *filter.Priority)
		argIdx++
	}
	if filter.ParentTaskID != nil {
		conditions = append(conditions, fmt.Sprintf("parent_task_id = $%d", argIdx))
		args = append(args, *filter.ParentTaskID)
		argIdx++
	}
	if len(filter.Labels) > 0 {
		conditions = append(conditions, fmt.Sprintf("labels && $%d", argIdx))
		args = append(args, pq.Array(filter.Labels))
		argIdx++
	}
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		conditions = append(conditions, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d)", argIdx, argIdx))
		args = append(args, pattern)
		argIdx++
	}
	if filter.HasDueDate != nil {
		if *filter.HasDueDate {
			conditions = append(conditions, "due_date IS NOT NULL")
		} else {
			conditions = append(conditions, "due_date IS NULL")
		}
	}

	// Custom field JSONB filters.
	for slug, cf := range filter.CustomFields {
		if !safeSlugRe.MatchString(slug) {
			continue // skip unsafe slugs — defense in depth
		}
		if cf.Eq != nil {
			conditions = append(conditions, fmt.Sprintf("custom_fields->>'%s' = $%d", slug, argIdx))
			args = append(args, fmt.Sprintf("%v", cf.Eq))
			argIdx++
		}
		if cf.Gte != nil {
			conditions = append(conditions, fmt.Sprintf("(custom_fields->>'%s')::numeric >= $%d", slug, argIdx))
			args = append(args, *cf.Gte)
			argIdx++
		}
		if cf.Lte != nil {
			conditions = append(conditions, fmt.Sprintf("(custom_fields->>'%s')::numeric <= $%d", slug, argIdx))
			args = append(args, *cf.Lte)
			argIdx++
		}
		if len(cf.In) > 0 {
			conditions = append(conditions, fmt.Sprintf("custom_fields->>'%s' = ANY($%d)", slug, argIdx))
			args = append(args, pq.Array(cf.In))
			argIdx++
		}
		if cf.IsSet != nil {
			if *cf.IsSet {
				conditions = append(conditions, fmt.Sprintf("custom_fields ? '%s'", slug))
			} else {
				conditions = append(conditions, fmt.Sprintf("NOT (custom_fields ? '%s')", slug))
			}
		}
	}

	where := "WHERE " + joinAnd(conditions)
	order := orderClause(pg, allowedSortColumns{
		"title":      "title",
		"priority":   "priority",
		"position":   "position",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"due_date":   "due_date",
	}, "created_at")

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM tasks %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data
	dataQ := fmt.Sprintf(`SELECT * FROM tasks %s %s %s`, where, order, paginationClause(pg))
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}

func (r *TaskRepo) ListByAssignee(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error) {
	const q = `
		SELECT * FROM tasks
		WHERE assignee_id = $1 AND assignee_type = $2 AND deleted_at IS NULL
		ORDER BY created_at ASC
	`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, assigneeID, assigneeType); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

func (r *TaskRepo) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	const q = `
		SELECT * FROM tasks
		WHERE parent_task_id = $1 AND deleted_at IS NULL
		ORDER BY position ASC, created_at ASC
	`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, parentTaskID); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

func (r *TaskRepo) CountByStatus(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]int, error) {
	const q = `
		SELECT status_id, COUNT(*) as cnt
		FROM tasks
		WHERE project_id = $1 AND deleted_at IS NULL
		GROUP BY status_id
	`
	type row struct {
		StatusID uuid.UUID `db:"status_id"`
		Cnt      int       `db:"cnt"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		result[r.StatusID] = r.Cnt
	}
	return result, nil
}

// CountByStatusCategory returns task counts grouped by status category for a project.
func (r *TaskRepo) CountByStatusCategory(ctx context.Context, projectID uuid.UUID) (map[domain.StatusCategory]int, error) {
	const q = `
		SELECT ts.category, COUNT(t.id) AS cnt
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		WHERE t.project_id = $1 AND t.deleted_at IS NULL
		GROUP BY ts.category
	`
	type row struct {
		Category domain.StatusCategory `db:"category"`
		Cnt      int                   `db:"cnt"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}
	result := make(map[domain.StatusCategory]int, len(rows))
	for _, r := range rows {
		result[r.Category] = r.Cnt
	}
	return result, nil
}

// ListByStatusCategory returns paginated tasks across all projects in a workspace
// that have a status matching the given category.
func (r *TaskRepo) ListByStatusCategory(ctx context.Context, workspaceID uuid.UUID, category domain.StatusCategory, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	const countQ = `
		SELECT COUNT(t.id)
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND ts.category = $2
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
	`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, workspaceID, category); err != nil {
		return nil, err
	}

	const dataQ = `
		SELECT t.*
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND ts.category = $2
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		ORDER BY t.created_at DESC
		LIMIT $3 OFFSET $4
	`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, workspaceID, category, pg.Limit(), pg.Offset()); err != nil {
		return nil, err
	}

	items := taskRowsToSlice(rows)
	return pagination.NewPage(items, totalCount, pg), nil
}
