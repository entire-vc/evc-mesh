package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// AgentEventsRepo implements repository.AgentEventsRepository with PostgreSQL.
type AgentEventsRepo struct {
	db *sqlx.DB
}

// NewAgentEventsRepo creates a new AgentEventsRepo.
func NewAgentEventsRepo(db *sqlx.DB) *AgentEventsRepo {
	return &AgentEventsRepo{db: db}
}

func (r *AgentEventsRepo) Create(ctx context.Context, event *domain.AgentEvent) error {
	const q = `
		INSERT INTO agent_events (event_id, agent_id, workspace_id, event_type, payload, emitted_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.ExecContext(ctx, q,
		event.EventID, event.AgentID, event.WorkspaceID, event.EventType,
		[]byte(event.Payload), event.EmittedAt, event.ExpiresAt,
	)
	return err
}

func (r *AgentEventsRepo) Lookup(ctx context.Context, eventID uuid.UUID) (*domain.AgentEvent, error) {
	const q = `
		SELECT event_id, agent_id, workspace_id, event_type, payload, emitted_at, expires_at
		FROM agent_events
		WHERE event_id = $1 AND expires_at > NOW()
	`
	var ev domain.AgentEvent
	if err := r.db.GetContext(ctx, &ev, q, eventID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &ev, nil
}

func (r *AgentEventsRepo) ListAfter(ctx context.Context, agentID, lastEventID uuid.UUID, limit int) ([]domain.AgentEvent, error) {
	const q = `
		SELECT event_id, agent_id, workspace_id, event_type, payload, emitted_at, expires_at
		FROM agent_events
		WHERE agent_id = $1 AND event_id > $2 AND expires_at > NOW()
		ORDER BY event_id ASC
		LIMIT $3
	`
	var events []domain.AgentEvent
	if err := r.db.SelectContext(ctx, &events, q, agentID, lastEventID, limit); err != nil {
		return nil, err
	}
	if events == nil {
		events = []domain.AgentEvent{}
	}
	return events, nil
}

func (r *AgentEventsRepo) DeleteExpired(ctx context.Context) (int64, error) {
	const q = `DELETE FROM agent_events WHERE expires_at < NOW()`
	res, err := r.db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
