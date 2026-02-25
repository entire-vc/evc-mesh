package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// integrationConfigRow is the DB row representation for integration_configs.
type integrationConfigRow struct {
	ID          uuid.UUID `db:"id"`
	WorkspaceID uuid.UUID `db:"workspace_id"`
	Provider    string    `db:"provider"`
	Config      []byte    `db:"config"`
	IsActive    bool      `db:"is_active"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

func (r *integrationConfigRow) toDomain() domain.IntegrationConfig {
	cfg := r.Config
	if cfg == nil {
		cfg = []byte("{}")
	}
	return domain.IntegrationConfig{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		Provider:    domain.IntegrationProvider(r.Provider),
		Config:      cfg,
		IsActive:    r.IsActive,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// IntegrationRepo implements repository.IntegrationRepository with PostgreSQL.
type IntegrationRepo struct {
	db *sqlx.DB
}

// NewIntegrationRepo creates a new IntegrationRepo.
func NewIntegrationRepo(db *sqlx.DB) *IntegrationRepo {
	return &IntegrationRepo{db: db}
}

// Upsert inserts or updates an integration configuration (unique per workspace+provider).
func (r *IntegrationRepo) Upsert(ctx context.Context, cfg *domain.IntegrationConfig) error {
	const q = `
		INSERT INTO integration_configs (
			id, workspace_id, provider, config, is_active, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id, provider)
		DO UPDATE SET config = EXCLUDED.config, is_active = EXCLUDED.is_active, updated_at = EXCLUDED.updated_at
	`
	config := cfg.Config
	if config == nil {
		config = []byte("{}")
	}
	_, err := r.db.ExecContext(ctx, q,
		cfg.ID, cfg.WorkspaceID, string(cfg.Provider), config, cfg.IsActive, cfg.CreatedAt, cfg.UpdatedAt,
	)
	return err
}

// GetByID retrieves an integration config by its ID.
func (r *IntegrationRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	const q = `SELECT * FROM integration_configs WHERE id = $1`
	var row integrationConfigRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ic := row.toDomain()
	return &ic, nil
}

// GetByProvider retrieves the integration config for a workspace+provider pair.
func (r *IntegrationRepo) GetByProvider(ctx context.Context, workspaceID uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	const q = `SELECT * FROM integration_configs WHERE workspace_id = $1 AND provider = $2`
	var row integrationConfigRow
	if err := r.db.GetContext(ctx, &row, q, workspaceID, string(provider)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ic := row.toDomain()
	return &ic, nil
}

// Update applies a partial update to an integration config.
func (r *IntegrationRepo) Update(ctx context.Context, id uuid.UUID, input domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, apierror.NotFound("Integration")
	}

	if input.Config != nil {
		existing.Config = input.Config
	}
	if input.IsActive != nil {
		existing.IsActive = *input.IsActive
	}
	existing.UpdatedAt = time.Now()

	const q = `
		UPDATE integration_configs
		SET config = $2, is_active = $3, updated_at = $4
		WHERE id = $1
	`
	_, err = r.db.ExecContext(ctx, q, id, existing.Config, existing.IsActive, existing.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete removes an integration config by its ID.
func (r *IntegrationRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM integration_configs WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Integration")
	}
	return nil
}

// ListByWorkspace returns all integration configs for a workspace.
func (r *IntegrationRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.IntegrationConfig, error) {
	const q = `SELECT * FROM integration_configs WHERE workspace_id = $1 ORDER BY provider ASC`
	var rows []integrationConfigRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID); err != nil {
		return nil, err
	}
	result := make([]domain.IntegrationConfig, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// Ensure IntegrationRepo satisfies the repository.IntegrationRepository interface.
var _ repository.IntegrationRepository = (*IntegrationRepo)(nil)
