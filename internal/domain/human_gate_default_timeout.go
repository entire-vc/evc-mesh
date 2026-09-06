package domain

import (
	"time"

	"github.com/google/uuid"
)

// HumanGateDefaultTimeoutCandidate is one row returned by
// TaskRepository.FindExpiredDefaultGates — the minimal shape the default-on-timeout
// sweep (task #060ccaae) needs to apply a gate's own stated fallback and explain the
// application in a recorded decision. Deliberately not domain.Task, same reasoning as
// HumanGateSoftTimeoutCandidate: the sweep has no use for the rest of the task.
type HumanGateDefaultTimeoutCandidate struct {
	TaskID uuid.UUID
	// RecommendedDefault is guaranteed non-empty here — FindExpiredDefaultGates
	// filters it in SQL, the same structural-proof style as the class literal in
	// FindSoftTimedOutGates. A gate with no stated default has no gate_deadline in
	// the first place (see ArmHumanGate's auto-default logic in task_repo.go), so
	// this candidate shape never needs to represent "no default to apply".
	RecommendedDefault string
	// GateAuthor/GateAuthorType are who asked — carried through as the decision's
	// DecidedBy, since applying the default is mechanically enacting THEIR own
	// pre-stated answer, not a new decision made by the system on their behalf.
	GateAuthor     uuid.UUID
	GateAuthorType ActorType
	ArmedAt        time.Time
	Deadline       time.Time
}
