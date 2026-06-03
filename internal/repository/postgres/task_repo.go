package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// safeSlugRe validates that a custom field slug is a safe SQL identifier.
var safeSlugRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var (
	hexPrefixRe = regexp.MustCompile(`^[0-9a-fA-F]{6,}$`)
	uuidRe      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// searchMode returns the detection mode for a search query: "uuid" for a full
// UUID, "prefix" for a hex-prefix short ID (≥6 hex chars, with optional leading
// #), or "text" for a plain text query. cleanInput has the leading # stripped
// and is lowercased for hex modes so it matches Postgres UUID::text output.
func searchMode(raw string) (mode, cleanInput string) {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if uuidRe.MatchString(s) {
		return "uuid", strings.ToLower(s)
	}
	if hexPrefixRe.MatchString(s) {
		return "prefix", strings.ToLower(s)
	}
	return "text", raw
}

// ErrCheckoutConflict is returned when AtomicCheckout finds the task is already
// checked out by a different agent and the lock has not yet expired.
var ErrCheckoutConflict = errors.New("task is already checked out")

// ErrInvalidCheckoutToken is returned when ReleaseCheckout or ExtendCheckout
// is called with a token that does not match the stored checkout_token.
var ErrInvalidCheckoutToken = errors.New("invalid checkout token")

// taskEnrichedSelect is the SELECT clause used for all task queries that need
// computed fields. It appends 4 correlated subqueries to the base columns so
// that callers always receive subtask_count, assignee_name, artifact_count,
// and vcs_link_count without extra round-trips.
//
// The alias "t" must exist in the surrounding FROM clause (e.g. FROM tasks t
// or when tasks is the only table and no alias is used, replace "t." with "").
// We provide two variants below: one without a table alias (for plain
// FROM tasks queries) and one with "t." (for joined queries).
const taskBaseColsNoAlias = `
	id, project_id, status_id, title, description,
	assignee_id, assignee_type, priority, parent_task_id, position,
	due_date, estimated_hours, custom_fields, labels,
	task_number, created_by, created_by_type, created_at, updated_at,
	completed_at, deleted_at,
	recurring_schedule_id, recurring_instance_number,
	checked_out_by, checkout_token, checkout_expires, checkout_acquired_at`

const taskComputedCols = `
	(SELECT COUNT(*) FROM tasks st WHERE st.parent_task_id = tasks.id AND st.deleted_at IS NULL) AS subtask_count,
	(SELECT COUNT(*) FROM artifacts a WHERE a.task_id = tasks.id) AS artifact_count,
	(SELECT COUNT(*) FROM vcs_links v WHERE v.task_id = tasks.id) AS vcs_link_count,
	CASE
		WHEN tasks.assignee_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = tasks.assignee_id AND deleted_at IS NULL)
		WHEN tasks.assignee_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = tasks.assignee_id)
		ELSE NULL
	END AS assignee_name,
	CASE
		WHEN tasks.created_by_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = tasks.created_by AND deleted_at IS NULL)
		WHEN tasks.created_by_type = 'user' THEN
			(SELECT COALESCE(NULLIF(display_name,''), SPLIT_PART(email,'@',1)) FROM users WHERE id = tasks.created_by)
		ELSE NULL
	END AS created_by_name`

const taskComputedColsAliased = `
	(SELECT COUNT(*) FROM tasks st WHERE st.parent_task_id = t.id AND st.deleted_at IS NULL) AS subtask_count,
	(SELECT COUNT(*) FROM artifacts a WHERE a.task_id = t.id) AS artifact_count,
	(SELECT COUNT(*) FROM vcs_links v WHERE v.task_id = t.id) AS vcs_link_count,
	CASE
		WHEN t.assignee_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = t.assignee_id AND deleted_at IS NULL)
		WHEN t.assignee_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = t.assignee_id)
		ELSE NULL
	END AS assignee_name,
	CASE
		WHEN t.created_by_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = t.created_by AND deleted_at IS NULL)
		WHEN t.created_by_type = 'user' THEN
			(SELECT COALESCE(NULLIF(display_name,''), SPLIT_PART(email,'@',1)) FROM users WHERE id = t.created_by)
		ELSE NULL
	END AS created_by_name`

// taskRow is the DB row representation (includes task_number and deleted_at
// that the domain model does not have, plus 4 computed enrichment fields).
type taskRow struct {
	ID             uuid.UUID           `db:"id"`
	ProjectID      uuid.UUID           `db:"project_id"`
	StatusID       uuid.UUID           `db:"status_id"`
	Title          string              `db:"title"`
	Description    string              `db:"description"`
	AssigneeID     *uuid.UUID          `db:"assignee_id"`
	AssigneeType   domain.AssigneeType `db:"assignee_type"`
	Priority       domain.Priority     `db:"priority"`
	ParentTaskID   *uuid.UUID          `db:"parent_task_id"`
	Position       float64             `db:"position"`
	DueDate        *time.Time          `db:"due_date"`
	EstimatedHours *float64            `db:"estimated_hours"`
	CustomFields   json.RawMessage     `db:"custom_fields"`
	Labels         pq.StringArray      `db:"labels"`
	TaskNumber     int                 `db:"task_number"`
	CreatedBy      uuid.UUID           `db:"created_by"`
	CreatedByType  domain.ActorType    `db:"created_by_type"`
	CreatedAt      time.Time           `db:"created_at"`
	UpdatedAt      time.Time           `db:"updated_at"`
	CompletedAt    *time.Time          `db:"completed_at"`
	DeletedAt      *time.Time          `db:"deleted_at"`

	// Recurring series fields.
	RecurringScheduleID     *uuid.UUID `db:"recurring_schedule_id"`
	RecurringInstanceNumber *int       `db:"recurring_instance_number"`

	// Checkout fields.
	CheckedOutBy       *uuid.UUID `db:"checked_out_by"`
	CheckoutToken      *uuid.UUID `db:"checkout_token"`
	CheckoutExpires    *time.Time `db:"checkout_expires"`
	CheckoutAcquiredAt *time.Time `db:"checkout_acquired_at"`

	// Computed enrichment fields populated by enriched queries.
	SubtaskCount  int     `db:"subtask_count"`
	AssigneeName  *string `db:"assignee_name"`
	CreatedByName *string `db:"created_by_name"`
	ArtifactCount int     `db:"artifact_count"`
	VCSLinkCount  int     `db:"vcs_link_count"`
}

func (r *taskRow) toDomain() domain.Task {
	return domain.Task{
		ID:                      r.ID,
		ProjectID:               r.ProjectID,
		StatusID:                r.StatusID,
		Title:                   r.Title,
		Description:             r.Description,
		AssigneeID:              r.AssigneeID,
		AssigneeType:            r.AssigneeType,
		Priority:                r.Priority,
		ParentTaskID:            r.ParentTaskID,
		Position:                r.Position,
		DueDate:                 r.DueDate,
		EstimatedHours:          r.EstimatedHours,
		CustomFields:            r.CustomFields,
		Labels:                  r.Labels,
		CreatedBy:               r.CreatedBy,
		CreatedByType:           r.CreatedByType,
		CreatedAt:               r.CreatedAt,
		UpdatedAt:               r.UpdatedAt,
		CompletedAt:             r.CompletedAt,
		RecurringScheduleID:     r.RecurringScheduleID,
		RecurringInstanceNumber: r.RecurringInstanceNumber,
		CheckedOutBy:            r.CheckedOutBy,
		CheckoutToken:           r.CheckoutToken,
		CheckoutExpires:         r.CheckoutExpires,
		CheckoutAcquiredAt:      r.CheckoutAcquiredAt,
		SubtaskCount:            r.SubtaskCount,
		AssigneeName:            r.AssigneeName,
		CreatedByName:           r.CreatedByName,
		ArtifactCount:           r.ArtifactCount,
		VCSLinkCount:            r.VCSLinkCount,
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
	// Use pg_advisory_xact_lock to serialize task_number generation per project.
	// Without this, concurrent INSERTs can read the same MAX(task_number) and
	// produce duplicates (the unique constraint catches it, but we'd error out).
	const q = `
		WITH lock AS (
			SELECT pg_advisory_xact_lock(hashtext($2::text))
		)
		INSERT INTO tasks (
			id, project_id, status_id, title, description,
			assignee_id, assignee_type, priority, parent_task_id, position,
			due_date, estimated_hours, custom_fields, labels,
			task_number, created_by, created_by_type, created_at, updated_at, completed_at,
			recurring_schedule_id, recurring_instance_number
		) VALUES (
			$1, $2::uuid, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14,
			(SELECT COALESCE(MAX(task_number), 0) + 1 FROM tasks WHERE project_id = $2::uuid),
			$15, $16, $17, $18, $19,
			$20, $21
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
	dbStart := time.Now()
	_, err := r.db.ExecContext(ctx, q,
		task.ID, task.ProjectID, task.StatusID, task.Title, task.Description,
		task.AssigneeID, task.AssigneeType, task.Priority, task.ParentTaskID, task.Position,
		task.DueDate, task.EstimatedHours, customFields, labels,
		task.CreatedBy, task.CreatedByType, task.CreatedAt, task.UpdatedAt, task.CompletedAt,
		task.RecurringScheduleID, task.RecurringInstanceNumber,
	)
	pkgmetrics.RecordDBQuery("task.create", time.Since(dbStart))
	return err
}

func (r *TaskRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks WHERE id = $1 AND deleted_at IS NULL`
	var row taskRow
	dbStart := time.Now()
	err := r.db.GetContext(ctx, &row, q, id)
	pkgmetrics.RecordDBQuery("task.get", time.Since(dbStart))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t := row.toDomain()
	return &t, nil
}

// GetByShortID resolves a task by UUID hex prefix (6–12 chars). Returns apierror.NotFound
// if no task matches, apierror.BadRequest if multiple tasks share the prefix.
func (r *TaskRepo) GetByShortID(ctx context.Context, prefix string) (*domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks WHERE id::text LIKE $1 AND deleted_at IS NULL LIMIT 2`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, prefix+"%"); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, apierror.NotFound("Task")
	}
	if len(rows) > 1 {
		return nil, apierror.BadRequest("ambiguous short ID: multiple tasks match")
	}
	t := rows[0].toDomain()
	return &t, nil
}

// Search searches tasks across all projects in a workspace by text query and optional filters.
func (r *TaskRepo) Search(ctx context.Context, workspaceID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	args := []interface{}{workspaceID} // $1
	conditions := []string{"p.workspace_id = $1", "t.deleted_at IS NULL", "p.deleted_at IS NULL"}
	argIdx := 2

	if filter.Search != "" {
		mode, clean := searchMode(filter.Search)
		switch mode {
		case "uuid":
			parsed, err := uuid.Parse(clean)
			if err == nil {
				conditions = append(conditions, fmt.Sprintf("t.id = $%d", argIdx))
				args = append(args, parsed)
				argIdx++
			}
		case "prefix":
			conditions = append(conditions, fmt.Sprintf("t.id::text LIKE $%d", argIdx))
			args = append(args, clean+"%")
			argIdx++
		default:
			pattern := "%" + filter.Search + "%"
			conditions = append(conditions, fmt.Sprintf("(t.title ILIKE $%d OR t.description ILIKE $%d)", argIdx, argIdx))
			args = append(args, pattern)
			argIdx++
		}
	}
	if len(filter.StatusIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("t.status_id = ANY($%d)", argIdx))
		args = append(args, pq.Array(filter.StatusIDs))
		argIdx++
	}
	if filter.AssigneeID != nil {
		conditions = append(conditions, fmt.Sprintf("t.assignee_id = $%d", argIdx))
		args = append(args, *filter.AssigneeID)
		argIdx++
	}
	if filter.Priority != nil {
		conditions = append(conditions, fmt.Sprintf("t.priority = $%d", argIdx))
		args = append(args, *filter.Priority)
		argIdx++
	}

	where := "WHERE " + joinAnd(conditions)

	countQ := fmt.Sprintf(`SELECT COUNT(t.id) FROM tasks t INNER JOIN projects p ON p.id = t.project_id %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	dataQ := fmt.Sprintf(`SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number, `+taskComputedColsAliased+`
		FROM tasks t INNER JOIN projects p ON p.id = t.project_id
		%s ORDER BY t.updated_at DESC, t.id ASC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	args = append(args, pg.Limit(), pg.Offset())
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}

func (r *TaskRepo) Update(ctx context.Context, task *domain.Task) error {
	const q = `
		UPDATE tasks
		SET status_id = $2, title = $3, description = $4,
		    assignee_id = $5, assignee_type = $6, priority = $7,
		    parent_task_id = $8, position = $9, due_date = $10,
		    estimated_hours = $11, custom_fields = $12, labels = $13,
		    updated_at = $14, completed_at = $15,
		    recurring_schedule_id = $16, recurring_instance_number = $17
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
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q,
		task.ID, task.StatusID, task.Title, task.Description,
		task.AssigneeID, task.AssigneeType, task.Priority,
		task.ParentTaskID, task.Position, task.DueDate,
		task.EstimatedHours, customFields, labels,
		task.UpdatedAt, task.CompletedAt,
		task.RecurringScheduleID, task.RecurringInstanceNumber,
	)
	pkgmetrics.RecordDBQuery("task.update", time.Since(dbStart))
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
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q, id)
	pkgmetrics.RecordDBQuery("task.delete", time.Since(dbStart))
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
	if filter.StatusCategory != nil {
		// Subquery avoids a JOIN while staying composable with other conditions.
		conditions = append(conditions, fmt.Sprintf(
			"status_id IN (SELECT id FROM task_statuses WHERE project_id = $1 AND category = $%d)", argIdx))
		args = append(args, *filter.StatusCategory)
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
		mode, clean := searchMode(filter.Search)
		switch mode {
		case "uuid":
			parsed, err := uuid.Parse(clean)
			if err == nil {
				conditions = append(conditions, fmt.Sprintf("id = $%d", argIdx))
				args = append(args, parsed)
				argIdx++
			}
		case "prefix":
			conditions = append(conditions, fmt.Sprintf("id::text LIKE $%d", argIdx))
			args = append(args, clean+"%")
			argIdx++
		default:
			pattern := "%" + filter.Search + "%"
			conditions = append(conditions, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d)", argIdx, argIdx))
			args = append(args, pattern)
			argIdx++
		}
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
	// Default to updated_at DESC so any task mutation (create/update/move) floats
	// the task to the top of the first page — the "write-through" effect.
	// Callers may override via explicit sort_by/sort_dir query params.
	if pg.SortBy == "" {
		pg.SortBy = "updated_at"
		pg.SortDir = "desc"
	}
	order := orderClause(pg, allowedSortColumns{
		"title":      "title",
		"priority":   "priority",
		"position":   "position",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"due_date":   "due_date",
	}, "updated_at") + ", tasks.id ASC"

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM tasks %s`, where)
	var totalCount int
	dbStart := time.Now()
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		pkgmetrics.RecordDBQuery("task.list_count", time.Since(dbStart))
		return nil, err
	}
	pkgmetrics.RecordDBQuery("task.list_count", time.Since(dbStart))

	// Data — use enriched select to populate computed fields in a single query.
	dataQ := fmt.Sprintf(`SELECT `+taskBaseColsNoAlias+`, `+taskComputedCols+` FROM tasks %s %s %s`, where, order, paginationClause(pg))
	var rows []taskRow
	dbStart = time.Now()
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		pkgmetrics.RecordDBQuery("task.list", time.Since(dbStart))
		return nil, err
	}
	pkgmetrics.RecordDBQuery("task.list", time.Since(dbStart))

	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}

func (r *TaskRepo) ListByAssignee(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks
		WHERE assignee_id = $1 AND assignee_type = $2 AND deleted_at IS NULL
		ORDER BY created_at ASC`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, assigneeID, assigneeType); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

func (r *TaskRepo) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks
		WHERE parent_task_id = $1 AND deleted_at IS NULL
		ORDER BY position ASC, created_at ASC`
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

	dataQ := `SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number, ` + taskComputedColsAliased + `
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND ts.category = $2
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		ORDER BY t.created_at DESC
		LIMIT $3 OFFSET $4`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, workspaceID, category, pg.Limit(), pg.Offset()); err != nil {
		return nil, err
	}

	items := taskRowsToSlice(rows)
	return pagination.NewPage(items, totalCount, pg), nil
}

// AtomicCheckout attempts to acquire an exclusive application-level lock on the task
// for the given agent. The UPDATE is conditional: it only succeeds when the task is
// unchecked (checked_out_by IS NULL), the existing checkout has expired
// (checkout_expires < now()), or the requesting agent already holds the checkout
// (same-agent re-checkout is idempotent).
//
// If the task is locked by a different, non-expired agent, the UPDATE matches 0 rows
// and ErrCheckoutConflict is returned. No SELECT FOR UPDATE is needed because the
// single UPDATE statement is itself atomic in PostgreSQL.
func (r *TaskRepo) AtomicCheckout(ctx context.Context, taskID, agentID, token uuid.UUID, expiresAt time.Time) error {
	const query = `
		UPDATE tasks
		SET checked_out_by       = $1,
		    checkout_token        = $2,
		    checkout_expires      = $3,
		    checkout_acquired_at  = CASE
		        WHEN checked_out_by = $1 THEN checkout_acquired_at
		        ELSE now()
		    END,
		    updated_at            = now()
		WHERE id = $4
		  AND deleted_at IS NULL
		  AND (
		        checked_out_by IS NULL
		        OR checkout_expires < now()
		        OR checked_out_by = $1
		      )`
	res, err := r.db.ExecContext(ctx, query, agentID, token, expiresAt, taskID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrCheckoutConflict
	}
	return nil
}

// ReleaseCheckout clears the checkout fields on a task. The token must match the
// stored checkout_token — this prevents a different agent from accidentally
// releasing another agent's lock.
func (r *TaskRepo) ReleaseCheckout(ctx context.Context, taskID, token uuid.UUID) error {
	const query = `
		UPDATE tasks
		SET checked_out_by      = NULL,
		    checkout_token       = NULL,
		    checkout_expires     = NULL,
		    checkout_acquired_at = NULL,
		    updated_at           = now()
		WHERE id = $1
		  AND checkout_token = $2
		  AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, query, taskID, token)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrInvalidCheckoutToken
	}
	return nil
}

