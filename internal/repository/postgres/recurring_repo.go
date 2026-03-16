package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// RecurringRepo implements repository.RecurringRepository with PostgreSQL.
type RecurringRepo struct {
	db *sqlx.DB
}

// NewRecurringRepo creates a new RecurringRepo.
func NewRecurringRepo(db *sqlx.DB) *RecurringRepo {
	return &RecurringRepo{db: db}
}

// recurringRow is the DB row representation, matching all columns in recurring_schedules.
type recurringRow struct {
	ID                  uuid.UUID                 `db:"id"`
	WorkspaceID         uuid.UUID                 `db:"workspace_id"`
	ProjectID           uuid.UUID                 `db:"project_id"`
	TitleTemplate       string                    `db:"title_template"`
	DescriptionTemplate string                    `db:"description_template"`
	Frequency           domain.RecurringFrequency `db:"frequency"`
	CronExpr            string                    `db:"cron_expr"`
	Timezone            string                    `db:"timezone"`
	AssigneeID          *uuid.UUID                `db:"assignee_id"`
	AssigneeType        domain.AssigneeType       `db:"assignee_type"`
	Priority            domain.Priority           `db:"priority"`
	Labels              pq.StringArray            `db:"labels"`
	StatusID            *uuid.UUID                `db:"status_id"`
	IsActive            bool                      `db:"is_active"`
	StartsAt            time.Time                 `db:"starts_at"`
	EndsAt              *time.Time                `db:"ends_at"`
	MaxInstances        *int                      `db:"max_instances"`
	NextRunAt           *time.Time                `db:"next_run_at"`
	LastTriggeredAt     *time.Time                `db:"last_triggered_at"`
	InstanceCount       int                       `db:"instance_count"`
	CreatedBy           uuid.UUID                 `db:"created_by"`
	CreatedByType       domain.ActorType          `db:"created_by_type"`
	CreatedAt           time.Time                 `db:"created_at"`
	UpdatedAt           time.Time                 `db:"updated_at"`
	DeletedAt           *time.Time                `db:"deleted_at"`
}

func (r *recurringRow) toDomain() domain.RecurringSchedule {
	return domain.RecurringSchedule{
		ID:                  r.ID,
		WorkspaceID:         r.WorkspaceID,
		ProjectID:           r.ProjectID,
		TitleTemplate:       r.TitleTemplate,
		DescriptionTemplate: r.DescriptionTemplate,
		Frequency:           r.Frequency,
		CronExpr:            r.CronExpr,
		Timezone:            r.Timezone,
		AssigneeID:          r.AssigneeID,
		AssigneeType:        r.AssigneeType,
		Priority:            r.Priority,
		Labels:              r.Labels,
		StatusID:            r.StatusID,
		IsActive:            r.IsActive,
		StartsAt:            r.StartsAt,
		EndsAt:              r.EndsAt,
		MaxInstances:        r.MaxInstances,
		NextRunAt:           r.NextRunAt,
		LastTriggeredAt:     r.LastTriggeredAt,
		InstanceCount:       r.InstanceCount,
		CreatedBy:           r.CreatedBy,
		CreatedByType:       r.CreatedByType,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
		DeletedAt:           r.DeletedAt,
	}
}

func (r *RecurringRepo) Create(ctx context.Context, schedule *domain.RecurringSchedule) error {
	const q = `
		INSERT INTO recurring_schedules (
			id, workspace_id, project_id,
			title_template, description_template,
			frequency, cron_expr, timezone,
			assignee_id, assignee_type,
			priority, labels, status_id,
			is_active, starts_at, ends_at, max_instances,
			next_run_at, last_triggered_at, instance_count,
			created_by, created_by_type, created_at, updated_at
		) VALUES (
			$1, $2, $3,
			$4, $5,
			$6, $7, $8,
			$9, $10,
			$11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20,
			$21, $22, $23, $24
		)
	`
	labels := schedule.Labels
	if labels == nil {
		labels = pq.StringArray{}
	}
	_, err := r.db.ExecContext(ctx, q,
		schedule.ID, schedule.WorkspaceID, schedule.ProjectID,
		schedule.TitleTemplate, schedule.DescriptionTemplate,
		schedule.Frequency, schedule.CronExpr, schedule.Timezone,
		schedule.AssigneeID, schedule.AssigneeType,
		schedule.Priority, labels, schedule.StatusID,
		schedule.IsActive, schedule.StartsAt, schedule.EndsAt, schedule.MaxInstances,
		schedule.NextRunAt, schedule.LastTriggeredAt, schedule.InstanceCount,
		schedule.CreatedBy, schedule.CreatedByType, schedule.CreatedAt, schedule.UpdatedAt,
	)
	return err
}

