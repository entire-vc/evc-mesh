package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RuleRepo implements repository.RuleRepository with PostgreSQL.
type RuleRepo struct {
	db *sqlx.DB
}

// NewRuleRepo creates a new RuleRepo.
func NewRuleRepo(db *sqlx.DB) *RuleRepo {
	return &RuleRepo{db: db}
}

func scanRules(db *sqlx.DB, ctx context.Context, query string, args ...interface{}) ([]domain.Rule, error) {
	rows, err := db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	defer rows.Close()

	var rules []domain.Rule
	for rows.Next() {
		var r domain.Rule
		if err := rows.StructScan(&r); err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}
		if r.AppliesToActorTypes == nil {
			r.AppliesToActorTypes = pq.StringArray{}
		}
		if r.AppliesToRoles == nil {
			r.AppliesToRoles = pq.StringArray{}
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	if rules == nil {
		rules = []domain.Rule{}
	}
	return rules, nil
}

// Create inserts a new rule.
func (r *RuleRepo) Create(ctx context.Context, rule *domain.Rule) error {
	const q = `
		INSERT INTO rules (
			id, workspace_id, project_id, agent_id,
			scope, rule_type, name, description, config,
			applies_to_actor_types, applies_to_roles,
			enforcement, priority, is_enabled,
			created_by, created_by_type, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11,
			$12, $13, $14,
			$15, $16, $17, $18
		)
	`
	_, err := r.db.ExecContext(ctx, q,
		rule.ID, rule.WorkspaceID, rule.ProjectID, rule.AgentID,
		rule.Scope, rule.RuleType, rule.Name, rule.Description, rule.Config,
		rule.AppliesToActorTypes, rule.AppliesToRoles,
		rule.Enforcement, rule.Priority, rule.IsEnabled,
		rule.CreatedBy, rule.CreatedByType, rule.CreatedAt, rule.UpdatedAt,
	)
	return err
}

// GetByID retrieves a rule by its primary key. Returns nil if not found.
func (r *RuleRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Rule, error) {
	const q = `SELECT * FROM rules WHERE id = $1`
	var rule domain.Rule
	if err := r.db.GetContext(ctx, &rule, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if rule.AppliesToActorTypes == nil {
		rule.AppliesToActorTypes = pq.StringArray{}
	}
	if rule.AppliesToRoles == nil {
		rule.AppliesToRoles = pq.StringArray{}
	}
	return &rule, nil
}

// Update persists changes to an existing rule.
func (r *RuleRepo) Update(ctx context.Context, rule *domain.Rule) error {
	const q = `
		UPDATE rules
		SET name = $2, description = $3, config = $4,
		    applies_to_actor_types = $5, applies_to_roles = $6,
		    enforcement = $7, priority = $8, is_enabled = $9,
		    updated_at = $10
		WHERE id = $1
	`
	res, err := r.db.ExecContext(ctx, q,
		rule.ID, rule.Name, rule.Description, rule.Config,
		rule.AppliesToActorTypes, rule.AppliesToRoles,
		rule.Enforcement, rule.Priority, rule.IsEnabled,
		rule.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Rule")
	}
	return nil
}

// Delete removes a rule by ID.
func (r *RuleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM rules WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Rule")
	}
	return nil
}

// ListByWorkspace returns all workspace-scoped rules.
func (r *RuleRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, includeDisabled bool) ([]domain.Rule, error) {
	q := `SELECT * FROM rules WHERE workspace_id = $1 AND scope = 'workspace'`
	if !includeDisabled {
		q += ` AND is_enabled = TRUE`
	}
	q += ` ORDER BY priority ASC, created_at ASC`
	return scanRules(r.db, ctx, q, workspaceID)
}

// ListByProject returns all project-scoped rules for the given project.
func (r *RuleRepo) ListByProject(ctx context.Context, projectID uuid.UUID, includeDisabled bool) ([]domain.Rule, error) {
	q := `SELECT * FROM rules WHERE project_id = $1 AND scope = 'project'`
	if !includeDisabled {
		q += ` AND is_enabled = TRUE`
	}
	q += ` ORDER BY priority ASC, created_at ASC`
	return scanRules(r.db, ctx, q, projectID)
}

// ListByAgent returns all agent-scoped rules for the given agent.
func (r *RuleRepo) ListByAgent(ctx context.Context, agentID uuid.UUID, includeDisabled bool) ([]domain.Rule, error) {
	q := `SELECT * FROM rules WHERE agent_id = $1 AND scope = 'agent'`
	if !includeDisabled {
		q += ` AND is_enabled = TRUE`
	}
	q += ` ORDER BY priority ASC, created_at ASC`
	return scanRules(r.db, ctx, q, agentID)
}

// GetEffective fetches all candidate rules across workspace, project, and agent scopes
// for inheritance resolution. All enabled rules are returned; the caller applies
// the "most specific wins" logic.
func (r *RuleRepo) GetEffective(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Rule, error) {
	// Build a dynamic IN-like query: always include workspace rules, optionally project and agent.
	conds := []string{"(scope = 'workspace' AND workspace_id = $1)"}
	args := []interface{}{workspaceID}
	idx := 2

	if projectID != nil {
		conds = append(conds, fmt.Sprintf("(scope = 'project' AND project_id = $%d)", idx))
		args = append(args, *projectID)
		idx++
	}
	if agentID != nil {
		conds = append(conds, fmt.Sprintf("(scope = 'agent' AND agent_id = $%d)", idx))
		args = append(args, *agentID)
	}

	q := fmt.Sprintf(
		`SELECT * FROM rules WHERE (%s) ORDER BY scope ASC, priority ASC, created_at ASC`,
		strings.Join(conds, " OR "),
	)
	return scanRules(r.db, ctx, q, args...)
}

// CountTasksByAssigneeAndCategory counts tasks assigned to an actor in the given status categories.
// Used by capacity limit evaluators.
func (r *RuleRepo) CountTasksByAssigneeAndCategory(ctx context.Context, workspaceID, assigneeID uuid.UUID, assigneeType string, categories []string) (int, error) {
	if len(categories) == 0 {
		return 0, nil
	}

	// Build $N placeholders for categories.
	placeholders := make([]string, len(categories))
	args := []interface{}{workspaceID, assigneeID, assigneeType}
	for i, cat := range categories {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		args = append(args, cat)
	}

	q := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM tasks t
		JOIN task_statuses ts ON ts.id = t.status_id
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND t.assignee_id = $2
		  AND t.assignee_type = $3
		  AND ts.category = ANY(ARRAY[%s]::text[])
	`, strings.Join(placeholders, ", "))

	var count int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count tasks by assignee and category: %w", err)
	}
	return count, nil
}
