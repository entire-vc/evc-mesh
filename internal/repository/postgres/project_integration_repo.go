package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

type projectIntegrationRow struct {
	ID        uuid.UUID  `db:"id"`
	ProjectID uuid.UUID  `db:"project_id"`
	Type      string     `db:"type"`
	Enabled   bool       `db:"enabled"`
	Settings  []byte     `db:"settings"`
	AgentKey  *string    `db:"agent_key"`
	CreatedAt time.Time  `db:"created_at"`
	UpdatedAt time.Time  `db:"updated_at"`
	CreatedBy *uuid.UUID `db:"created_by"`
}

func (r *projectIntegrationRow) toDomain() (domain.ProjectIntegration, error) {
	settings := r.Settings
	if settings == nil {
		settings = []byte("{}")
	}
	pi := domain.ProjectIntegration{
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Type:      r.Type,
		Enabled:   r.Enabled,
		Settings:  settings,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		CreatedBy: r.CreatedBy,
	}
	if r.AgentKey != nil && *r.AgentKey != "" {
		plain, err := encryption.Decrypt(*r.AgentKey)
		if err != nil {
			return pi, err
		}
		pi.AgentKey = plain
	}
	return pi, nil
}

// ProjectIntegrationRepo implements repository.ProjectIntegrationRepository using PostgreSQL.
type ProjectIntegrationRepo struct {
	db *sqlx.DB
}

// NewProjectIntegrationRepo creates a new ProjectIntegrationRepo.
func NewProjectIntegrationRepo(db *sqlx.DB) *ProjectIntegrationRepo {
	return &ProjectIntegrationRepo{db: db}
}

// Get retrieves the integration of the given type for the project, or nil if not found.
func (r *ProjectIntegrationRepo) Get(ctx context.Context, projectID uuid.UUID, intType string) (*domain.ProjectIntegration, error) {
	const q = `SELECT id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by FROM project_integrations WHERE project_id = $1 AND type = $2`
	var row projectIntegrationRow
	if err := r.db.GetContext(ctx, &row, q, projectID, intType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	pi, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	return &pi, nil
}

// Upsert inserts or updates a project integration. When agent_key is NULL, the existing value is preserved.
func (r *ProjectIntegrationRepo) Upsert(ctx context.Context, pi *domain.ProjectIntegration) error {
	settings := pi.Settings
	if settings == nil {
		settings = json.RawMessage("{}")
	}
	var encKey *string
	if pi.AgentKey != "" {
		enc, err := encryption.Encrypt(pi.AgentKey)
		if err != nil {
			return err
		}
		encKey = &enc
	}
	const q = `
		INSERT INTO project_integrations (id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (project_id, type)
		DO UPDATE SET
			enabled    = EXCLUDED.enabled,
			settings   = EXCLUDED.settings,
			agent_key  = CASE WHEN EXCLUDED.agent_key IS NOT NULL THEN EXCLUDED.agent_key ELSE project_integrations.agent_key END,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.db.ExecContext(ctx, q,
		pi.ID, pi.ProjectID, pi.Type, pi.Enabled, []byte(settings), encKey,
		pi.CreatedAt, pi.UpdatedAt, pi.CreatedBy,
	)
	return err
}

// Delete removes the integration of the given type for the project.
func (r *ProjectIntegrationRepo) Delete(ctx context.Context, projectID uuid.UUID, intType string) error {
	const q = `DELETE FROM project_integrations WHERE project_id = $1 AND type = $2`
	res, err := r.db.ExecContext(ctx, q, projectID, intType)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("ProjectIntegration")
	}
	return nil
}

// ListByProject returns all integrations for a project, ordered by type.
func (r *ProjectIntegrationRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectIntegration, error) {
	const q = `SELECT id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by FROM project_integrations WHERE project_id = $1 ORDER BY type ASC`
	var rows []projectIntegrationRow
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}
	result := make([]domain.ProjectIntegration, 0, len(rows))
	for i := range rows {
		pi, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		result = append(result, pi)
	}
	return result, nil
}

// Ensure ProjectIntegrationRepo satisfies the repository.ProjectIntegrationRepository interface.
var _ repository.ProjectIntegrationRepository = (*ProjectIntegrationRepo)(nil)
