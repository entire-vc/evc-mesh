package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

func transitionSource(source, reason string) string {
	switch source {
	case "api", "ui", "mcp", "lease_reaper.expired", "lease_reaper.unleased", "checkout", "auto_transition.parent", "auto_transition.dependency", "dispatcher.session_exit", "fiddler.recovery", "verify_driver", "queue_balancer":
		return source
	case "auto_transition":
		if reason == "blocking_dep_resolved" {
			return "auto_transition.dependency"
		}
		return "auto_transition.parent"
	default:
		return "api"
	}
}

func newTransitionAudit(ctx context.Context, task *domain.Task, input MoveTaskInput) *domain.TransitionAudit {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorType == "" {
		actorType = domain.ActorTypeSystem
	}
	sessionID := input.SessionID
	if input.Reaper != nil && sessionID == nil {
		sessionID = task.CheckoutSessionID
	}
	return &domain.TransitionAudit{ActorID: actorID, ActorType: actorType, Source: transitionSource(input.Source, input.Reason), Reason: input.Reason, SessionID: sessionID, CorrelationID: input.CorrelationID, TriggerTaskID: input.TriggerTaskID, PreviousHolder: task.CheckedOutBy, LeaseGeneration: task.CheckoutGeneration}
}

// Used by the reaper to avoid writing a second, best-effort lease activity.
// Repository doubles without the transactional writer retain their old path.
func (s *taskService) DurableTaskTransitions() bool {
	_, ok := s.taskRepo.(interface {
		UpdateTransition(context.Context, *domain.Task, domain.TaskTransition) error
	})
	return ok
}

func (s *taskService) moveAudit(ctx context.Context, task *domain.Task, input MoveTaskInput, oldStatus uuid.UUID, oldPosition float64, oldAssignee *uuid.UUID, oldAssigneeType domain.AssigneeType, statusChanged, positionChanged, assignmentChanged bool, assignmentReason string, violation map[string]interface{}) *domain.TransitionAudit {
	audit := newTransitionAudit(ctx, task, input)
	if violation != nil {
		audit.Entries = append(audit.Entries, domain.TaskAuditEntry{Action: "task.transition_violation", Changes: violation})
	}
	changes := map[string]any{}
	if input.Source == "auto_transition" {
		changes["source"] = input.Source
	}
	if statusChanged {
		oldName, newName := oldStatus.String(), task.StatusID.String()
		if st, err := s.statusRepo.GetByID(ctx, oldStatus); err == nil && st != nil {
			oldName = st.Name
		}
		if st, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && st != nil {
			newName = st.Name
		}
		changes["status"] = map[string]any{"old": oldName, "new": newName}
	}
	if positionChanged {
		changes["position"] = map[string]any{"old": oldPosition, "new": task.Position}
	}
	if statusChanged || positionChanged || input.AlarmDue != nil {
		audit.Entries = append(audit.Entries, domain.TaskAuditEntry{Action: "task.moved", Changes: changes})
	}
	if assignmentChanged {
		audit.Entries = append(audit.Entries, domain.TaskAuditEntry{Action: "task.assigned", Changes: map[string]any{"assignee_id": map[string]any{"old": oldAssignee, "new": task.AssigneeID}, "assignee_type": map[string]any{"old": oldAssigneeType, "new": task.AssigneeType}, "reason": assignmentReason}})
	}
	if input.Reaper != nil {
		audit.Entries = append(audit.Entries, domain.TaskAuditEntry{Action: input.Reason, Changes: map[string]any{}})
	}
	return audit
}
