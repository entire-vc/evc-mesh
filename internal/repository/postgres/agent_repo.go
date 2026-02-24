package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// agentRow is the DB row representation (includes deleted_at that the domain model does not have).
type agentRow struct {
	ID                  uuid.UUID       `db:"id"`
	WorkspaceID         uuid.UUID       `db:"workspace_id"`
	Name                string          `db:"name"`
	Slug                string          `db:"slug"`
	AgentType           domain.AgentType `db:"agent_type"`
	APIKeyHash          string          `db:"api_key_hash"`
	APIKeyPrefix        string          `db:"api_key_prefix"`
	Capabilities        json.RawMessage `db:"capabilities"`
	Status              domain.AgentStatus `db:"status"`
	LastHeartbeat       *time.Time      `db:"last_heartbeat"`
	CurrentTaskID       *uuid.UUID      `db:"current_task_id"`
	Settings            json.RawMessage `db:"settings"`
	TotalTasksCompleted int             `db:"total_tasks_completed"`
	TotalErrors         int             `db:"total_errors"`
	CreatedAt           time.Time       `db:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"`
	DeletedAt           *time.Time      `db:"deleted_at"`
}

func (r *agentRow) toDomain() domain.Agent {
	return domain.Agent{
		ID:                  r.ID,
		WorkspaceID:         r.WorkspaceID,
		Name:                r.Name,
		Slug:                r.Slug,
		AgentType:           r.AgentType,
		APIKeyHash:          r.APIKeyHash,
		APIKeyPrefix:        r.APIKeyPrefix,
		Capabilities:        r.Capabilities,
		Status:              r.Status,
		LastHeartbeat:       r.LastHeartbeat,
		CurrentTaskID:       r.CurrentTaskID,
		Settings:            r.Settings,
		TotalTasksCompleted: r.TotalTasksCompleted,
		TotalErrors:         r.TotalErrors,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

func agentRowsToSlice(rows []agentRow) []domain.Agent {
	result := make([]domain.Agent, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result
}

// AgentRepo implements repository.AgentRepository with PostgreSQL.
type AgentRepo struct {
	db *sqlx.DB
}

// NewAgentRepo creates a new AgentRepo.
func NewAgentRepo(db *sqlx.DB) *AgentRepo {
	return &AgentRepo{db: db}
}

func (r *AgentRepo) Create(ctx context.Context, agent *domain.Agent) error {
	const q = `
		INSERT INTO agents (
			id, workspace_id, name, slug, agent_type,
			api_key_hash, api_key_prefix, capabilities, status,
			last_heartbeat, current_task_id, settings,
			total_tasks_completed, total_errors,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12,
			$13, $14,
			$15, $16
		)
	`
	capabilities := agent.Capabilities
	if capabilities == nil {
		capabilities = json.RawMessage(`{}`)
	}
	settings := agent.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		agent.ID, agent.WorkspaceID, agent.Name, agent.Slug, agent.AgentType,
		agent.APIKeyHash, agent.APIKeyPrefix, capabilities, agent.Status,
		agent.LastHeartbeat, agent.CurrentTaskID, settings,
		agent.TotalTasksCompleted, agent.TotalErrors,
		agent.CreatedAt, agent.UpdatedAt,
	)
	return err
}

func (r *AgentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
	const q = `SELECT * FROM agents WHERE id = $1 AND deleted_at IS NULL`
	var row agentRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a := row.toDomain()
	return &a, nil
}

func (r *AgentRepo) GetByAPIKeyPrefix(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.Agent, error) {
	const q = `SELECT * FROM agents WHERE workspace_id = $1 AND api_key_prefix = $2 AND deleted_at IS NULL`
	var row agentRow
	if err := r.db.GetContext(ctx, &row, q, workspaceID, prefix); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a := row.toDomain()
	return &a, nil
}

func (r *AgentRepo) Update(ctx context.Context, agent *domain.Agent) error {
	const q = `
		UPDATE agents
		SET name = $2, slug = $3, agent_type = $4,
		    api_key_hash = $5, api_key_prefix = $6,
		    capabilities = $7, status = $8,
		    last_heartbeat = $9, current_task_id = $10,
		    settings = $11, total_tasks_completed = $12,
		    total_errors = $13, updated_at = $14
		WHERE id = $1 AND deleted_at IS NULL
	`
	capabilities := agent.Capabilities
	if capabilities == nil {
		capabilities = json.RawMessage(`{}`)
	}
	settings := agent.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	res, err := r.db.ExecContext(ctx, q,
		agent.ID, agent.Name, agent.Slug, agent.AgentType,
		agent.APIKeyHash, agent.APIKeyPrefix,
		capabilities, agent.Status,
		agent.LastHeartbeat, agent.CurrentTaskID,
		settings, agent.TotalTasksCompleted,
		agent.TotalErrors, agent.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Agent")
	}
	return nil
}

// Delete performs a soft delete by setting deleted_at.
func (r *AgentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE agents SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Agent")
	}
	return nil
}

func (r *AgentRepo) List(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error) {
	pg.Normalize()

	args := []interface{}{workspaceID} // $1
	conditions := []string{"workspace_id = $1", "deleted_at IS NULL"}
	argIdx := 2

	if filter.Status != nil {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *filter.Status)
		argIdx++
	}
	if filter.AgentType != nil {
		conditions = append(conditions, fmt.Sprintf("agent_type = $%d", argIdx))
		args = append(args, *filter.AgentType)
		argIdx++
	}
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		conditions = append(conditions, fmt.Sprintf("(name ILIKE $%d OR slug ILIKE $%d)", argIdx, argIdx))
		args = append(args, pattern)
		argIdx++
	}

	where := "WHERE " + joinAnd(conditions)
	order := orderClause(pg, allowedSortColumns{
		"name":       "name",
		"status":     "status",
		"created_at": "created_at",
		"updated_at": "updated_at",
	}, "created_at")

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM agents %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data
	dataQ := fmt.Sprintf(`SELECT * FROM agents %s %s %s`, where, order, paginationClause(pg))
	var rows []agentRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	return pagination.NewPage(agentRowsToSlice(rows), totalCount, pg), nil
}

func (r *AgentRepo) UpdateHeartbeat(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE agents SET last_heartbeat = NOW(), status = 'online', updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Agent")
	}
	return nil
}

func (r *AgentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.AgentStatus) error {
	const q = `UPDATE agents SET status = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, status)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Agent")
	}
	return nil
}
