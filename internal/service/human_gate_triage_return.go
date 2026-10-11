package service

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// afterGateClear runs the follow-ups of a real true -> false human_gate transition:
// triage -> todo, then hand a human-held card back to the agent that raised the ask.
// prev is the task as it was while still gated (gate_author is wiped by the release).
func (s *taskService) afterGateClear(ctx context.Context, prev *domain.Task) {
	s.returnTriageToTodoAfterGateClear(ctx, prev.ID)
	s.handBackToGateAuthor(ctx, prev)
}

// handBackToGateAuthor: a card whose assignee is a person (the ask was parked on
// Pavel) must not stay on that person once they have answered — "проверяйте и
// закрывайте" was addressed to an agent, and nobody checked for 7-10 days
// (#346bd658, #d7c420c1; task #8f191cf7). The agent that armed the gate takes it
// back. Left alone: agent-held cards, cards of a gate with no agent author (raw
// PATCH/UI arm), terminal cards, and a card somebody checked out in the meantime (also when that checkout
// lands after the check: AssignTask compares the snapshot's updated_at).
// A pinned (human-assigned) card refuses the system source — logged, not forced.
func (s *taskService) handBackToGateAuthor(ctx context.Context, prev *domain.Task) {
	if prev.GateAuthor == nil || prev.GateAuthorType == nil || *prev.GateAuthorType != domain.ActorTypeAgent {
		return
	}
	fresh, err := s.taskRepo.GetByID(ctx, prev.ID)
	if err != nil || fresh == nil || fresh.HumanGate || hasLiveCheckout(fresh) {
		return
	}
	if fresh.AssigneeID == nil || fresh.AssigneeType != domain.AssigneeTypeUser {
		return
	}
	if st, err := s.statusRepo.GetByID(ctx, fresh.StatusID); err != nil || st == nil ||
		st.Category == domain.StatusCategoryDone || st.Category == domain.StatusCategoryCancelled {
		return
	}
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)
	author := *prev.GateAuthor
	// The snapshot the checks above were made on: a checkout taken after it moves
	// updated_at, so AssignTask refuses instead of overwriting the new holder's card.
	seen := fresh.UpdatedAt
	if err := s.AssignTask(sysCtx, fresh.ID, AssignTaskInput{
		AssigneeID: &author, AssigneeType: domain.AssigneeTypeAgent, Source: domain.AssignmentSourceSystem,
		ExpectedUpdatedAt: &seen,
	}); err != nil {
		var conflict *CASConflictError
		if errors.As(err, &conflict) {
			// Somebody took the card after the snapshot: no hand-back needed, not a failure.
			log.Printf("[human-gate] hand-back of task %s skipped: card changed after the snapshot (cas_conflict)", fresh.ID)
			return
		}
		log.Printf("[human-gate] WARNING: hand-back of task %s to gate author %s failed: %v", fresh.ID, author, err)
		return
	}
	s.postAssigneeChangeComment(ctx, fresh, fresh.AssigneeID, &author, "human_gate снят — карточка вернулась агенту, поднявшему вопрос")
}

// returnTriageToTodoAfterGateClear is the ONE place that decides where a task goes when a
// human_gate comes down. Every release path (button/DELETE, recorded decision, human
// reply, author withdrawal, PATCH/UI) funnels through taskService.SetHumanGate(false),
// ClearHumanGate or Update, so they all produce the same outcome.
//
// Contract: a task sitting in the triage category goes to the project's first todo
// status. Anything else is left alone — an in_progress task (a live checkout owns it),
// todo (already there), done/cancelled (a human closed it) and backlog (a deliberate
// hold). Moving the card to in_progress here would invent an occupied slot nobody
// checked out; checkout -> in_progress stays the only way into that status.
//
// Safety shape: fresh read AFTER the flag write, then a CAS move on the status and
// updated_at that read observed, so a concurrent checkout/move wins and this becomes a
// no-op. A missing todo status or a failed move is logged and leaves the task in triage
// with the gate already down; there is deliberately no fallback to in_progress.
// dependencies and start_after are not touched: MoveTask only changes the status.
//
// Returns true when the task was actually moved.
func (s *taskService) returnTriageToTodoAfterGateClear(ctx context.Context, taskID uuid.UUID) bool {
	if s.statusRepo == nil {
		return false
	}
	fresh, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || fresh == nil {
		return false
	}
	// Re-armed in the meantime, or a live checkout: not ours to move.
	if fresh.HumanGate {
		return false
	}
	if hasLiveCheckout(fresh) {
		return false
	}
	cur, err := s.statusRepo.GetByID(ctx, fresh.StatusID)
	if err != nil || cur == nil || cur.Category != domain.StatusCategoryTriage {
		return false
	}
	todoID, err := findStatusIDByCategory(ctx, s.statusRepo, fresh.ProjectID, domain.StatusCategoryTodo)
	if err != nil || todoID == uuid.Nil {
		log.Printf("[human-gate] WARNING: gate cleared on task %s but project %s has no todo status; task stays in triage", taskID, fresh.ProjectID)
		return false
	}
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)
	updatedAt := fresh.UpdatedAt
	statusID := fresh.StatusID
	if err := s.MoveTask(sysCtx, taskID, MoveTaskInput{
		StatusID:          &todoID,
		ExpectedStatusID:  &statusID,
		ExpectedUpdatedAt: &updatedAt,
		Source:            "human_gate_release",
		Reason:            "human_gate cleared on triage task",
	}); err != nil {
		log.Printf("[human-gate] WARNING: move task %s from triage to todo after gate clear failed: %v", taskID, err)
		return false
	}
	return true
}

// hasLiveCheckout mirrors the repository's notion of a held checkout: someone holds the
// task and the lease has not expired. A nil checkout_expires counts as live (the
// checkout SQL treats IS NULL that way), so a holder without an expiry is protected too.
func hasLiveCheckout(t *domain.Task) bool {
	if t.CheckedOutBy == nil && t.CheckoutToken == nil {
		return false
	}
	return t.CheckoutExpires == nil || t.CheckoutExpires.After(timeNow())
}
