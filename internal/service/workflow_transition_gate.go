package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// applyTransitionGate checks WorkflowRulesConfig.Transitions for the status move.
// Returns enforcement mode and a violation message; empty violation = allowed.
// If rulesConfigSvc is nil or config is empty, returns advisory + no violation (allow-all).
//
// shipDiag is non-nil only when the refusal came from the author_if_merged_shipped
// pattern: it names which delivery facts are missing so the caller can repair
// them instead of guessing at "actor not permitted".
func (s *taskService) applyTransitionGate(ctx context.Context, task *domain.Task, oldStatusID uuid.UUID, newStatus *domain.TaskStatus) (enfMode, violation string, shipDiag *ShipEvidenceDiagnosis) {
	if s.rulesConfigSvc == nil {
		return domain.RuleConfigEnforcementAdvisory, "", nil
	}
	wfResp, err := s.rulesConfigSvc.GetProjectWorkflowRules(ctx, task.ProjectID, nil)
	if err != nil || wfResp == nil || len(wfResp.Transitions) == 0 {
		return domain.RuleConfigEnforcementAdvisory, "", nil
	}

	oldStatus, err := s.statusRepo.GetByID(ctx, oldStatusID)
	if err != nil || oldStatus == nil {
		return domain.RuleConfigEnforcementAdvisory, "", nil
	}

	actorID, actorType := actorctx.FromContext(ctx)
	var links []domain.VCSLink
	// Links are read only when the rule for this move names the pattern, so
	// projects that do not use it pay nothing and see no change.
	if tr, ok := wfResp.Transitions[oldStatus.Slug]; ok && hasActorPattern(tr.ActorsFor(newStatus.Slug), actorPatternAuthorIfMergedShipped) && s.vcsLinkRepo != nil {
		if ls, lerr := s.vcsLinkRepo.ListByTask(ctx, task.ID); lerr == nil {
			links = ls
			links = s.syncGitHubEvidenceForGate(ctx, task, links)
		}
	}
	return evaluateTransitionGate(oldStatus.Slug, newStatus.Slug, transitionActor{ID: actorID, Type: actorType}, task, links, wfResp.WorkflowRulesConfig)
}

// ShipEvidenceDiagnosis explains an author_if_merged_shipped refusal.
type ShipEvidenceDiagnosis struct {
	Pattern string             `json:"pattern"`
	Links   []ShipEvidenceLink `json:"links"`
	Reasons []string           `json:"reasons"`
}

// ShipEvidenceLink is one delivery MR link and the facts it lacks.
type ShipEvidenceLink struct {
	LinkID     uuid.UUID `json:"link_id"`
	ExternalID string    `json:"external_id"`
	URL        string    `json:"url"`
	Reasons    []string  `json:"reasons"`
}

// ReasonNoDeliveryLink: the card has no delivery MR link at all.
const ReasonNoDeliveryLink = "no_delivery_link"

// diagnoseMergedShipped lists, for every delivery link (same selection as
// mergedShippedEvidence), what is missing. Reasons is the first-seen-order
// union across links. Links that already prove delivery are listed with no reasons.
func diagnoseMergedShipped(links []domain.VCSLink) *ShipEvidenceDiagnosis {
	d := &ShipEvidenceDiagnosis{Pattern: actorPatternAuthorIfMergedShipped, Links: []ShipEvidenceLink{}, Reasons: []string{}}
	seen := map[string]bool{}
	for _, l := range links {
		if l.LinkType != domain.VCSLinkTypePR || l.IsReviewSubject() || l.IsRelatesTo() {
			continue
		}
		gaps := l.ShipEvidenceGaps()
		if gaps == nil {
			gaps = []string{}
		}
		d.Links = append(d.Links, ShipEvidenceLink{LinkID: l.ID, ExternalID: l.ExternalID, URL: l.URL, Reasons: gaps})
		for _, g := range gaps {
			if !seen[g] {
				seen[g] = true
				d.Reasons = append(d.Reasons, g)
			}
		}
	}
	if len(d.Links) == 0 {
		d.Reasons = append(d.Reasons, ReasonNoDeliveryLink)
	}
	return d
}

// ShipNextAction is the safe repair for a missing-evidence refusal. It never
// asks a human to close a technical failure and never asks for a second review
// when a valid exact-HEAD receipt already exists.
const ShipNextAction = "Restore the existing valid independent exact-HEAD SHIP receipt onto the MR link " +
	"(re-publish it so vcs_link.metadata.ship_sha equals head_sha) and retry; " +
	"if no valid receipt exists for the current head, request the standard reviewer. " +
	"Do not ask a human to close this and do not start a new review while a valid receipt exists."

