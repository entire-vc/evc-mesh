package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ProjectRuleRepo implements repository.ProjectRuleConfigRepository with PostgreSQL.
type ProjectRuleRepo struct {
	db *sqlx.DB
}

// NewProjectRuleRepo creates a new ProjectRuleRepo.
func NewProjectRuleRepo(db *sqlx.DB) *ProjectRuleRepo {
	return &ProjectRuleRepo{db: db}
}

// Upsert inserts or updates a project rule config (keyed on project_id + rule_type).
func (r *ProjectRuleRepo) Upsert(ctx context.Context, rule *domain.ProjectRuleConfig) error {
	const q = `
		INSERT INTO project_rules (id, project_id, rule_type, config, enforcement_mode, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (project_id, rule_type)
		DO UPDATE SET config = EXCLUDED.config, enforcement_mode = EXCLUDED.enforcement_mode, updated_at = NOW()
		RETURNING id, created_at, updated_at
	`
	return r.db.QueryRowContext(ctx, q,
		rule.ID, rule.ProjectID, rule.RuleType, rule.Config, rule.EnforcementMode,
	).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)
}

// GetByType retrieves a project rule config by type. Returns nil if not found.
func (r *ProjectRuleRepo) GetByType(ctx context.Context, projectID uuid.UUID, ruleType string) (*domain.ProjectRuleConfig, error) {
	const q = `SELECT id, project_id, rule_type, config, enforcement_mode, created_at, updated_at FROM project_rules WHERE project_id = $1 AND rule_type = $2`
	var rule domain.ProjectRuleConfig
	if err := r.db.GetContext(ctx, &rule, q, projectID, ruleType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rule, nil
}

// ListByProject returns all rule configs for the given project.
func (r *ProjectRuleRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectRuleConfig, error) {
	const q = `SELECT id, project_id, rule_type, config, enforcement_mode, created_at, updated_at FROM project_rules WHERE project_id = $1 ORDER BY rule_type`
	var rules []domain.ProjectRuleConfig
	if err := r.db.SelectContext(ctx, &rules, q, projectID); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []domain.ProjectRuleConfig{}
	}
	return rules, nil
}

// Delete removes a project rule config by project + type.
func (r *ProjectRuleRepo) Delete(ctx context.Context, projectID uuid.UUID, ruleType string) error {
	const q = `DELETE FROM project_rules WHERE project_id = $1 AND rule_type = $2`
	_, err := r.db.ExecContext(ctx, q, projectID, ruleType)
	return err
}
