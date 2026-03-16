package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Ensure AgentActivityLogRepo implements repository.AgentActivityLogRepository at compile time.
var _ repository.AgentActivityLogRepository = (*AgentActivityLogRepo)(nil)

type agentActivityLogRow struct {
	ID          uuid.UUID       `db:"id"`
	AgentID     uuid.UUID       `db:"agent_id"`
	WorkspaceID uuid.UUID       `db:"workspace_id"`
	EventType   string          `db:"event_type"`
	TaskID      *uuid.UUID      `db:"task_id"`
	Message     string          `db:"message"`
	Metadata    json.RawMessage `db:"metadata"`
	CreatedAt   time.Time       `db:"created_at"`
}

func (r *agentActivityLogRow) toDomain() domain.AgentActivityLog {
	return domain.AgentActivityLog{
		ID:          r.ID,
		AgentID:     r.AgentID,
		WorkspaceID: r.WorkspaceID,
		EventType:   r.EventType,
		TaskID:      r.TaskID,
		Message:     r.Message,
		Metadata:    r.Metadata,
		CreatedAt:   r.CreatedAt,
	}
}

// AgentActivityLogRepo implements repository.AgentActivityLogRepository with PostgreSQL.
type AgentActivityLogRepo struct {
	db *sqlx.DB
}

// NewAgentActivityLogRepo creates a new AgentActivityLogRepo.
func NewAgentActivityLogRepo(db *sqlx.DB) *AgentActivityLogRepo {
	return &AgentActivityLogRepo{db: db}
}

func (r *AgentActivityLogRepo) Create(ctx context.Context, entry *domain.AgentActivityLog) error {
	const q = `
		INSERT INTO agent_activity_log (id, agent_id, workspace_id, event_type, task_id, message, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	metadata := entry.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		entry.ID, entry.AgentID, entry.WorkspaceID, entry.EventType,
		entry.TaskID, entry.Message, metadata, entry.CreatedAt,
	)
	return err
}

func (r *AgentActivityLogRepo) List(ctx context.Context, agentID uuid.UUID, filter repository.AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error) {
	pg.Normalize()

	args := []interface{}{agentID}
	conditions := []string{"agent_id = $1"}
	argIdx := 2

	applyActivityFilters(&conditions, &args, argIdx, filter)

	where := "WHERE " + joinAnd(conditions)

	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM agent_activity_log %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	dataQ := fmt.Sprintf(`SELECT * FROM agent_activity_log %s ORDER BY created_at DESC %s`, where, paginationClause(pg))
	var rows []agentActivityLogRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	items := make([]domain.AgentActivityLog, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}
	return pagination.NewPage(items, totalCount, pg), nil
}

func (r *AgentActivityLogRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error) {
	pg.Normalize()

	args := []interface{}{workspaceID}
	conditions := []string{"workspace_id = $1"}
	argIdx := 2

	applyActivityFilters(&conditions, &args, argIdx, filter)

	where := "WHERE " + joinAnd(conditions)

	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM agent_activity_log %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	dataQ := fmt.Sprintf(`SELECT * FROM agent_activity_log %s ORDER BY created_at DESC %s`, where, paginationClause(pg))
	var rows []agentActivityLogRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	items := make([]domain.AgentActivityLog, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}
	return pagination.NewPage(items, totalCount, pg), nil
}

func applyActivityFilters(conditions *[]string, args *[]interface{}, argIdx int, filter repository.AgentActivityLogFilter) {
	if filter.EventType != "" {
		*conditions = append(*conditions, fmt.Sprintf("event_type = $%d", argIdx))
		*args = append(*args, filter.EventType)
		argIdx++
	}
	if filter.Since != nil {
		*conditions = append(*conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		*args = append(*args, *filter.Since)
		argIdx++
	}
	if filter.Until != nil {
		*conditions = append(*conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		*args = append(*args, *filter.Until)
		argIdx++
	}
	_ = argIdx
}
