package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func userPtr(id uuid.UUID) *uuid.UUID { return &id }

func assigneeTypePtr(t domain.AssigneeType) *domain.AssigneeType { return &t }

// TestTaskParticipants_CollectsTheTaskSOwnPeople is the happy path: the three
// roles a task carries, in a stable order, with the actor removed.
func TestTaskParticipants_CollectsTheTaskSOwnPeople(t *testing.T) {
	assignee, reviewer, creator, actor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	task := &domain.Task{
		AssigneeID:    userPtr(assignee),
		AssigneeType:  domain.AssigneeTypeUser,
		ReviewerID:    userPtr(reviewer),
		ReviewerType:  assigneeTypePtr(domain.AssigneeTypeUser),
		CreatedBy:     creator,
		CreatedByType: domain.ActorTypeUser,
	}

	assert.Equal(t, []uuid.UUID{assignee, reviewer, creator}, taskParticipants(task, actor))
}

// TestTaskParticipants_ExcludesTheActor: the person who caused the event is not
// told about their own action. Before the recipient set existed, "you moved a
// task" was a large share of what the Telegram channel sent.
func TestTaskParticipants_ExcludesTheActor(t *testing.T) {
	assignee, reviewer := uuid.New(), uuid.New()
	task := &domain.Task{
		AssigneeID:    userPtr(assignee),
		AssigneeType:  domain.AssigneeTypeUser,
		ReviewerID:    userPtr(reviewer),
		ReviewerType:  assigneeTypePtr(domain.AssigneeTypeUser),
		CreatedBy:     assignee,
		CreatedByType: domain.ActorTypeUser,
	}

	got := taskParticipants(task, assignee)
	assert.Equal(t, []uuid.UUID{reviewer}, got, "the actor is still in their own recipient set")
}

// TestTaskParticipants_ExcludesAgents: agent ids are not user ids. An agent
// assignee reaches its own delivery route (AgentNotifyService); putting its id
// in a human recipient set could only ever match the wrong thing.
func TestTaskParticipants_ExcludesAgents(t *testing.T) {
	agent, creator := uuid.New(), uuid.New()
	task := &domain.Task{
		AssigneeID:    userPtr(agent),
		AssigneeType:  domain.AssigneeTypeAgent,
		ReviewerID:    userPtr(agent),
		ReviewerType:  assigneeTypePtr(domain.AssigneeTypeAgent),
		CreatedBy:     creator,
		CreatedByType: domain.ActorTypeAgent,
	}

	assert.Empty(t, taskParticipants(task, uuid.New()))
}

// TestTaskParticipants_DeduplicatesAndSkipsNil: the same person in two roles
// appears once, and absent roles contribute nothing.
func TestTaskParticipants_DeduplicatesAndSkipsNil(t *testing.T) {
	both := uuid.New()
	task := &domain.Task{
		AssigneeID:    userPtr(both),
		AssigneeType:  domain.AssigneeTypeUser,
		ReviewerID:    userPtr(both),
		ReviewerType:  assigneeTypePtr(domain.AssigneeTypeUser),
		CreatedBy:     uuid.Nil,
		CreatedByType: domain.ActorTypeUser,
	}

	assert.Equal(t, []uuid.UUID{both}, taskParticipants(task, uuid.New()))
	assert.Nil(t, taskParticipants(nil, uuid.New()))
}

// TestCommentParticipants_ReplyReachesThePersonAnsweredEvenIfUnrelated is the
// case taskParticipants alone cannot cover: "somebody answered you" is one of
// the things a personal channel was asked to carry, and the person answered is
// frequently not the assignee, the reviewer or the creator.
func TestCommentParticipants_ReplyReachesThePersonAnsweredEvenIfUnrelated(t *testing.T) {
	assignee, bystander, author := uuid.New(), uuid.New(), uuid.New()
	parentID := uuid.New()

	repo := NewMockCommentRepository()
	require.NoError(t, repo.Create(context.Background(), &domain.Comment{
		ID:         parentID,
		AuthorID:   bystander,
		AuthorType: domain.ActorTypeUser,
	}))
	svc := &commentService{commentRepo: repo}

	task := &domain.Task{AssigneeID: userPtr(assignee), AssigneeType: domain.AssigneeTypeUser}
	reply := &domain.Comment{AuthorID: author, ParentCommentID: &parentID}

	got := svc.commentParticipants(context.Background(), reply, task)
	assert.ElementsMatch(t, []uuid.UUID{assignee, bystander}, got)
}

// TestCommentParticipants_TopLevelCommentDoesNoLookup: only a reply pays for the
// parent fetch, and the comment's own author is never a recipient.
func TestCommentParticipants_TopLevelCommentDoesNoLookup(t *testing.T) {
	assignee, author := uuid.New(), uuid.New()
	svc := &commentService{commentRepo: NewMockCommentRepository()}

	task := &domain.Task{
		AssigneeID:    userPtr(assignee),
		AssigneeType:  domain.AssigneeTypeUser,
		CreatedBy:     author,
		CreatedByType: domain.ActorTypeUser,
	}

	got := svc.commentParticipants(context.Background(), &domain.Comment{AuthorID: author}, task)
	assert.Equal(t, []uuid.UUID{assignee}, got, "the comment's own author was notified about their own comment")
}

// TestCommentParticipants_AgentReplyAuthorIsNotARecipient: replying to an
// agent's comment must not put the agent id into a human recipient set.
func TestCommentParticipants_AgentReplyAuthorIsNotARecipient(t *testing.T) {
	assignee, agent, author := uuid.New(), uuid.New(), uuid.New()
	parentID := uuid.New()

	repo := NewMockCommentRepository()
	require.NoError(t, repo.Create(context.Background(), &domain.Comment{
		ID:         parentID,
		AuthorID:   agent,
		AuthorType: domain.ActorTypeAgent,
	}))
	svc := &commentService{commentRepo: repo}

	task := &domain.Task{AssigneeID: userPtr(assignee), AssigneeType: domain.AssigneeTypeUser}
	got := svc.commentParticipants(context.Background(),
		&domain.Comment{AuthorID: author, ParentCommentID: &parentID}, task)

	assert.Equal(t, []uuid.UUID{assignee}, got)
}

// TestRelevantToUser_EmptySetMeansEveryone guards the distinction the field
// depends on: a producer that has not been taught to fill RelevantUserIDs in
// must keep delivering, not go silent.
func TestRelevantToUser_EmptySetMeansEveryone(t *testing.T) {
	anyone := uuid.New()
	assert.True(t, relevantToUser(domain.NotificationEvent{}, anyone))
	assert.True(t, relevantToUser(domain.NotificationEvent{RelevantUserIDs: []uuid.UUID{}}, anyone))

	named := uuid.New()
	event := domain.NotificationEvent{RelevantUserIDs: []uuid.UUID{named}}
	assert.True(t, relevantToUser(event, named))
	assert.False(t, relevantToUser(event, anyone))
}
