package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// commentParticipants is the recipient set for a comment.created event: the
// task's own people, plus the author of the comment being replied to, minus the
// person who just wrote it.
//
// The reply case is the reason this is a method rather than a bare call to
// taskParticipants. "Somebody answered you" is one of the things the product
// explicitly asked a personal channel to carry, and the person answered is
// frequently neither the assignee nor the reviewer nor the creator — a
// colleague who commented once on a task that is not theirs. Only a reply pays
// for the extra lookup; a top-level comment does none.
func (s *commentService) commentParticipants(ctx context.Context, comment *domain.Comment, task *domain.Task) []uuid.UUID {
	var repliedTo *uuid.UUID
	if comment.ParentCommentID != nil && s.commentRepo != nil {
		if parent, err := s.commentRepo.GetByID(ctx, *comment.ParentCommentID); err == nil &&
			parent != nil && parent.AuthorType == domain.ActorTypeUser {
			author := parent.AuthorID
			repliedTo = &author
		}
	}
	return taskParticipants(task, comment.AuthorID, repliedTo)
}

// taskParticipants returns the human users a task-scoped event is about: the
// people whose own work the task represents, rather than everyone who can see
// it.
//
// Membership of this set is deliberately narrow and derived only from fields
// the caller already holds — assignee, reviewer, creator, plus whatever `extra`
// the specific event knows about (the author of a comment being replied to, for
// instance). No repository call is added to any notify path to compute it.
//
// Agents are excluded on purpose: AgentNotifyService is their delivery route,
// and an agent id in a human-notification recipient set would either match
// nothing or, worse, collide with a user id namespace it has no business in.
//
// `exclude` drops the actor. Nobody needs a Telegram message telling them what
// they themselves just did, and before this existed that was a large share of
// everything the channel sent.
func taskParticipants(task *domain.Task, exclude uuid.UUID, extra ...*uuid.UUID) []uuid.UUID {
	if task == nil {
		return nil
	}

	seen := make(map[uuid.UUID]bool, 4)
	out := make([]uuid.UUID, 0, 4)
	add := func(id uuid.UUID) {
		if id == uuid.Nil || id == exclude || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}

	if task.AssigneeID != nil && task.AssigneeType == domain.AssigneeTypeUser {
		add(*task.AssigneeID)
	}
	if task.ReviewerID != nil && task.ReviewerType != nil && *task.ReviewerType == domain.AssigneeTypeUser {
		add(*task.ReviewerID)
	}
	if task.CreatedByType == domain.ActorTypeUser {
		add(task.CreatedBy)
	}
	for _, id := range extra {
		if id != nil {
			add(*id)
		}
	}

	return out
}
