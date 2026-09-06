package service

import (
	"context"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// qualifiesForTriage reports whether a task's CURRENT human_gate state is a
// legitimate reason to sit in the triage status category.
//
// Requires the gate to be actively armed (task.HumanGate == true), not merely
// classified from a past cycle: GateAuthorType/HumanGateClass are read-only
// history that survives a release (domain.Task doc, HumanGateClass field) —
// without this check, a task that was hard-gated once, answered, and released
// would still read as "hard" forever, silently re-qualifying it for triage on
// any later, unrelated move that has nothing to do with the original gate.
//
// Given an active gate, entry is legitimate when either:
//   - a human posted the marker directly (GateAuthorType == user) — trust the
//     human's own judgment regardless of class, or
//   - the gate is hard-classed (the fail-closed default for a "❓ Blocking
//     @user" marker) — the common case, author-agnostic.
//
// A soft-classed gate armed by an agent does not qualify: soft gates are
// designed to resolve themselves via the default-on-timeout sweep rather than
// sit in the column reserved for "needs human eyes right now" (see
// docs/human-gate-decision-recorded.md §5.1/§5.2 and the [soft] marker tag).
// A task with no active gate at all (the dispatcher's count==3 stale-
// redispatch auto-triage, or any other caller that never went through
// ArmHumanGate) does not qualify either — that is the exact case this gate
// exists to refuse.
func qualifiesForTriage(task *domain.Task) bool {
	if task == nil || !task.HumanGate {
		return false
	}
	if task.GateAuthorType != nil && *task.GateAuthorType == domain.ActorTypeUser {
		return true
	}
	return task.HumanGateClass == domain.HumanGateClassHard
}

// passesTriageEntryGate reports whether a move into the triage category is
// allowed, and whether strict mode was the reason (mirrors
// passesReviewEvidenceGate's shape in review_evidence.go: the config is read
// once so a message rendered from the return value cannot describe a
// different condition than the one that actually decided it).
//
// Fail-open on an unreadable config, same reasoning as midPipelineConfig
// itself: an unreadable project-rules service must not start refusing triage
// moves fleet-wide.
func (s *taskService) passesTriageEntryGate(ctx context.Context, task *domain.Task) (ok, strict bool) {
	strict = s.midPipelineConfig(ctx, task.ProjectID).TriageStrict()
	if !strict {
		return true, false
	}
	return qualifiesForTriage(task), true
}