// MoveToProject atomically moves a task to a different project by updating
// project_id, status_id, task_number (recalculated within the target project),
// and updated_at in a single UPDATE statement.
// Returns apierror.NotFound("Task") when the task does not exist or is soft-deleted.
func (r *TaskRepo) MoveToProject(ctx context.Context, taskID, targetProjectID, targetStatusID uuid.UUID) error {
	const q = `
		WITH lock AS (
			SELECT pg_advisory_xact_lock(hashtext($2::text))
		)
		UPDATE tasks
		SET project_id  = $2::uuid,
		    status_id   = $3,
		    task_number = (SELECT COALESCE(MAX(task_number), 0) + 1 FROM tasks WHERE project_id = $2::uuid AND deleted_at IS NULL),
		    updated_at  = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`
	res, err := r.db.ExecContext(ctx, q, taskID, targetProjectID, targetStatusID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// ForceReleaseCheckout clears the checkout fields without token verification.
// Used by the service layer for auto-release on terminal status transitions
// (done/review/cancelled) and for admin force-unlock via the
// `DELETE /tasks/:id/checkout?force=true` endpoint. Returns nil even when the
// task held no checkout — the caller treats this as idempotent cleanup.
func (r *TaskRepo) ForceReleaseCheckout(ctx context.Context, taskID uuid.UUID) error {
	const query = `
		UPDATE tasks
		SET checked_out_by      = NULL,
		    checkout_token       = NULL,
		    checkout_expires     = NULL,
		    checkout_acquired_at = NULL,
		    updated_at           = now()
		WHERE id = $1
		  AND deleted_at IS NULL`
	_, err := r.db.ExecContext(ctx, query, taskID)
	return err
}

// ReleaseExpiredCheckouts clears checkout fields on all tasks whose checkout_expires
// is in the past. This is called periodically by the background reaper goroutine so
// that orphan locks (held by dead sessions) are reclaimed without waiting for
// another agent to attempt a checkout on the same task.
func (r *TaskRepo) ReleaseExpiredCheckouts(ctx context.Context) (int64, error) {
	const query = `
		UPDATE tasks
		SET checked_out_by      = NULL,
		    checkout_token       = NULL,
		    checkout_expires     = NULL,
		    checkout_acquired_at = NULL,
		    updated_at           = now()
		WHERE checkout_expires < now()
		  AND checkout_expires IS NOT NULL
		  AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ExtendCheckout pushes the checkout_expires deadline forward. The token must match
// and the existing checkout must not already be expired (to prevent hijacking an
// expired slot via extend).
func (r *TaskRepo) ExtendCheckout(ctx context.Context, taskID, token uuid.UUID, newExpires time.Time) error {
	const query = `
		UPDATE tasks
		SET checkout_expires = $1,
		    updated_at       = now()
		WHERE id = $2
		  AND checkout_token   = $3
		  AND deleted_at IS NULL
		  AND checkout_expires >= now()`
	res, err := r.db.ExecContext(ctx, query, newExpires, taskID, token)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrInvalidCheckoutToken
	}
	return nil
}

// ListByUserActive returns active tasks (excluding done/cancelled) assigned to the given user in a workspace.
func (r *TaskRepo) ListByUserActive(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	const countQ = `
		SELECT COUNT(t.id)
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND t.assignee_id = $2
		  AND t.assignee_type = 'user'
		  AND ts.category NOT IN ('done', 'cancelled')
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, workspaceID, userID); err != nil {
		return nil, err
	}

	const dataQ = `SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number, ` + taskComputedColsAliased + `
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND t.assignee_id = $2
		  AND t.assignee_type = 'user'
		  AND ts.category NOT IN ('done', 'cancelled')
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		ORDER BY t.updated_at DESC, t.id ASC
		LIMIT $3 OFFSET $4`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, workspaceID, userID, pg.Limit(), pg.Offset()); err != nil {
		return nil, err
	}
	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}
