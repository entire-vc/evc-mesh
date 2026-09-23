package service

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Assignee-mention lift (#ed60c795).
//
// A PERSON who writes "@<lane>" on a card that lane already owns, while the
// card is parked in backlog, means "come back to this". Before this, the
// mention was recorded as skipped — the lane polls only todo — and nothing
// else happened: Pavel wrote "Дорабатывай программу @howard" on Howard's own
// parked card (#6d6c101c, 23.09) and it reached nobody, while the recorded
// hint told him to assign a card that was already assigned.
//
// The lift moves the card backlog → todo so the mention lands in the one feed
// the lane actually reads. It is deliberately narrow — every condition below
// is a case where moving the card would be wrong or pointless:
//
//   - author is a person. Agent → agent mentions never lift: agents address
//     each other in-thread all the time ("@garfield принято") and would start
//     waking each other in a loop;
//   - the mentioned agent IS the assignee. Naming someone else is a question
//     of ownership, not of status — the recorded not_assignee verdict and the
//     UI's assign action cover it;
//   - status is backlog. Triage has its own human-response exit
//     (enforceTriageExit); done/cancelled are shipped work that a comment must
//     not reopen (#56a6d5b2); in_progress/review are not "parked";
//   - no armed human_gate and no future start_after — the feeder would still
//     skip the card, so the move would change nothing but the column;
//   - the comment carries no blocking marker (it is a question to a person,
//     not a go-signal) and the handle is not "fyi: @lane"-exempt.
//
// Runs AFTER the comment is persisted (a comment that fails to save must not
// move a card) and BEFORE notifyMentions, so the delivery verdict recorded for
// this very comment reads the post-lift state: delivered/task_queue.

// assigneeMentionLiftTarget reports whether this comment qualifies for the
// lift, and the todo status to lift to. Pure decision + reads, no writes, so
// the mention-handoff gate can ask the same question before persistence.
func (s *commentService) assigneeMentionLiftTarget(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	wsID uuid.UUID,
) (uuid.UUID, bool) {
	if s.taskSvc == nil || s.statusRepo == nil || s.agentSvc == nil || task == nil || comment == nil {
		return uuid.Nil, false
	}
	if comment.AuthorType != domain.ActorTypeUser {
		return uuid.Nil, false
	}
	if task.AssigneeType != domain.AssigneeTypeAgent || task.AssigneeID == nil {
		return uuid.Nil, false
	}
	if task.HumanGate {
		return uuid.Nil, false
	}
	if task.StartAfter != nil && task.StartAfter.After(timeNow()) {
		return uuid.Nil, false
	}
	if hasBlockingMarker(comment.Body) {
		return uuid.Nil, false
	}
	if s.taskStatusCategory(ctx, task) != string(domain.StatusCategoryBacklog) {
		return uuid.Nil, false
	}

	exempt := fyiExemptSlugs(comment.Body)
	named := false
	for _, slug := range extractMentionSlugs(comment.Body) {
		if exempt[slug] {
			continue
		}
		agent, err := s.agentSvc.GetBySlug(ctx, wsID, slug)
		if err == nil && agent != nil && agent.ID == *task.AssigneeID {
			named = true
			break
		}
	}
	if !named {
		return uuid.Nil, false
	}

	todoID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryTodo)
	if err != nil || todoID == uuid.Nil {
		return uuid.Nil, false
	}
	return todoID, true
}

// liftParkedCardOnAssigneeMention performs the lift. On success it updates
// task.StatusID in place so the rest of Create (notably notifyMentions) sees
// the card where it now is. On failure it logs and leaves the card alone: the
// delivery verdict then honestly records status_not_fed, and the UI offers
// the same move as a button.
func (s *commentService) liftParkedCardOnAssigneeMention(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	wsID uuid.UUID,
) bool {
	todoID, ok := s.assigneeMentionLiftTarget(ctx, comment, task, wsID)
	if !ok {
		return false
	}
	if err := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &todoID}); err != nil {
		log.Printf("[assignee-mention-lift] WARNING: move task %s backlog → todo failed: %v", task.ID, err)
		return false
	}
	task.StatusID = todoID

	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body:       "🔄 Auto: задача поднята из backlog → todo — человек упомянул исполнителя на его карточке.",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[assignee-mention-lift] WARNING: system comment on task %s failed: %v", task.ID, err)
	}
	return true
}
