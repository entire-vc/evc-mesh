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

// Ensure AgentRepo implements repository.AgentRepository at compile time.
var _ repository.AgentRepository = (*AgentRepo)(nil)

// agentRow is the DB row representation (includes deleted_at that the domain model does not have).
type agentRow struct {
	ID                  uuid.UUID          `db:"id"`
	WorkspaceID         uuid.UUID          `db:"workspace_id"`
	ParentAgentID       *uuid.UUID         `db:"parent_agent_id"`
	Name                string             `db:"name"`
	Slug                string             `db:"slug"`
	AgentType           domain.AgentType   `db:"agent_type"`
	APIKeyHash          string             `db:"api_key_hash"`
	APIKeyPrefix        string             `db:"api_key_prefix"`
	Capabilities        json.RawMessage    `db:"capabilities"`
	Status              domain.AgentStatus `db:"status"`
	LastHeartbeat       *time.Time         `db:"last_heartbeat"`
	HeartbeatStatus     string             `db:"heartbeat_status"`
	HeartbeatMessage    string             `db:"heartbeat_message"`
	HeartbeatMetadata   json.RawMessage    `db:"heartbeat_metadata"`
	CurrentTaskID       *uuid.UUID         `db:"current_task_id"`
	Settings            json.RawMessage    `db:"settings"`
	TotalTasksCompleted int                `db:"total_tasks_completed"`
	TotalErrors         int                `db:"total_errors"`
	ExternalAgentID     *string            `db:"external_agent_id"`
	Role               string             `db:"role"`
	ResponsibilityZone string             `db:"responsibility_zone"`
	EscalationTo       *json.RawMessage   `db:"escalation_to"`
	AcceptsFrom        json.RawMessage    `db:"accepts_from"`
	MaxConcurrentTasks int                `db:"max_concurrent_tasks"`
	WorkingHours       string             `db:"working_hours"`
	ProfileDescription string             `db:"profile_description"`
	CallbackURL        string             `db:"callback_url"`
	CreatedAt           time.Time          `db:"created_at"`
	UpdatedAt           time.Time          `db:"updated_at"`
	DeletedAt           *time.Time         `db:"deleted_at"`
}

