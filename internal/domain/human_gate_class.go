package domain

import (
	"time"

	"github.com/google/uuid"
)

// HumanGateClass classifies an armed human_gate as timeout-eligible or not. See the
// contract at docs/human-gate-decision-recorded.md §5 in the evc-mesh repo.
//
// WHICH real questions get classified soft is deliberately out of scope for the type
// itself — that is a policy decision (a separate card) configured on top of this
// mechanism. Every gate starts (and, on release, resets to) HumanGateClassHard; nothing
// in the arm/release path ever classifies a gate soft on its own.
type HumanGateClass string

const (
	// HumanGateClassHard never times out. There is no code path that releases a hard
	// gate on a clock — SweepExpiredSoftGates' query filters on class in SQL, so a hard
	// row is structurally unreachable from the timeout sweep regardless of how long it
	// has been armed (see task_repo.go FindSoftTimedOutGates).
	HumanGateClassHard HumanGateClass = "hard"
	// HumanGateClassSoft unfreezes once its arm age exceeds the sweep's window. The
	// release does not answer the underlying question — it only stops blocking the
	// fleet — so the card must stay visible in Pavel's digest afterward (contract §5,
	// AC1). See HumanGateSoftTimeoutService.
	HumanGateClassSoft HumanGateClass = "soft"
)

// HumanGateSoftTimeoutCandidate is one row returned by
// TaskRepository.FindSoftTimedOutGates — the minimal shape the sweep needs to release a
// gate and explain the release in a comment. Deliberately not domain.Task: the sweep has
// no use for the rest of the task and fetching it would mean widening taskRow/Update()
// for two columns that only this narrow path touches.
type HumanGateSoftTimeoutCandidate struct {
	TaskID  uuid.UUID
	ArmedAt time.Time
}
