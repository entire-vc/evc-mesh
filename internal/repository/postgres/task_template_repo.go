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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskTemplateRepo implements repository.TaskTemplateRepository with PostgreSQL.
type TaskTemplateRepo struct {
	db *sqlx.DB
}

// NewTaskTemplateRepo creates a new TaskTemplateRepo.
func NewTaskTemplateRepo(db *sqlx.DB) *TaskTemplateRepo {
	return &TaskTemplateRepo{db: db}
}

// templateRow is the DB row representation matching all columns in task_templates.
type templateRow struct {
	ID                  uuid.UUID            `db:"id"`
	ProjectID           uuid.UUID            `db:"project_id"`
	Name                string               `db:"name"`
	Description         string               `db:"description"`
	TitleTemplate       string               `db:"title_template"`
	DescriptionTemplate string               `db:"description_template"`
	Priority            domain.Priority      `db:"priority"`
	Labels              pq.StringArray       `db:"labels"`
	EstimatedHours      *float64             `db:"estimated_hours"`
	CustomFields        []byte               `db:"custom_fields"`
	AssigneeID          *uuid.UUID           `db:"assignee_id"`
	AssigneeType        *domain.AssigneeType `db:"assignee_type"`
	StatusID            *uuid.UUID           `db:"status_id"`
	CreatedBy           *uuid.UUID           `db:"created_by"`
	CreatedAt           time.Time            `db:"created_at"`
	UpdatedAt           time.Time            `db:"updated_at"`
}