func (r *agentRow) toDomain() domain.Agent {
	return domain.Agent{
		ID:                  r.ID,
		WorkspaceID:         r.WorkspaceID,
		ParentAgentID:       r.ParentAgentID,
		Name:                r.Name,
		Slug:                r.Slug,
		AgentType:           r.AgentType,
		APIKeyHash:          r.APIKeyHash,
		APIKeyPrefix:        r.APIKeyPrefix,
		Capabilities:        r.Capabilities,
		Status:              r.Status,
		LastHeartbeat:       r.LastHeartbeat,
		HeartbeatStatus:     r.HeartbeatStatus,
		HeartbeatMessage:    r.HeartbeatMessage,
		HeartbeatMetadata:   r.HeartbeatMetadata,
		CurrentTaskID:       r.CurrentTaskID,
		Settings:            r.Settings,
		TotalTasksCompleted: r.TotalTasksCompleted,
		TotalErrors:         r.TotalErrors,
		ExternalAgentID:     r.ExternalAgentID,
		Role:               r.Role,
		ResponsibilityZone: r.ResponsibilityZone,
		EscalationTo:       r.EscalationTo,
		AcceptsFrom:        r.AcceptsFrom,
		MaxConcurrentTasks: r.MaxConcurrentTasks,
		WorkingHours:       r.WorkingHours,
		ProfileDescription: r.ProfileDescription,
		CallbackURL:        r.CallbackURL,
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
			id, workspace_id, parent_agent_id, name, slug, agent_type,
			api_key_hash, api_key_prefix, capabilities, status,
			last_heartbeat, current_task_id, settings,
			total_tasks_completed, total_errors,
			role, responsibility_zone, escalation_to, accepts_from,
			max_concurrent_tasks, working_hours, profile_description,
			callback_url,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13,
			$14, $15,
			$16, $17, $18, $19,
			$20, $21, $22,
			$23,
			$24, $25
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
	acceptsFrom := agent.AcceptsFrom
	if acceptsFrom == nil {
		acceptsFrom = json.RawMessage(`["*"]`)
	}
	_, err := r.db.ExecContext(ctx, q,
		agent.ID, agent.WorkspaceID, agent.ParentAgentID, agent.Name, agent.Slug, agent.AgentType,
		agent.APIKeyHash, agent.APIKeyPrefix, capabilities, agent.Status,
		agent.LastHeartbeat, agent.CurrentTaskID, settings,
		agent.TotalTasksCompleted, agent.TotalErrors,
		agent.Role, agent.ResponsibilityZone, agent.EscalationTo, acceptsFrom,
		agent.MaxConcurrentTasks, agent.WorkingHours, agent.ProfileDescription,
		agent.CallbackURL,
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
		SET parent_agent_id = $2, name = $3, slug = $4, agent_type = $5,
		    api_key_hash = $6, api_key_prefix = $7,
		    capabilities = $8, status = $9,
		    last_heartbeat = $10, current_task_id = $11,
		    settings = $12, total_tasks_completed = $13,
		    total_errors = $14,
		    role = $15, responsibility_zone = $16, escalation_to = $17, accepts_from = $18,
		    max_concurrent_tasks = $19, working_hours = $20, profile_description = $21,
		    callback_url = $22,
		    updated_at = $23
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
	acceptsFrom := agent.AcceptsFrom
	if acceptsFrom == nil {
		acceptsFrom = json.RawMessage(`["*"]`)
	}
	res, err := r.db.ExecContext(ctx, q,
		agent.ID, agent.ParentAgentID, agent.Name, agent.Slug, agent.AgentType,
		agent.APIKeyHash, agent.APIKeyPrefix,
		capabilities, agent.Status,
		agent.LastHeartbeat, agent.CurrentTaskID,
		settings, agent.TotalTasksCompleted,
		agent.TotalErrors,
		agent.Role, agent.ResponsibilityZone, agent.EscalationTo, acceptsFrom,
		agent.MaxConcurrentTasks, agent.WorkingHours, agent.ProfileDescription,
		agent.CallbackURL,
		agent.UpdatedAt,
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
	if filter.ParentAgentID != nil {
		conditions = append(conditions, fmt.Sprintf("parent_agent_id = $%d", argIdx))
		args = append(args, *filter.ParentAgentID)
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

// GetSubAgentTree returns all descendant agents of parentID using a recursive CTE,
// limited to 10 levels of depth. Results are ordered by depth then created_at.
func (r *AgentRepo) GetSubAgentTree(ctx context.Context, parentID uuid.UUID) ([]domain.Agent, error) {
	const q = `
		WITH RECURSIVE agent_tree AS (
			SELECT *, 1 AS depth FROM agents
			WHERE parent_agent_id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT a.*, t.depth + 1 FROM agents a
			INNER JOIN agent_tree t ON a.parent_agent_id = t.id
			WHERE a.deleted_at IS NULL AND t.depth < 10
		)
		SELECT id, workspace_id, parent_agent_id, name, slug, agent_type,
		       api_key_hash, api_key_prefix, capabilities, status,
		       last_heartbeat, heartbeat_status, heartbeat_message, heartbeat_metadata,
		       current_task_id, settings,
		       total_tasks_completed, total_errors, external_agent_id,
		       role, responsibility_zone, escalation_to, accepts_from,
		       max_concurrent_tasks, working_hours, profile_description,
		       callback_url, created_at, updated_at, deleted_at
		FROM agent_tree
		ORDER BY depth, created_at
	`
	var rows []agentRow
	if err := r.db.SelectContext(ctx, &rows, q, parentID); err != nil {
		return nil, err
	}
	return agentRowsToSlice(rows), nil
}

func (r *AgentRepo) UpdateHeartbeat(ctx context.Context, id uuid.UUID, params *repository.UpdateHeartbeatParams) error {
	q := `UPDATE agents SET last_heartbeat = NOW(), status = 'online', updated_at = NOW()`
	args := []interface{}{id}
	argIdx := 2
	if params != nil {
		if params.Status != "" {
			q += fmt.Sprintf(", heartbeat_status = $%d", argIdx)
			args = append(args, params.Status)
			argIdx++
		}
		if params.Message != "" {
			q += fmt.Sprintf(", heartbeat_message = $%d", argIdx)
			args = append(args, params.Message)
			argIdx++
		}
		if params.Metadata != nil {
			q += fmt.Sprintf(", heartbeat_metadata = $%d", argIdx)
			args = append(args, params.Metadata)
			argIdx++
		}
	}
	q += " WHERE id = $1 AND deleted_at IS NULL"
	res, err := r.db.ExecContext(ctx, q, args...)
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

// agentWithProjectsRow is the raw DB row for ListWithProjects.
type agentWithProjectsRow struct {
	agentRow
	ProjectNames json.RawMessage `db:"project_names"`
}

// ListWithProjects returns all agents in a workspace, each annotated with the
// list of project names they participate in through project_members.
func (r *AgentRepo) ListWithProjects(ctx context.Context, workspaceID uuid.UUID) ([]repository.AgentWithProjects, error) {
	const q = `
		SELECT a.id, a.workspace_id, a.parent_agent_id, a.name, a.slug, a.agent_type,
		       a.api_key_hash, a.api_key_prefix, a.capabilities, a.status,
		       a.last_heartbeat, a.heartbeat_status, a.heartbeat_message, a.heartbeat_metadata,
		       a.current_task_id, a.settings,
		       a.total_tasks_completed, a.total_errors, a.external_agent_id,
		       a.role, a.responsibility_zone, a.escalation_to, a.accepts_from,
		       a.max_concurrent_tasks, a.working_hours, a.profile_description,
		       a.callback_url, a.created_at, a.updated_at, a.deleted_at,
		       COALESCE(
		           json_agg(DISTINCT p.name) FILTER (WHERE p.id IS NOT NULL),
		           '[]'::json
		       ) AS project_names
		FROM agents a
		LEFT JOIN project_members pm ON pm.agent_id = a.id
		LEFT JOIN projects p ON p.id = pm.project_id AND p.deleted_at IS NULL
		WHERE a.workspace_id = $1 AND a.deleted_at IS NULL
		GROUP BY a.id
		ORDER BY a.name
	`
	var rows []agentWithProjectsRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID); err != nil {
		return nil, fmt.Errorf("list agents with projects: %w", err)
	}

	result := make([]repository.AgentWithProjects, len(rows))
	for i, row := range rows {
		var projects []string
		if len(row.ProjectNames) > 0 {
			_ = json.Unmarshal(row.ProjectNames, &projects)
		}
		result[i] = repository.AgentWithProjects{
			Agent:    row.agentRow.toDomain(),
			Projects: projects,
		}
	}
	return result, nil
}
