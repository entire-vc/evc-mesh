package domain

import (
	"time"

	"github.com/google/uuid"
)

// TransitionAudit is internal: the actor is taken from authentication context,
// never from client supplied metadata. Entries share one committed task version.
type TransitionAudit struct {
	ActorID         uuid.UUID
	ActorType       ActorType
	Source          string
	Reason          string
	SessionID       *uuid.UUID
	CorrelationID   *uuid.UUID
	TriggerTaskID   *uuid.UUID
	PreviousHolder  *uuid.UUID
	LeaseGeneration int64
	Entries         []TaskAuditEntry
}

type TaskAuditEntry struct {
	Action  string
	Changes map[string]any
}

// TaskOutboxEvent is a committed delivery candidate. The immutable message ID
// is also the activity ID and the sink idempotency key.
type TaskOutboxEvent struct {
	ID        uuid.UUID `db:"id"`
	Message   []byte    `db:"message"`
	Attempts  int       `db:"attempts"`
	CreatedAt time.Time `db:"created_at"`
}