// apiError turns a strict refusal into the 403 payload: error text unchanged
// (workflow_transition_blocked), with the machine facts in Context.
func (d *ShipEvidenceDiagnosis) apiError(violation string) *apierror.Error {
	var parts []string
	for _, l := range d.Links {
		if len(l.Reasons) > 0 {
			parts = append(parts, fmt.Sprintf("MR %s: %s", l.ExternalID, strings.Join(l.Reasons, ",")))
		}
	}
	if len(d.Links) == 0 {
		parts = append(parts, ReasonNoDeliveryLink)
	}
	e := apierror.ForbiddenWithDetails("workflow_transition_blocked",
		fmt.Sprintf("%s; author_if_merged_shipped evidence missing (%s). %s", violation, strings.Join(parts, "; "), ShipNextAction))
	e.Context = map[string]any{
		"gate":        "author_if_merged_shipped",
		"reasons":     d.Reasons,
		"links":       d.Links,
		"next_action": ShipNextAction,
	}
	return e
}

// evaluateTransitionGate is the pure-function core of the transition gate.
// Returns enforcement mode and violation message; empty violation = allowed.
// Empty Transitions map → allow-all (backward-compatible). When the refused actor is
// the task author and the rule offers author_if_merged_shipped, the diagnosis of
// which delivery facts are missing. Other refusals carry no diagnosis.
func evaluateTransitionGate(fromStatus, toStatus string, actor transitionActor, task *domain.Task, links []domain.VCSLink, cfg domain.WorkflowRulesConfig) (enfMode, violation string, diag *ShipEvidenceDiagnosis) {
	enfMode = cfg.EnforcementMode
	if enfMode == "" {
		enfMode = domain.RuleConfigEnforcementAdvisory
	}

	if len(cfg.Transitions) == 0 {
		return enfMode, "", nil
	}

	actorType := actor.Type

	// System actors are exempt from all transition enforcement by default.
	if actorType == domain.ActorTypeSystem && !cfg.EnforceSystemActors {
		return enfMode, "", nil
	}

	tr, ok := cfg.Transitions[fromStatus]
	if !ok {
		// No rule defined for this from-status → no restriction.
		return enfMode, "", nil
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
			), nil
		}
	}

	// (b) Actor restriction via AllowedActors patterns.
	// A per-target entry replaces AllowedActors for this move (an empty list = any).
	if actors := tr.ActorsFor(toStatus); len(actors) > 0 {
		if !isTransitionActorAllowed(actor, task, links, actors) {
			if hasActorPattern(actors, actorPatternAuthorIfMergedShipped) && actor.ID != uuid.Nil {
				if a := taskAuthorID(task); a != nil && *a == actor.ID {
					diag = diagnoseMergedShipped(links)
				}
			}
			return enfMode, fmt.Sprintf(
				"actor type %q not permitted for transition %q→%q",
				actorType, fromStatus, toStatus,
			), diag
		}
	}

	return enfMode, "", nil
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

// syncGitHubEvidenceForGate asks GitHub for the head commit and checks of the
// task's GitHub delivery links, then re-reads the links. Links that already
// look complete are re-verified too, so a cached success is never trusted over
// what GitHub reports now. A link
// created after its PR merged never gets a webhook, so without this the head
// and CI facts stay empty for ever. The gate still judges the stored facts
// only: nothing is written unless GitHub reports them for the PR's own head.
func (s *taskService) syncGitHubEvidenceForGate(ctx context.Context, task *domain.Task, links []domain.VCSLink) []domain.VCSLink {
	need := false
	for _, l := range links {
		need = need || githubLinkIsDelivery(l)
	}
	if !need {
		return links
	}
	workspaceID, _ := s.resolveProjectWorkspace(ctx, task.ProjectID)
	var src githubapi.PullRequestEvidenceSource
	if checker, ok := s.resolveGitHubChecker(ctx, workspaceID); ok {
		src, _ = checker.(githubapi.PullRequestEvidenceSource)
	}
	var changed bool
	unconfirmed := map[uuid.UUID]bool{}
	if src != nil {
		changed, unconfirmed = syncGitHubLinks(ctx, s.vcsLinkRepo, src, links, true)
	} else {
		// GitHub cannot be asked (integration removed, no token): fail closed,
		// a cached green that nothing can confirm does not prove delivery.
		changed = unbindGitHubCachedGreen(ctx, s.vcsLinkRepo, links)
		for _, l := range links {
			if githubLinkIsDelivery(l) {
				unconfirmed[l.ID] = true
			}
		}
	}
	if changed {
		if fresh, err := s.vcsLinkRepo.ListByTask(ctx, task.ID); err == nil {
			links = fresh
		}
	}
	// Whatever the store holds, a link GitHub could not confirm never keeps a
	// cached green binding in the gate's view (a failed unbind write included).
	return withoutCachedGreen(links, unconfirmed)
}