func (r *RecurringRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.RecurringSchedule, error) {
	const q = `
		SELECT id, workspace_id, project_id,
			title_template, description_template,
			frequency, cron_expr, timezone,
			assignee_id, assignee_type,
			priority, labels, status_id,
			is_active, starts_at, ends_at, max_instances,
			next_run_at, last_triggered_at, instance_count,
			created_by, created_by_type, created_at, updated_at, deleted_at
		FROM recurring_schedules
		WHERE id = $1 AND deleted_at IS NULL
	`
	var row recurringRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	s := row.toDomain()
	return &s, nil
}

func (r *RecurringRepo) Update(ctx context.Context, schedule *domain.RecurringSchedule) error {
	const q = `
		UPDATE recurring_schedules
		SET title_template = $2,
			description_template = $3,
			frequency = $4,
			cron_expr = $5,
			timezone = $6,
			assignee_id = $7,
			assignee_type = $8,
			priority = $9,
			labels = $10,
			status_id = $11,
			is_active = $12,
			ends_at = $13,
			max_instances = $14,
			next_run_at = $15,
			updated_at = $16
		WHERE id = $1 AND deleted_at IS NULL
	`
	labels := schedule.Labels
	if labels == nil {
		labels = pq.StringArray{}
	}
	res, err := r.db.ExecContext(ctx, q,
		schedule.ID,
		schedule.TitleTemplate,
		schedule.DescriptionTemplate,
		schedule.Frequency,
		schedule.CronExpr,
		schedule.Timezone,
		schedule.AssigneeID,
		schedule.AssigneeType,
		schedule.Priority,
		labels,
		schedule.StatusID,
		schedule.IsActive,
		schedule.EndsAt,
		schedule.MaxInstances,
		schedule.NextRunAt,
		schedule.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("RecurringSchedule")
	}
	return nil
}

// Delete performs a soft delete by setting deleted_at.
func (r *RecurringRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE recurring_schedules SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("RecurringSchedule")
	}
	return nil
}

