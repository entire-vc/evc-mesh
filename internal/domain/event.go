package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// EventType classifies the kind of event published to the event bus.
type EventType string

const (
	EventTypeSummary            EventType = "summary"
	EventTypeStatusChange       EventType = "status_change"
	EventTypeContextUpdate      EventType = "context_update"
	EventTypeError              EventType = "error"
	EventTypeDependencyResolved EventType = "dependency_resolved"
	EventTypeCustom             EventType = "custom"
)

// EventBusMessage is a structured event published to the NATS JetStream event bus.
// Events enable inter-agent context sharing (summaries, decisions, errors).
type EventBusMessage struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	WorkspaceID uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	ProjectID   uuid.UUID       `json:"project_id" db:"project_id"`
	TaskID      *uuid.UUID      `json:"task_id" db:"task_id"`
	AgentID     *uuid.UUID      `json:"agent_id" db:"agent_id"`
	EventType   EventType       `json:"event_type" db:"event_type"`
	Subject     string          `json:"subject" db:"subject"`
	Payload     json.RawMessage `json:"payload" db:"payload"`
	Tags        pq.StringArray  `json:"tags" db:"tags"`
	TTL         string          `json:"ttl" db:"ttl"` // PostgreSQL INTERVAL stored as string
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	ExpiresAt   *time.Time      `json:"expires_at" db:"expires_at"`
}
