package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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
	var links []domain.VCSLink
	// Links are read only when the rule for this move names the pattern, so
	// projects that do not use it pay nothing and see no change.
	if tr, ok := wfResp.Transitions[oldStatus.Slug]; ok && hasActorPattern(tr.ActorsFor(newStatus.Slug), actorPatternAuthorIfMergedShipped) && s.vcsLinkRepo != nil {
		if ls, lerr := s.vcsLinkRepo.ListByTask(ctx, task.ID); lerr == nil {
			links = ls
		}
	}
	return checkTransitionGate(oldStatus.Slug, newStatus.Slug, transitionActor{ID: actorID, Type: actorType}, task, links, wfResp.WorkflowRulesConfig)
}

// checkTransitionGate is the pure-function core of the transition gate.
// Returns enforcement mode and violation message; empty violation = allowed.
// Empty Transitions map → allow-all (backward-compatible).
func checkTransitionGate(fromStatus, toStatus string, actor transitionActor, task *domain.Task, links []domain.VCSLink, cfg domain.WorkflowRulesConfig) (enfMode, violation string) {
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
	// A per-target entry replaces AllowedActors for this move (an empty list = any).
	if actors := tr.ActorsFor(toStatus); len(actors) > 0 {
		if !isTransitionActorAllowed(actor, task, links, actors) {
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

	// actorPatternAuthorIfMergedShipped lets the author through only with proof
	// of delivery on the card's MR links: merged, SHIP bound to the current
	// head (metadata ship_sha == head_sha), green pipeline. Money/UI/security
	// sign-offs stay separate gates and are not part of this pattern.
	actorPatternAuthorIfMergedShipped = "author_if_merged_shipped"
)

func hasActorPattern(allowed []string, p string) bool {
	for _, a := range allowed {
		if a == p {
			return true
		}
	}
	return false
}

// mergedShippedEvidence: at least one delivery MR link and every one proves
// delivery. Review-subject links point at someone else's MR and are skipped; a
// MR closed without merge is NOT skipped — it is unproven delivery, so the
// author must unlink an abandoned MR before the pattern lets them through.
func mergedShippedEvidence(links []domain.VCSLink) bool {
	n := 0
	for _, l := range links {
		if l.LinkType != domain.VCSLinkTypePR || l.IsReviewSubject() || l.IsRelatesTo() {
			continue
		}
		n++
		if !l.IsMergedShippedGreen() {
			return false
		}
	}
	return n > 0
}

// isConditionalActorPattern reports whether p depends on the task (not just actor type).
func isConditionalActorPattern(p string) bool {
	switch p {
	case actorPatternAuthor, actorPatternNotAuthor, actorPatternAssignee, actorPatternReviewer, actorPatternAuthorIfMergedShipped:
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
// Task-relative patterns: "author", "not_author", "assignee", "reviewer",
// "author_if_merged_shipped" (author + delivery proof in links).
// A task-relative pattern never matches without a task or an actor id; not_author
// matches when the author is unknown (nobody to exclude).
func isTransitionActorAllowed(actor transitionActor, task *domain.Task, links []domain.VCSLink, allowed []string) bool {
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
		case actorPatternAuthorIfMergedShipped:
			if same(taskAuthorID(task)) && mergedShippedEvidence(links) {
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

// validActorPattern reports whether p is a pattern isTransitionActorAllowed knows.
func validActorPattern(p string) bool {
	switch p {
	case "*", string(domain.ActorTypeUser), string(domain.ActorTypeAgent), string(domain.ActorTypeSystem):
		return true
	}
	return isConditionalActorPattern(p)
}

// validateAllowedActorsByTarget rejects unknown patterns and, when knownStatuses
// is non-nil, target statuses that do not exist in the project.
func validateAllowedActorsByTarget(cfg domain.WorkflowRulesConfig, knownStatuses map[string]bool) error {
	for from, tr := range cfg.Transitions {
		for target, actors := range tr.AllowedActorsByTarget {
			if knownStatuses != nil && !knownStatuses[target] {
				return apierror.BadRequest(fmt.Sprintf("transitions.%s.allowed_actors_by_target: unknown target status %q", from, target))
			}
			for _, p := range actors {
				if !validActorPattern(p) {
					return apierror.BadRequest(fmt.Sprintf("transitions.%s.allowed_actors_by_target.%s: unknown actor pattern %q", from, target, p))
				}
			}
		}
	}
	return nil
}
