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

// integrationConfigSelectCols is every column integrationConfigRow scans,
// listed explicitly — see agentSelectCols in agent_repo.go for why
// `SELECT *` is unsafe: sqlx refuses to scan a column with no matching
// struct field, so an additive migration to this table breaks every read
// until redeployed.
const integrationConfigSelectCols = `id, workspace_id, provider, config, is_active, created_at, updated_at`

// NewIntegrationRepo creates a new IntegrationRepo.
func NewIntegrationRepo(db *sqlx.DB) *IntegrationRepo {
	return &IntegrationRepo{db: db}
}

// Upsert inserts or updates an integration configuration (unique per workspace+provider).
func (r *IntegrationRepo) Upsert(ctx context.Context, cfg *domain.IntegrationConfig) error {
	if domain.IsRuntimeProvider(cfg.Provider) {
		return apierror.BadRequest("use the versioned runtime API")
	}
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

// InsertIfAbsent creates the row only when the workspace has none for the
// provider and reports whether it did. Unlike Upsert it never overwrites the
// config of a connection another request just made.
func (r *IntegrationRepo) InsertIfAbsent(ctx context.Context, cfg *domain.IntegrationConfig) (bool, error) {
	if cfg.Provider == domain.IntegrationProviderAgentRuntime {
		return false, apierror.BadRequest("use the versioned runtime API")
	}
	const q = `
		INSERT INTO integration_configs (
			id, workspace_id, provider, config, is_active, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id, provider) DO NOTHING
	`
	config := cfg.Config
	if config == nil {
		config = []byte("{}")
	}
	if cfg.Provider == domain.IntegrationProviderRunner {
		return r.insertRunnerIfAbsent(ctx, cfg, config)
	}
	res, err := r.db.ExecContext(ctx, q,
		cfg.ID, cfg.WorkspaceID, string(cfg.Provider), config, cfg.IsActive, cfg.CreatedAt, cfg.UpdatedAt,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// insertRunnerIfAbsent creates the Runner connection unless the workspace
// already has one under either of its provider names. The unique index is per
// name, so the creators are serialized by the advisory lock the runtime save
// path holds too.
func (r *IntegrationRepo) insertRunnerIfAbsent(ctx context.Context, cfg *domain.IntegrationConfig, config []byte) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	err = lockRunnerConnection(ctx, tx, cfg.WorkspaceID)
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO integration_configs (id, workspace_id, provider, config, is_active, created_at, updated_at)
		SELECT $1::uuid, $2::uuid, $3, $4::jsonb, $5, $6, $7
		WHERE NOT EXISTS (SELECT 1 FROM integration_configs WHERE workspace_id = $2::uuid AND provider IN ('runner', 'agent_runtime'))
		ON CONFLICT (workspace_id, provider) DO NOTHING`,
		cfg.ID, cfg.WorkspaceID, string(cfg.Provider), config, cfg.IsActive, cfg.CreatedAt, cfg.UpdatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}

// GetByID retrieves an integration config by its ID.
func (r *IntegrationRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	const q = `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE id = $1`
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
	const q = `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE workspace_id = $1 AND provider = $2`
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
	if domain.IsRuntimeProvider(existing.Provider) {
		return nil, apierror.BadRequest("use the versioned runtime API")
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

// SetActive switches a connection on or off without touching its config. Unlike
// Update it is allowed on a Runner row: the runtime API's own save flips the
// same column, and nothing here reaches the versioned catalog.
func (r *IntegrationRepo) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE integration_configs SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("Integration")
	}
	return nil
}

// Delete removes an integration config by its ID.
func (r *IntegrationRepo) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing != nil && domain.IsRuntimeProvider(existing.Provider) {
		return apierror.BadRequest("disable runtime through the versioned runtime API")
	}
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
	const q = `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE workspace_id = $1 ORDER BY provider ASC`
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

// ListActiveByProvider returns every active integration config for one
// provider across all workspaces.
func (r *IntegrationRepo) ListActiveByProvider(ctx context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	const q = `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE provider = $1 AND is_active = true ORDER BY workspace_id ASC`
	var rows []integrationConfigRow
	if err := r.db.SelectContext(ctx, &rows, q, string(provider)); err != nil {
		return nil, err
	}
	result := make([]domain.IntegrationConfig, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// ListByProvider returns every integration for one provider across all
// workspaces, active or not. See the interface doc for why this differs
// from ListActiveByProvider.
func (r *IntegrationRepo) ListByProvider(ctx context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error) {
	const q = `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE provider = $1 ORDER BY workspace_id ASC`
	var rows []integrationConfigRow
	if err := r.db.SelectContext(ctx, &rows, q, string(provider)); err != nil {
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
