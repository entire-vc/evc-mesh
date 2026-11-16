package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TurnHooksFor returns the task service's hooks for the turn commands: hand_off
// is held to the same assignee rules as assign_task, and a committed command
// sends the events a move or an assignment would.
func TurnHooksFor(ts TaskService) TurnHooks {
	s, ok := ts.(*taskService)
	if !ok {
		panic(fmt.Sprintf("TurnHooksFor: unsupported TaskService %T", ts))
	}
	return s
}

// CheckHandoffAssignee runs assign_task's assignee checks (typed principal,
// workspace tenancy, accepts assignments) and enrols the agent in the project.
func (s *taskService) CheckHandoffAssignee(ctx context.Context, projectID, agentID uuid.UUID) error {
	return s.ensureAssigneeProjectMember(ctx, projectID, &agentID, domain.AssigneeTypeAgent)
}

// AfterTurn is best effort: the command is already committed.
func (s *taskService) AfterTurn(ctx context.Context, taskID uuid.UUID, kind domain.TurnOutcomeKind) {
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, taskID)
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return
	}
	changes := map[string]any{"source": "typed-outcome", "outcome": kind}
	action := "task.updated"
	if kind == domain.TurnOutcomeHandoff {
		action = "task.assigned"
		changes["assignee_id"] = task.AssigneeID
	}
	// logActivity writes the audit row and publishes the same action to the
	// event bus; a second publishTaskEvent here put every wait/next/hand_off
	// into event_bus_messages twice, 35 ms apart (fde4f82f).
	s.logActivity(ctx, task.ProjectID, taskID, action, changes)
	if kind == domain.TurnOutcomeHandoff {
		s.notifyAssignedAgent(ctx, task, "task.assigned", changes)
	}
}