func (r *RecurringRepo) ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringSchedule], error) {
	pg.Normalize()

	const countQ = `SELECT COUNT(*) FROM recurring_schedules WHERE project_id = $1 AND deleted_at IS NULL`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, projectID); err != nil {
		return nil, err
	}

	order := orderClause(pg, allowedSortColumns{
		"created_at":     "created_at",
		"updated_at":     "updated_at",
		"title_template": "title_template",
	}, "created_at")

	dataQ := fmt.Sprintf(`
		SELECT id, workspace_id, project_id,
			title_template, description_template,
			frequency, cron_expr, timezone,
			assignee_id, assignee_type,
			priority, labels, status_id,
			is_active, starts_at, ends_at, max_instances,
			next_run_at, last_triggered_at, instance_count,
			created_by, created_by_type, created_at, updated_at, deleted_at
		FROM recurring_schedules
		WHERE project_id = $1 AND deleted_at IS NULL
		%s %s
	`, order, paginationClause(pg))

	var rows []recurringRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, projectID); err != nil {
		return nil, err
	}

	items := make([]domain.RecurringSchedule, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

// FindDue finds all active schedules whose next_run_at <= NOW() and that haven't
// already been triggered at or after next_run_at. Uses FOR UPDATE SKIP LOCKED
// for safe concurrent access when multiple API instances run simultaneously.
func (r *RecurringRepo) FindDue(ctx context.Context) ([]domain.RecurringSchedule, error) {
	const q = `
		SELECT id, workspace_id, project_id,
			title_template, description_template,
			frequency, cron_expr, timezone,
			assignee_id, assignee_type,
			priority, labels, status_id,
			is_active, starts_at, ends_at, max_instances,
			next_run_at, last_triggered_at, instance_count,
			created_by, created_by_type, created_at, updated_at, deleted_at
		FROM recurring_schedules
		WHERE is_active = TRUE
		  AND deleted_at IS NULL
		  AND next_run_at <= NOW()
		  AND (last_triggered_at IS NULL OR last_triggered_at < next_run_at)
		FOR UPDATE SKIP LOCKED
	`
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("recurring FindDue begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var rows []recurringRow
	if err := tx.SelectContext(ctx, &rows, q); err != nil {
		return nil, fmt.Errorf("recurring FindDue select: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("recurring FindDue commit: %w", err)
	}

	items := make([]domain.RecurringSchedule, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}
	return items, nil
}

// IncrementInstance atomically updates instance_count, last_triggered_at, and next_run_at.
func (r *RecurringRepo) IncrementInstance(ctx context.Context, id uuid.UUID, nextRunAt *time.Time) error {
	const q = `
		UPDATE recurring_schedules
		SET instance_count = instance_count + 1,
			last_triggered_at = NOW(),
			next_run_at = $2,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`
	res, err := r.db.ExecContext(ctx, q, id, nextRunAt)
	if err != nil {
		return fmt.Errorf("recurring IncrementInstance: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("RecurringSchedule")
	}
	return nil
}

// instanceSummaryRow is the DB row for the history query.
type instanceSummaryRow struct {
	TaskID         uuid.UUID  `db:"task_id"`
	InstanceNumber int        `db:"instance_number"`
	Title          string     `db:"title"`
	StatusCategory string     `db:"status_category"`
	CompletedAt    *time.Time `db:"completed_at"`
	CreatedAt      time.Time  `db:"created_at"`
	LastComment    *string    `db:"last_comment"`
	ArtifactCount  int        `db:"artifact_count"`
}

// GetInstanceHistory returns paginated RecurringInstanceSummary entries for a schedule.
// Each row includes the last comment body (truncated to 2000 chars) and artifact count.
func (r *RecurringRepo) GetInstanceHistory(ctx context.Context, scheduleID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error) {
	pg.Normalize()

	const countQ = `
		SELECT COUNT(*)
		FROM tasks t
		WHERE t.recurring_schedule_id = $1 AND t.deleted_at IS NULL
	`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, scheduleID); err != nil {
		return nil, fmt.Errorf("recurring GetInstanceHistory count: %w", err)
	}

	// Sort direction for history: default newest first.
	sortDir := "DESC"
	if pg.SortDir == "asc" {
		sortDir = "ASC"
	}

	dataQ := fmt.Sprintf(`
		SELECT
			t.id AS task_id,
			t.recurring_instance_number AS instance_number,
			t.title,
			ts.category AS status_category,
			t.completed_at,
			t.created_at,
			LEFT(
				(SELECT c.body FROM comments c WHERE c.task_id = t.id ORDER BY c.created_at DESC LIMIT 1),
				2000
			) AS last_comment,
			(SELECT COUNT(*) FROM artifacts a WHERE a.task_id = t.id)::int AS artifact_count
		FROM tasks t
		LEFT JOIN task_statuses ts ON ts.id = t.status_id
		WHERE t.recurring_schedule_id = $1 AND t.deleted_at IS NULL
		ORDER BY t.recurring_instance_number %s NULLS LAST
		LIMIT $2 OFFSET $3
	`, sortDir)

	var rows []instanceSummaryRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, scheduleID, pg.Limit(), pg.Offset()); err != nil {
		return nil, fmt.Errorf("recurring GetInstanceHistory select: %w", err)
	}

	items := make([]domain.RecurringInstanceSummary, len(rows))
	for i, row := range rows {
		items[i] = domain.RecurringInstanceSummary{
			TaskID:         row.TaskID,
			InstanceNumber: row.InstanceNumber,
			Title:          row.Title,
			StatusCategory: row.StatusCategory,
			CompletedAt:    row.CompletedAt,
			CreatedAt:      row.CreatedAt,
			LastComment:    row.LastComment,
			ArtifactCount:  row.ArtifactCount,
		}
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

// Ensure RecurringRepo implements the interface at compile time.
var _ repository.RecurringRepository = (*RecurringRepo)(nil)
