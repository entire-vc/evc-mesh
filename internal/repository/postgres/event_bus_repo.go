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
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// eventBusRow is the DB row representation.
// The DB stores ttl as INTERVAL and expires_at as NOT NULL TIMESTAMPTZ.
type eventBusRow struct {
	ID          uuid.UUID        `db:"id"`
	WorkspaceID uuid.UUID        `db:"workspace_id"`
	ProjectID   uuid.UUID        `db:"project_id"`
	TaskID      *uuid.UUID       `db:"task_id"`
	AgentID     *uuid.UUID       `db:"agent_id"`
	EventType   domain.EventType `db:"event_type"`
	Subject     string           `db:"subject"`
	Payload     json.RawMessage  `db:"payload"`
	Tags        pq.StringArray   `db:"tags"`
	TTL         string           `db:"ttl"`
	CreatedAt   time.Time        `db:"created_at"`
	ExpiresAt   time.Time        `db:"expires_at"`
	MemoryHint  *json.RawMessage `db:"memory_hint"`
}

func (r *eventBusRow) toDomain() domain.EventBusMessage {
	expiresAt := r.ExpiresAt
	return domain.EventBusMessage{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		ProjectID:   r.ProjectID,
		TaskID:      r.TaskID,
		AgentID:     r.AgentID,
		EventType:   r.EventType,
		Subject:     r.Subject,
		Payload:     r.Payload,
		Tags:        r.Tags,
		TTL:         r.TTL,
		CreatedAt:   r.CreatedAt,
		ExpiresAt:   &expiresAt,
	}
}

// EventBusMessageRepo implements repository.EventBusMessageRepository with PostgreSQL.
type EventBusMessageRepo struct {
	db *sqlx.DB
}

// NewEventBusMessageRepo creates a new EventBusMessageRepo.
func NewEventBusMessageRepo(db *sqlx.DB) *EventBusMessageRepo {
	return &EventBusMessageRepo{db: db}
}

func (r *EventBusMessageRepo) Create(ctx context.Context, msg *domain.EventBusMessage) error {
	// The DB requires expires_at NOT NULL.
	// If the caller set expires_at, use it directly. Otherwise compute from TTL.
	const q = `
		INSERT INTO event_bus_messages (
			id, workspace_id, project_id, task_id, agent_id,
			event_type, subject, payload, tags, ttl, created_at, expires_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, COALESCE(NULLIF($10, '')::INTERVAL, INTERVAL '30 days'),
			$11,
			CASE WHEN $12::TIMESTAMPTZ IS NOT NULL THEN $12::TIMESTAMPTZ
			     ELSE $11::TIMESTAMPTZ + COALESCE(NULLIF($10, '')::INTERVAL, INTERVAL '30 days')
			END
		)
	`
	payload := msg.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	tags := msg.Tags
	if tags == nil {
		tags = pq.StringArray{}
	}

	var expiresAt *time.Time
	if msg.ExpiresAt != nil {
		expiresAt = msg.ExpiresAt
	}

	_, err := r.db.ExecContext(ctx, q,
		msg.ID, msg.WorkspaceID, msg.ProjectID, msg.TaskID, msg.AgentID,
		msg.EventType, msg.Subject, payload, tags, msg.TTL,
		msg.CreatedAt, expiresAt,
	)
	return err
}

// Upsert inserts an event bus message, ignoring conflicts on the primary key.
// This is used by the PG writer to safely persist events that may already exist.
func (r *EventBusMessageRepo) Upsert(ctx context.Context, msg *domain.EventBusMessage) error {
	const q = `
		INSERT INTO event_bus_messages (
			id, workspace_id, project_id, task_id, agent_id,
			event_type, subject, payload, tags, ttl, created_at, expires_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, COALESCE(NULLIF($10, '')::INTERVAL, INTERVAL '30 days'),
			$11,
			CASE WHEN $12::TIMESTAMPTZ IS NOT NULL THEN $12::TIMESTAMPTZ
			     ELSE $11::TIMESTAMPTZ + COALESCE(NULLIF($10, '')::INTERVAL, INTERVAL '30 days')
			END
		)
		ON CONFLICT (id) DO NOTHING
	`
	payload := msg.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	tags := msg.Tags
	if tags == nil {
		tags = pq.StringArray{}
	}

	var expiresAt *time.Time
	if msg.ExpiresAt != nil {
		expiresAt = msg.ExpiresAt
	}

	_, err := r.db.ExecContext(ctx, q,
		msg.ID, msg.WorkspaceID, msg.ProjectID, msg.TaskID, msg.AgentID,
		msg.EventType, msg.Subject, payload, tags, msg.TTL,
		msg.CreatedAt, expiresAt,
	)
	return err
}

func (r *EventBusMessageRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error) {
	const q = `SELECT * FROM event_bus_messages WHERE id = $1`
	var row eventBusRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	m := row.toDomain()
	return &m, nil
}

func (r *EventBusMessageRepo) List(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error) {
	pg.Normalize()

	args := []interface{}{projectID} // $1
	conditions := []string{"project_id = $1"}
	argIdx := 2

	if filter.EventType != nil {
		conditions = append(conditions, fmt.Sprintf("event_type = $%d", argIdx))
		args = append(args, *filter.EventType)
		argIdx++
	}
	if filter.AgentID != nil {
		conditions = append(conditions, fmt.Sprintf("agent_id = $%d", argIdx))
		args = append(args, *filter.AgentID)
		argIdx++
	}
	if filter.TaskID != nil {
		conditions = append(conditions, fmt.Sprintf("task_id = $%d", argIdx))
		args = append(args, *filter.TaskID)
		argIdx++
	}
	if len(filter.Tags) > 0 {
		conditions = append(conditions, fmt.Sprintf("tags && $%d", argIdx))
		args = append(args, pq.Array(filter.Tags))
	}

	where := "WHERE " + joinAnd(conditions)

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM event_bus_messages %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data
	dataQ := fmt.Sprintf(`SELECT * FROM event_bus_messages %s ORDER BY created_at DESC %s`, where, paginationClause(pg))
	var rows []eventBusRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	items := make([]domain.EventBusMessage, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

func (r *EventBusMessageRepo) DeleteExpired(ctx context.Context) (int64, error) {
	const q = `DELETE FROM event_bus_messages WHERE expires_at < NOW()`
	res, err := r.db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
