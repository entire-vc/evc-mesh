package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TaskConflict reports the current state after a conditional write lost.
type TaskConflict struct {
	CurrentVersion   int64     `json:"current_version"`
	CurrentStatusID  uuid.UUID `json:"current_status_id"`
	CurrentUpdatedAt time.Time `json:"current_updated_at"`
}

func (e *TaskConflict) Error() string {
	return fmt.Sprintf("cas_conflict: current task version %d", e.CurrentVersion)
}

// ReaperExpectation is internal, never accepted from a request body.
type ReaperExpectation struct {
	Mode       string
	Generation int64
	Token      *uuid.UUID
	ExpiresAt  *time.Time
	QuietGrace time.Duration
}

// TaskTransition updates only transition-owned fields. A reaper alarm and lease
// release, when requested, are committed with the status and assignment.
type TaskTransition struct {
	Audit             *TransitionAudit
	AssignedBy        *AssignmentSource
	ReleaseCheckout   bool
	ExpectedVersion   int64
	ExpectedStatusID  *uuid.UUID
	ExpectedUpdatedAt *time.Time
	Reaper            *ReaperExpectation
	AlarmDue          *time.Time
	AlarmLabels       []string
	// Automatic moves must leave registered WAITs to their atomic consumer.
	DisallowParkedWait bool
}
