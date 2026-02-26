package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// WorkspaceRuleRepo implements repository.WorkspaceRuleConfigRepository with PostgreSQL.
type WorkspaceRuleRepo struct {
	db *sqlx.DB
}

// NewWorkspaceRuleRepo creates a new WorkspaceRuleRepo.
func NewWorkspaceRuleRepo(db *sqlx.DB) *WorkspaceRuleRepo {
	return &WorkspaceRuleRepo{db: db}
}

// Upsert inserts or updates a workspace rule config (keyed on workspace_id + rule_type).
func (r *WorkspaceRuleRepo) Upsert(ctx context.Context, rule *domain.WorkspaceRuleConfig) error {
	const q = `
		INSERT INTO workspace_rules (id, workspace_id, rule_type, config, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (workspace_id, rule_type)
		DO UPDATE SET config = EXCLUDED.config, updated_at = NOW()
		RETURNING id, created_at, updated_at
	`
	return r.db.QueryRowContext(ctx, q,
		rule.ID, rule.WorkspaceID, rule.RuleType, rule.Config,
	).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)
}

// GetByType retrieves a workspace rule config by type. Returns nil if not found.
func (r *WorkspaceRuleRepo) GetByType(ctx context.Context, workspaceID uuid.UUID, ruleType string) (*domain.WorkspaceRuleConfig, error) {
	const q = `SELECT id, workspace_id, rule_type, config, created_at, updated_at FROM workspace_rules WHERE workspace_id = $1 AND rule_type = $2`
	var rule domain.WorkspaceRuleConfig
	if err := r.db.GetContext(ctx, &rule, q, workspaceID, ruleType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rule, nil
}

// ListByWorkspace returns all workspace rule configs for the given workspace.
func (r *WorkspaceRuleRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceRuleConfig, error) {
	const q = `SELECT id, workspace_id, rule_type, config, created_at, updated_at FROM workspace_rules WHERE workspace_id = $1 ORDER BY rule_type`
	var rules []domain.WorkspaceRuleConfig
	if err := r.db.SelectContext(ctx, &rules, q, workspaceID); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []domain.WorkspaceRuleConfig{}
	}
	return rules, nil
}

// Delete removes a workspace rule config by workspace + type.
func (r *WorkspaceRuleRepo) Delete(ctx context.Context, workspaceID uuid.UUID, ruleType string) error {
	const q = `DELETE FROM workspace_rules WHERE workspace_id = $1 AND rule_type = $2`
	_, err := r.db.ExecContext(ctx, q, workspaceID, ruleType)
	return err
}
