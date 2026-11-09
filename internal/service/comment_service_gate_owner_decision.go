package service

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/presence"
)

// Next-action texts for HumanGateInfo.NextAction. They are receipts, not claims:
// none of them says the gate is cleared — only GET task human_gate=false does.
const (
	gateNextOwnerWithdraw = "owner: read the decision comment, then act on it and withdraw your ask with a new comment whose last paragraph withdraws it; confirm with GET task human_gate=false"
	gateNextNotOwner      = "not the owner: only the author of the live ask can withdraw it; this comment does not clear the gate"
	gateNextAwaitDecision = "no decision from the ask's addressee after the live marker yet; the gate stays"
)

// applyGateDecision fills the decision_* fields of info from the marker scan
// (task dependency delivery fix). It only REPORTS: it never clears a gate, and it does not
// decide who may — releaseHumanGateOnWithdrawal keeps that.
//
// An answer counts only when ALL of these hold, each closing one way the old
// "someone said resolved" reading could be abused:
//   - it comes after the LIVE marker (a newer marker resets the scan, so a stale
//     or already-superseded decision is never pending);
//   - its author is the marker's addressee: the human user, or the agent whose
//     slug the marker itself names (a user is matched by username, in the task's workspace), or the
//     directory-confirmed lead of the task's project / the CPO (isDelegatedDecisionSource,
//     Lead review). Any other agent, however it describes itself, does not qualify;
//   - it is not a service/auto comment, not a hold line, and not the owner's own;
//   - the body carries text outside quoted spans (a pure quote is not an answer).
//
// The newest qualifying comment wins, so a repeat event maps to ONE decision id.
func (s *commentService) applyGateDecision(ctx context.Context, info *domain.HumanGateInfo, scan *humanGateMarkerScan, taskID uuid.UUID) {
	var wsID uuid.UUID
	wsResolved := false
	info.NextAction = gateNextAwaitDecision
	for i := len(scan.afterMarker) - 1; i >= 0; i-- {
		c := scan.afterMarker[i]
		if c.note || c.authorID == scan.markerAuthorID || c.authorType == domain.ActorTypeSystem {
			continue
		}
		if isServiceComment(c.body) || isHoldMarkerComment(c.body) {
			continue
		}
		if strings.TrimSpace(stripQuotedSpans(c.body)) == "" {
			continue
		}
		if c.authorType == domain.ActorTypeUser && !wsResolved {
			wsID, wsResolved = s.workspaceOfTask(ctx, taskID), true
		}
		if !s.isMarkerAddressee(ctx, c, scan.markerSlugs, wsID) && !s.isDelegatedDecisionSource(ctx, c, taskID) {
			continue
		}
		id := c.id
		info.DecisionPending = true
		info.DecisionCommentID = &id
		info.DecisionAuthorName = c.authorName
		info.NextAction = gateNextOwnerWithdraw
		return
	}
}

// isDelegatedDecisionSource says whether an agent answer may be REPORTED to the gate
// owner although the agent is not the marker's addressee (task dependency delivery fix, Lead
// review, option 1): the authenticated lead of the task's own project (the project's
// effective default_assignee) or the CPO, both read from the team directory, never from
// the comment text. It is a source predicate only: it lets the owner be told, it never
// lets the answerer clear anything (releaseHumanGateOnWithdrawal keeps that), and a
// plain agent, a self-declared role or the word "resolved" never qualify.
func (s *commentService) isDelegatedDecisionSource(ctx context.Context, c gateAnswerCandidate, taskID uuid.UUID) bool {
	if c.authorType != domain.ActorTypeAgent || s.agentSvc == nil {
		return false
	}
	agent, err := s.agentSvc.GetByID(ctx, c.authorID)
	if err != nil || agent == nil {
		return false
	}
	if isCPOAgent(agent) {
		return true
	}
	// Lead needs BOTH facts: the directory says "lead" AND the project routes to this agent.
	// A default assignee alone is routing configuration, not authority.
	if !hasDirectoryRole(agent, "lead") || s.rulesSvc == nil || s.taskRepo == nil {
		return false
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return false
	}
	rules, err := s.rulesSvc.GetEffectiveAssignmentRules(ctx, task.ProjectID)
	if err != nil || rules == nil || rules.DefaultAssignee == nil {
		return false
	}
	lead := strings.TrimSpace(rules.DefaultAssignee.Value)
	if lead == "" {
		return false
	}
	if raw := strings.TrimPrefix(lead, "agent:"); uuid.Validate(raw) == nil {
		return raw == agent.ID.String()
	}
	return strings.EqualFold(lead, agent.Slug)
}

// isCPOAgent reads the role the team directory holds for the agent (Role / ShortTag).
func isCPOAgent(a *domain.Agent) bool { return hasDirectoryRole(a, "cpo") }

func hasDirectoryRole(a *domain.Agent, role string) bool {
	if strings.EqualFold(strings.TrimSpace(a.Role), role) {
		return true
	}
	return a.ShortTag != nil && strings.EqualFold(strings.TrimSpace(*a.ShortTag), role)
}

