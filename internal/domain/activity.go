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

	// Additive durable transition metadata. Legacy activities keep null versions.
	EventID         *uuid.UUID `json:"event_id,omitempty" db:"event_id"`
	Source          string     `json:"source" db:"source"`
	Reason          string     `json:"reason,omitempty" db:"reason"`
	SessionID       *uuid.UUID `json:"session_id,omitempty" db:"session_id"`
	CorrelationID   *uuid.UUID `json:"correlation_id,omitempty" db:"correlation_id"`
	OldVersion      *int64     `json:"old_version,omitempty" db:"old_version"`
	NewVersion      *int64     `json:"new_version,omitempty" db:"new_version"`
	LeaseGeneration *int64     `json:"lease_generation,omitempty" db:"lease_generation"`
	PreviousHolder  *uuid.UUID `json:"previous_holder,omitempty" db:"previous_holder"`
	TriggerTaskID   *uuid.UUID `json:"trigger_task_id,omitempty" db:"trigger_task_id"`

	// Computed (not a DB column — populated via subquery in SELECT).
	ActorName *string `json:"actor_name,omitempty" db:"actor_name"`
}
