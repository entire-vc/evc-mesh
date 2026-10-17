package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// applyTransitionGate checks WorkflowRulesConfig.Transitions for the status move.
// Returns enforcement mode and a violation message; empty violation = allowed.
// If rulesConfigSvc is nil or config is empty, returns advisory + no violation (allow-all).
func (s *taskService) applyTransitionGate(ctx context.Context, task *domain.Task, oldStatusID uuid.UUID, newStatus *domain.TaskStatus) (enfMode, violation string) {
	if s.rulesConfigSvc == nil {
		return domain.RuleConfigEnforcementAdvisory, ""
	}
	wfResp, err := s.rulesConfigSvc.GetProjectWorkflowRules(ctx, task.ProjectID, nil)
	if err != nil || wfResp == nil || len(wfResp.Transitions) == 0 {
		return domain.RuleConfigEnforcementAdvisory, ""
	}

	oldStatus, err := s.statusRepo.GetByID(ctx, oldStatusID)
	if err != nil || oldStatus == nil {
		return domain.RuleConfigEnforcementAdvisory, ""
	}

	actorID, actorType := actorctx.FromContext(ctx)
	return checkTransitionGate(oldStatus.Slug, newStatus.Slug, transitionActor{ID: actorID, Type: actorType}, task, wfResp.WorkflowRulesConfig)
}

// checkTransitionGate is the pure-function core of the transition gate.
// Returns enforcement mode and violation message; empty violation = allowed.
// Empty Transitions map → allow-all (backward-compatible).
func checkTransitionGate(fromStatus, toStatus string, actor transitionActor, task *domain.Task, cfg domain.WorkflowRulesConfig) (enfMode, violation string) {
	enfMode = cfg.EnforcementMode
	if enfMode == "" {
		enfMode = domain.RuleConfigEnforcementAdvisory
	}

	if len(cfg.Transitions) == 0 {
		return enfMode, ""
	}

	actorType := actor.Type

	// System actors are exempt from all transition enforcement by default.
	if actorType == domain.ActorTypeSystem && !cfg.EnforceSystemActors {
		return enfMode, ""
	}

	tr, ok := cfg.Transitions[fromStatus]
	if !ok {
		// No rule defined for this from-status → no restriction.
		return enfMode, ""
	}

	// (a) Allowed target statuses.
	if len(tr.Allowed) > 0 {
		found := false
		for _, a := range tr.Allowed {
			if a == toStatus {
				found = true
				break
			}
		}
		if !found {
			return enfMode, fmt.Sprintf(
				"transition %q→%q not permitted; allowed targets: [%s]",
				fromStatus, toStatus, strings.Join(tr.Allowed, ", "),
			)
		}
	}

	// (b) Actor restriction via AllowedActors patterns.
	if len(tr.AllowedActors) > 0 {
		if !isTransitionActorAllowed(actor, task, tr.AllowedActors) {
			return enfMode, fmt.Sprintf(
				"actor type %q not permitted for transition %q→%q",
				actorType, fromStatus, toStatus,
			)
		}
	}

	return enfMode, ""
}

// transitionActor identifies who performs a status move.
type transitionActor struct {
	ID   uuid.UUID
	Type domain.ActorType
}

// Task-relative allowed_actors patterns.
const (
	actorPatternAuthor    = "author"     // the one who did the work (pre_review_assignee_id, else assignee)
	actorPatternNotAuthor = "not_author" // anyone except the author
	actorPatternAssignee  = "assignee"   // current assignee
	actorPatternReviewer  = "reviewer"   // task.reviewer_id
)

// isConditionalActorPattern reports whether p depends on the task (not just actor type).
func isConditionalActorPattern(p string) bool {
	switch p {
	case actorPatternAuthor, actorPatternNotAuthor, actorPatternAssignee, actorPatternReviewer:
		return true
	}
	return false
}

// taskAuthorID returns the id of whoever did the work: the assignee stashed
// before the task went to review, else the current assignee.
func taskAuthorID(task *domain.Task) *uuid.UUID {
	if task == nil {
		return nil
	}
	if task.PreReviewAssigneeID != nil {
		return task.PreReviewAssigneeID
	}
	return task.AssigneeID
}

// isTransitionActorAllowed checks if the actor matches any allowed actor pattern.
// Type patterns: "*" (any), "user", "agent", "system".
// Task-relative patterns: "author", "not_author", "assignee", "reviewer".
// A task-relative pattern never matches without a task or an actor id; not_author
// matches when the author is unknown (nobody to exclude).
func isTransitionActorAllowed(actor transitionActor, task *domain.Task, allowed []string) bool {
	s := string(actor.Type)
	same := func(id *uuid.UUID) bool {
		return id != nil && actor.ID != uuid.Nil && *id == actor.ID
	}
	for _, p := range allowed {
		switch p {
		case "*", s:
			return true
		case actorPatternAuthor:
			if same(taskAuthorID(task)) {
				return true
			}
		case actorPatternNotAuthor:
			if task != nil && actor.ID != uuid.Nil && !same(taskAuthorID(task)) {
				return true
			}
		case actorPatternAssignee:
			if task != nil && same(task.AssigneeID) {
				return true
			}
		case actorPatternReviewer:
			if task != nil && same(task.ReviewerID) {
				return true
			}
		}
	}
	return false
}