func (s *commentService) isMarkerAddressee(ctx context.Context, c gateAnswerCandidate, slugs []string, wsID uuid.UUID) bool {
	switch c.authorType {
	case domain.ActorTypeUser:
		// Only the person the live marker names. Resolution failure is a refusal.
		if s.userRepo == nil || wsID == uuid.Nil {
			return false
		}
		for _, sl := range slugs {
			// Same rule as firstResolvedUserSlug: a slug naming a workspace agent is an agent
			// address, never the same-named human login (developer-one, developer-two).
			if s.agentSvc != nil {
				ag, err := s.agentSvc.GetBySlug(ctx, wsID, sl)
				if err != nil {
					// Not found is (nil, nil); an error means we cannot tell an agent address
					// from a human login, so refuse rather than fall through to the human.
					log.Printf("[human-gate] decision check: agent slug %q lookup failed, not an addressee: %v", sl, err)
					return false
				}
				if ag != nil {
					continue
				}
			}
			if u, err := s.userRepo.GetByUsername(ctx, wsID, sl); err == nil && u != nil && u.ID == c.authorID {
				return true
			}
		}
		return false
	case domain.ActorTypeAgent:
		if s.agentSvc == nil {
			return false
		}
		agent, err := s.agentSvc.GetByID(ctx, c.authorID)
		if err != nil || agent == nil {
			log.Printf("[human-gate] decision check: agent %s lookup failed, not an addressee: %v", c.authorID, err)
			return false
		}
		for _, sl := range slugs {
			if strings.EqualFold(sl, agent.Slug) {
				return true
			}
		}
	}
	return false
}

// gateWithdrawalReceipt says what became of a withdrawal-shaped comment (task dependency delivery fix AC4).
// Read-only: it re-reads the task and the ownership report after releaseHumanGateOnWithdrawal
// has run and never changes anything. It answers only for agent comments that read as a
// withdrawal (negator in scope, not a service/hold line); anything else gets no receipt.
func (s *commentService) gateWithdrawalReceipt(ctx context.Context, comment *domain.Comment, task *domain.Task, wasGated bool) *domain.GateReceipt {
	if comment.AuthorType != domain.ActorTypeAgent || isServiceComment(comment.Body) ||
		isHoldMarkerComment(comment.Body) || !hasNegatorInScope(comment.Body) {
		return nil
	}
	if !wasGated {
		return &domain.GateReceipt{Outcome: domain.GateReceiptNoOp, Reason: "gate_not_raised",
			NextAction: "nothing to withdraw: GET task human_gate is already false"}
	}
	if s.taskRepo == nil {
		return nil
	}
	fresh, err := s.taskRepo.GetByID(ctx, task.ID)
	if err != nil || fresh == nil {
		return nil
	}
	if !fresh.HumanGate {
		return &domain.GateReceipt{Outcome: domain.GateReceiptCleared,
			NextAction: "gate lifted; continue or close the task and confirm with GET task human_gate=false"}
	}
	info, err := s.GetHumanGateOwner(ctx, task.ID)
	if err != nil || info == nil {
		return nil
	}
	if info.OwnerAgentID == nil || *info.OwnerAgentID != comment.AuthorID {
		return &domain.GateReceipt{Outcome: domain.GateReceiptNotOwner, Reason: info.ReasonIfNot, NextAction: gateNextNotOwner}
	}
	return &domain.GateReceipt{Outcome: domain.GateReceiptStale, Reason: info.ReasonIfNot,
		NextAction: "gate is still raised: you own the ask but this comment did not lift it (see reason; repeat the withdrawal as a separate comment once it applies) - verify with GET task human_gate"}
}

// recordGateOwnerDelivery writes the delivery receipt for the gate owner when THIS comment
// is the pending answer of the ask's addressee, even if the comment never @-mentions the
// owner (the runner reads human_gate_info itself; the mention is not what wakes anyone).
// The row is honest about reach: IsAssignee decides delivered/gate_owner_decision versus
// not_assignee, so an owner who does not hold the card is reported as not reached.
// Skipped when the mention path already recorded an agent row for the owner.
func (s *commentService) recordGateOwnerDelivery(ctx context.Context, comment *domain.Comment, task *domain.Task) {
	if !task.HumanGate || s.deliveryRepo == nil || s.agentSvc == nil || comment.AuthorType == domain.ActorTypeSystem {
		return
	}
	info, err := s.GetHumanGateOwner(ctx, task.ID)
	if err != nil || info == nil || !info.DecisionPending || info.DecisionCommentID == nil ||
		*info.DecisionCommentID != comment.ID || info.OwnerAgentID == nil {
		return
	}
	owner, err := s.agentSvc.GetByID(ctx, *info.OwnerAgentID)
	if err != nil || owner == nil {
		return
	}
	if rows, lerr := s.deliveryRepo.ListByCommentIDs(ctx, []uuid.UUID{comment.ID}); lerr == nil {
		for _, r := range rows[comment.ID] {
			if r.RecipientKind == domain.RecipientKindAgent && r.RecipientID != nil && *r.RecipientID == owner.ID {
				return
			}
		}
	}
	now := timeNow()
	statusCategory := s.taskStatusCategory(ctx, task)
	row := newOutcomeRow(comment.ID, deliveryFacts{
		Slug:              owner.Slug,
		Agent:             owner,
		IsAssignee:        taskAssignedToAgent(task, owner.ID),
		StatusCategory:    statusCategory,
		Gated:             true,
		GateOwnerDecision: true,
		Scheduled:         task.StartAfter != nil && task.StartAfter.After(now),
		StreamConnected:   presence.IsConnected(owner.ID),
		Presence:          owner.ComputedStatus(presence.IsConnected(owner.ID)),
	}, now)
	if err := s.deliveryRepo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{row}); err != nil {
		log.Printf("[comment-delivery] gate-owner receipt for comment %s failed: %v", comment.ID, err)
	}
}

// workspaceOfTask resolves the task's workspace; uuid.Nil when it cannot be read (fail closed).
func (s *commentService) workspaceOfTask(ctx context.Context, taskID uuid.UUID) uuid.UUID {
	if s.taskRepo == nil || s.projectRepo == nil {
		return uuid.Nil
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return uuid.Nil
	}
	proj, err := s.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || proj == nil {
		return uuid.Nil
	}
	return proj.WorkspaceID
}
