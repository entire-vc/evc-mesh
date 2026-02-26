package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// RuleViolationLogRepo implements repository.RuleViolationLogRepository with PostgreSQL.
type RuleViolationLogRepo struct {
	db *sqlx.DB
}

// NewRuleViolationLogRepo creates a new RuleViolationLogRepo.
func NewRuleViolationLogRepo(db *sqlx.DB) *RuleViolationLogRepo {
	return &RuleViolationLogRepo{db: db}
}

// Create inserts a new rule violation log entry.
func (r *RuleViolationLogRepo) Create(ctx context.Context, v *domain.RuleViolationLog) error {
	const q = `
		INSERT INTO rule_violation_logs (id, workspace_id, project_id, actor_id, actor_type, rule_type, violation_detail, action_taken, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		RETURNING id, created_at
	`
	return r.db.QueryRowContext(ctx, q,
		v.ID, v.WorkspaceID, v.ProjectID, v.ActorID, v.ActorType,
		v.RuleType, v.ViolationDetail, v.ActionTaken,
	).Scan(&v.ID, &v.CreatedAt)
}

// ListByWorkspace returns recent violation log entries for the workspace, ordered by created_at DESC.
func (r *RuleViolationLogRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.RuleViolationLog, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		SELECT id, workspace_id, project_id, actor_id, actor_type, rule_type, violation_detail, action_taken, created_at
		FROM rule_violation_logs
		WHERE workspace_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`
	var logs []domain.RuleViolationLog
	if err := r.db.SelectContext(ctx, &logs, q, workspaceID, limit); err != nil {
		return nil, err
	}
	if logs == nil {
		logs = []domain.RuleViolationLog{}
	}
	return logs, nil
}
