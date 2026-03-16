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

// CustomFieldDefinitionRepo implements repository.CustomFieldDefinitionRepository with PostgreSQL.
type CustomFieldDefinitionRepo struct {
	db *sqlx.DB
}

// NewCustomFieldDefinitionRepo creates a new CustomFieldDefinitionRepo.
func NewCustomFieldDefinitionRepo(db *sqlx.DB) *CustomFieldDefinitionRepo {
	return &CustomFieldDefinitionRepo{db: db}
}

func (r *CustomFieldDefinitionRepo) Create(ctx context.Context, field *domain.CustomFieldDefinition) error {
	const q = `
		INSERT INTO custom_field_definitions (
			id, project_id, name, slug, field_type, description,
			options, default_value, is_required, is_visible_to_agents, position, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`
	options := field.Options
	if options == nil {
		options = json.RawMessage(`{}`)
	}
	defaultValue := field.DefaultValue
	if defaultValue == nil {
		defaultValue = json.RawMessage(`null`)
	}
	_, err := r.db.ExecContext(ctx, q,
		field.ID, field.ProjectID, field.Name, field.Slug,
		field.FieldType, field.Description, options, defaultValue,
		field.IsRequired, field.IsVisibleToAgents, field.Position, field.CreatedAt,
	)
	return err
}

func (r *CustomFieldDefinitionRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.CustomFieldDefinition, error) {
	const q = `SELECT * FROM custom_field_definitions WHERE id = $1`
	var field domain.CustomFieldDefinition
	if err := r.db.GetContext(ctx, &field, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &field, nil
}

func (r *CustomFieldDefinitionRepo) Update(ctx context.Context, field *domain.CustomFieldDefinition) error {
	const q = `
		UPDATE custom_field_definitions
		SET name = $2, slug = $3, field_type = $4, description = $5,
		    options = $6, default_value = $7, is_required = $8,
		    is_visible_to_agents = $9, position = $10
		WHERE id = $1
	`
	options := field.Options
	if options == nil {
		options = json.RawMessage(`{}`)
	}
	defaultValue := field.DefaultValue
	if defaultValue == nil {
		defaultValue = json.RawMessage(`null`)
	}
	res, err := r.db.ExecContext(ctx, q,
		field.ID, field.Name, field.Slug, field.FieldType,
		field.Description, options, defaultValue,
		field.IsRequired, field.IsVisibleToAgents, field.Position,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("CustomFieldDefinition")
	}
	return nil
}

func (r *CustomFieldDefinitionRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM custom_field_definitions WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("CustomFieldDefinition")
	}
	return nil
}

func (r *CustomFieldDefinitionRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	const q = `SELECT * FROM custom_field_definitions WHERE project_id = $1 ORDER BY position ASC`
	var fields []domain.CustomFieldDefinition
	if err := r.db.SelectContext(ctx, &fields, q, projectID); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = []domain.CustomFieldDefinition{}
	}
	return fields, nil
}

func (r *CustomFieldDefinitionRepo) ListVisibleToAgents(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	const q = `SELECT * FROM custom_field_definitions WHERE project_id = $1 AND is_visible_to_agents = TRUE ORDER BY position ASC`
	var fields []domain.CustomFieldDefinition
	if err := r.db.SelectContext(ctx, &fields, q, projectID); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = []domain.CustomFieldDefinition{}
	}
	return fields, nil
}

// Reorder updates the position of each custom field definition in the given order.
// fieldIDs[0] gets position 0, fieldIDs[1] gets position 1, etc.
func (r *CustomFieldDefinitionRepo) Reorder(ctx context.Context, projectID uuid.UUID, fieldIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback on error or panic

	// Lock rows to prevent concurrent reorder.
	const lockQ = `SELECT id FROM custom_field_definitions WHERE project_id = $1 FOR UPDATE`
	var ids []uuid.UUID
	if err := tx.SelectContext(ctx, &ids, lockQ, projectID); err != nil {
		return err
	}

	const q = `UPDATE custom_field_definitions SET position = $1 WHERE id = $2 AND project_id = $3`
	for i, id := range fieldIDs {
		res, err := tx.ExecContext(ctx, q, i, id, projectID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return apierror.NotFound(fmt.Sprintf("CustomFieldDefinition %s", id))
		}
	}

	return tx.Commit()
}
