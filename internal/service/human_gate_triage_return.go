package service

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

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
