package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AgentEvent is a persisted record of a notification delivered to an agent.
// It backs SSE replay: on reconnect the client sends Last-Event-ID and the
// server replays all events with event_id > cursor from this table.
type AgentEvent struct {
	EventID     uuid.UUID       `db:"event_id"     json:"event_id"`
	AgentID     uuid.UUID       `db:"agent_id"     json:"agent_id"`
	WorkspaceID uuid.UUID       `db:"workspace_id" json:"workspace_id"`
	EventType   string          `db:"event_type"   json:"event_type"`
	Payload     json.RawMessage `db:"payload"      json:"payload"`
	EmittedAt   time.Time       `db:"emitted_at"   json:"emitted_at"`
	ExpiresAt   time.Time       `db:"expires_at"   json:"expires_at"`
}
