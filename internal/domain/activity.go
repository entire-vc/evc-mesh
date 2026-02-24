package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ActivityLog records an audit entry for any change to an entity.
// Captures who did what, when, and the diff of changes.
type ActivityLog struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	WorkspaceID uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	EntityType  string          `json:"entity_type" db:"entity_type"`
	EntityID    uuid.UUID       `json:"entity_id" db:"entity_id"`
	Action      string          `json:"action" db:"action"`
	ActorID     uuid.UUID       `json:"actor_id" db:"actor_id"`
	ActorType   ActorType       `json:"actor_type" db:"actor_type"`
	Changes     json.RawMessage `json:"changes" db:"changes"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
}
