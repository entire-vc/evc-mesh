package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// AutoTransitionRuleRepo implements repository.AutoTransitionRuleRepository with PostgreSQL.
type AutoTransitionRuleRepo struct {
	db *sqlx.DB
}

// NewAutoTransitionRuleRepo creates a new AutoTransitionRuleRepo.
func NewAutoTransitionRuleRepo(db *sqlx.DB) *AutoTransitionRuleRepo {
	return &AutoTransitionRuleRepo{db: db}
}

// List returns all auto-transition rules for a project, ordered by trigger name.
func (r *AutoTransitionRuleRepo) List(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error) {
	var rules []domain.AutoTransitionRule
	err := r.db.SelectContext(ctx, &rules,
		`SELECT id, project_id, trigger, target_status_id, is_enabled, created_at, updated_at
		 FROM auto_transition_rules
		 WHERE project_id = $1
		 ORDER BY trigger`,
		projectID)
	if err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []domain.AutoTransitionRule{}
	}
	return rules, nil
}

// Get returns a single auto-transition rule by ID, or nil if not found.
func (r *AutoTransitionRuleRepo) Get(ctx context.Context, id uuid.UUID) (*domain.AutoTransitionRule, error) {
	var rule domain.AutoTransitionRule
	err := r.db.GetContext(ctx, &rule,
		`SELECT id, project_id, trigger, target_status_id, is_enabled, created_at, updated_at
		 FROM auto_transition_rules
		 WHERE id = $1`,
		id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rule, nil
}

// Create inserts a new auto-transition rule. Assigns a new UUID if ID is zero.
func (r *AutoTransitionRuleRepo) Create(ctx context.Context, rule *domain.AutoTransitionRule) error {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO auto_transition_rules
		 (id, project_id, trigger, target_status_id, is_enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rule.ID, rule.ProjectID, rule.Trigger, rule.TargetStatusID,
		rule.IsEnabled, rule.CreatedAt, rule.UpdatedAt)
	return err
}

// Update persists changes to target_status_id, is_enabled, and updated_at.
func (r *AutoTransitionRuleRepo) Update(ctx context.Context, rule *domain.AutoTransitionRule) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE auto_transition_rules
		 SET target_status_id = $1, is_enabled = $2, updated_at = $3
		 WHERE id = $4`,
		rule.TargetStatusID, rule.IsEnabled, rule.UpdatedAt, rule.ID)
	return err
}

// Delete removes an auto-transition rule by ID.
func (r *AutoTransitionRuleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM auto_transition_rules WHERE id = $1", id)
	return err
}