func (r *templateRow) toDomain() domain.TaskTemplate {
	t := domain.TaskTemplate{
		ID:                  r.ID,
		ProjectID:           r.ProjectID,
		Name:                r.Name,
		Description:         r.Description,
		TitleTemplate:       r.TitleTemplate,
		DescriptionTemplate: r.DescriptionTemplate,
		Priority:            r.Priority,
		Labels:              r.Labels,
		EstimatedHours:      r.EstimatedHours,
		AssigneeID:          r.AssigneeID,
		AssigneeType:        r.AssigneeType,
		StatusID:            r.StatusID,
		CreatedBy:           r.CreatedBy,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
	if len(r.CustomFields) > 0 {
		t.CustomFields = r.CustomFields
	}
	return t
}

// Create inserts a new task template into the database.
func (repo *TaskTemplateRepo) Create(ctx context.Context, tmpl *domain.TaskTemplate) error {
	query := `
		INSERT INTO task_templates (
			id, project_id, name, description,
			title_template, description_template,
			priority, labels, estimated_hours, custom_fields,
			assignee_id, assignee_type, status_id, created_by,
			created_at, updated_at
		) VALUES (
			:id, :project_id, :name, :description,
			:title_template, :description_template,
			:priority, :labels, :estimated_hours, :custom_fields,
			:assignee_id, :assignee_type, :status_id, :created_by,
			:created_at, :updated_at
		)`

	row := &templateRow{
		ID:                  tmpl.ID,
		ProjectID:           tmpl.ProjectID,
		Name:                tmpl.Name,
		Description:         tmpl.Description,
		TitleTemplate:       tmpl.TitleTemplate,
		DescriptionTemplate: tmpl.DescriptionTemplate,
		Priority:            tmpl.Priority,
		Labels:              tmpl.Labels,
		EstimatedHours:      tmpl.EstimatedHours,
		CustomFields:        tmpl.CustomFields,
		AssigneeID:          tmpl.AssigneeID,
		AssigneeType:        tmpl.AssigneeType,
		StatusID:            tmpl.StatusID,
		CreatedBy:           tmpl.CreatedBy,
		CreatedAt:           tmpl.CreatedAt,
		UpdatedAt:           tmpl.UpdatedAt,
	}

	_, err := repo.db.NamedExecContext(ctx, query, row)
	if err != nil {
		return fmt.Errorf("TaskTemplateRepo.Create: %w", err)
	}
	return nil
}

// GetByID fetches a task template by its ID.
func (repo *TaskTemplateRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskTemplate, error) {
	var row templateRow
	err := repo.db.GetContext(ctx, &row,
		`SELECT * FROM task_templates WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("template not found")
		}
		return nil, fmt.Errorf("TaskTemplateRepo.GetByID: %w", err)
	}
	t := row.toDomain()
	return &t, nil
}

// List fetches all task templates for a given project.
func (repo *TaskTemplateRepo) List(ctx context.Context, projectID uuid.UUID) ([]domain.TaskTemplate, error) {
	var rows []templateRow
	err := repo.db.SelectContext(ctx, &rows,
		`SELECT * FROM task_templates WHERE project_id = $1 ORDER BY created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("TaskTemplateRepo.List: %w", err)
	}
	result := make([]domain.TaskTemplate, len(rows))
	for i, r := range rows {
		result[i] = r.toDomain()
	}
	return result, nil
}

// Update applies the given input fields to the template identified by id.
func (repo *TaskTemplateRepo) Update(ctx context.Context, id uuid.UUID, input domain.UpdateTemplateInput) (*domain.TaskTemplate, error) {
	tmpl, err := repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		tmpl.Name = *input.Name
	}
	if input.Description != nil {
		tmpl.Description = *input.Description
	}
	if input.TitleTemplate != nil {
		tmpl.TitleTemplate = *input.TitleTemplate
	}
	if input.DescriptionTemplate != nil {
		tmpl.DescriptionTemplate = *input.DescriptionTemplate
	}
	if input.Priority != nil {
		tmpl.Priority = *input.Priority
	}
	if input.Labels != nil {
		tmpl.Labels = *input.Labels
	}
	if input.EstimatedHours != nil {
		tmpl.EstimatedHours = input.EstimatedHours
	}
	if input.CustomFields != nil {
		tmpl.CustomFields = input.CustomFields
	}
	if input.AssigneeID != nil {
		tmpl.AssigneeID = input.AssigneeID
	}
	if input.AssigneeType != nil {
		tmpl.AssigneeType = input.AssigneeType
	}
	if input.StatusID != nil {
		tmpl.StatusID = input.StatusID
	}
	tmpl.UpdatedAt = time.Now()

	row := &templateRow{
		ID:                  tmpl.ID,
		ProjectID:           tmpl.ProjectID,
		Name:                tmpl.Name,
		Description:         tmpl.Description,
		TitleTemplate:       tmpl.TitleTemplate,
		DescriptionTemplate: tmpl.DescriptionTemplate,
		Priority:            tmpl.Priority,
		Labels:              tmpl.Labels,
		EstimatedHours:      tmpl.EstimatedHours,
		CustomFields:        tmpl.CustomFields,
		AssigneeID:          tmpl.AssigneeID,
		AssigneeType:        tmpl.AssigneeType,
		StatusID:            tmpl.StatusID,
		CreatedBy:           tmpl.CreatedBy,
		CreatedAt:           tmpl.CreatedAt,
		UpdatedAt:           tmpl.UpdatedAt,
	}

	_, err = repo.db.NamedExecContext(ctx, `
		UPDATE task_templates SET
			name = :name,
			description = :description,
			title_template = :title_template,
			description_template = :description_template,
			priority = :priority,
			labels = :labels,
			estimated_hours = :estimated_hours,
			custom_fields = :custom_fields,
			assignee_id = :assignee_id,
			assignee_type = :assignee_type,
			status_id = :status_id,
			updated_at = :updated_at
		WHERE id = :id`, row)
	if err != nil {
		return nil, fmt.Errorf("TaskTemplateRepo.Update: %w", err)
	}
	return tmpl, nil
}

// Delete removes a task template by ID.
func (repo *TaskTemplateRepo) Delete(ctx context.Context, id uuid.UUID) error {
	result, err := repo.db.ExecContext(ctx,
		`DELETE FROM task_templates WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("TaskTemplateRepo.Delete: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("TaskTemplateRepo.Delete rows: %w", err)
	}
	if n == 0 {
		return apierror.NotFound("template not found")
	}
	return nil
}
