package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// #ed60c795 — "@<lane>" from a person on the lane's own parked card.
//
// Live case 23.09: Pavel wrote "Дорабатывай программу @howard" on Howard's own
// card, parked in backlog 20 minutes earlier. The recorded verdict was
// skipped/no_queue_path with the hint "this task isn't assigned to them —
// assign it", on a card that WAS assigned to him. Nothing reached Howard.
//
// These tests drive the real Create path end to end (lift → notifyMentions →
// read-back into the response), with only the task mover faked.

type liftEnv struct {
	svc         *commentService
	mover       *fakeTaskMover
	taskRepo    *MockTaskRepository
	commentRepo *MockCommentRepository
	backlogID   uuid.UUID
	todoID      uuid.UUID
	doneID      uuid.UUID
	projID      uuid.UUID
	howard      *domain.Agent
	other       *domain.Agent
	pavelID     uuid.UUID
}

func setupLiftEnv(t *testing.T) liftEnv {
	t.Helper()
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectRepo := NewMockProjectRepository()
	agentSvc := NewMockAgentService()
	deliveryRepo := NewMockCommentDeliveryOutcomeRepository()
	mover := &fakeTaskMover{}

	wsID, projID := uuid.New(), uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	backlogID, todoID, doneID := uuid.New(), uuid.New(), uuid.New()
	statusRepo.items[backlogID] = &domain.TaskStatus{ID: backlogID, ProjectID: projID, Category: domain.StatusCategoryBacklog, Name: "Backlog"}
	statusRepo.items[todoID] = &domain.TaskStatus{ID: todoID, ProjectID: projID, Category: domain.StatusCategoryTodo, Name: "Todo"}
	statusRepo.items[doneID] = &domain.TaskStatus{ID: doneID, ProjectID: projID, Category: domain.StatusCategoryDone, Name: "Done"}

	// Alive (recent heartbeat), so a miss is never recipient_offline.
	hb := time.Now()
	howard := &domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Slug: "howard", LastHeartbeat: &hb}
	other := &domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Slug: "other", LastHeartbeat: &hb}
	agentSvc.AddAgent(wsID, howard)
	agentSvc.AddAgent(wsID, other)

	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, NewMockActivityLogRepository(),
		WithCommentAgentNotify(NewMockAgentNotifyService()),
		WithCommentAgentService(agentSvc),
		WithCommentStatusRepo(statusRepo),
		WithCommentProjectRepo(projectRepo),
		WithCommentDeliveryOutcomeRepo(deliveryRepo),
		WithCommentTaskService(mover),
	).(*commentService)

	return liftEnv{svc, mover, taskRepo, commentRepo, backlogID, todoID, doneID, projID, howard, other, uuid.New()}
}

// seed puts a card assigned to howard into statusID.
func (e liftEnv) seed(statusID uuid.UUID, mut func(*domain.Task)) *domain.Task {
	id := uuid.New()
	task := &domain.Task{
		ID: id, ProjectID: e.projID, Title: "T", StatusID: statusID,
		AssigneeType: domain.AssigneeTypeAgent, AssigneeID: &e.howard.ID,
	}
	if mut != nil {
		mut(task)
	}
	e.taskRepo.items[id] = task
	return task
}

func (e liftEnv) comment(t *testing.T, taskID uuid.UUID, author domain.ActorType, body string) *domain.Comment {
	t.Helper()
	authorID := e.pavelID
	if author == domain.ActorTypeAgent {
		authorID = e.other.ID
	}
	ctx := actorctx.WithActor(context.Background(), authorID, author)
	c := &domain.Comment{TaskID: taskID, AuthorID: authorID, AuthorType: author, Body: body}
	require.NoError(t, e.svc.Create(ctx, c))
	return c
}

func deliveryFor(c *domain.Comment, slug string) *domain.CommentDeliveryOutcome {
	for i := range c.Delivery {
		if c.Delivery[i].RecipientSlug == slug && c.Delivery[i].RecipientKind == domain.RecipientKindAgent {
			return &c.Delivery[i]
		}
	}
	return nil
}

// AC3 (variant А) + the live case: the card is lifted to todo, and the verdict
// for THIS comment reads delivered — the lane is handed it on the next poll.
func TestAssigneeMentionLift_PersonOnOwnBacklogCard_LiftsAndDelivers(t *testing.T) {
	e := setupLiftEnv(t)
	task := e.seed(e.backlogID, nil)

	c := e.comment(t, task.ID, domain.ActorTypeUser, "Дорабатывай программу @howard")

	moves := e.mover.calls()
	require.Len(t, moves, 1, "the parked card must be lifted")
	require.NotNil(t, moves[0].input.StatusID)
	assert.Equal(t, e.todoID, *moves[0].input.StatusID)

	row := deliveryFor(c, "howard")
	require.NotNil(t, row)
	assert.Equal(t, domain.DeliveryDelivered, row.Outcome)
	assert.Equal(t, domain.ReasonTaskQueue, row.Reason)
	assert.Empty(t, row.Hint)

	var sys []string
	for _, cm := range e.commentRepo.items {
		if cm.AuthorType == domain.ActorTypeSystem {
			sys = append(sys, cm.Body)
		}
	}
	require.Len(t, sys, 1, "the lift must leave an audit trail on the card")
	assert.Contains(t, sys[0], "backlog → todo")
}

// Negative control from the AC: agent → agent on a backlog card never lifts.
// And AC1: the verdict names the card's status instead of "not assigned".
func TestAssigneeMentionLift_AgentAuthor_NoLift_HintNamesBacklog(t *testing.T) {
	e := setupLiftEnv(t)
	task := e.seed(e.backlogID, nil)

	c := e.comment(t, task.ID, domain.ActorTypeAgent, "@howard дорабатывай")

	assert.Empty(t, e.mover.calls(), "agent → agent must not lift a parked card")
	row := deliveryFor(c, "howard")
	require.NotNil(t, row)
	assert.Equal(t, domain.DeliverySkipped, row.Outcome)
	assert.Equal(t, domain.ReasonStatusNotFed, row.Reason)
	require.NotNil(t, row.TaskStatusCategory)
	assert.Equal(t, "backlog", *row.TaskStatusCategory)
	assert.Contains(t, row.Hint, "sits in backlog")
	assert.NotContains(t, row.Hint, "assign", "the card IS theirs — the hint must not send the author to assign it")
}

// Every other case where the lift must not fire, each with the verdict the
// author then sees.
func TestAssigneeMentionLift_DoesNotFire(t *testing.T) {
	future := frozenTime.Add(48 * time.Hour)
	cases := []struct {
		name       string
		status     func(liftEnv) uuid.UUID
		mut        func(*domain.Task)
		body       string
		slug       string
		wantReason string
		wantOut    string
	}{
		{
			name:   "armed human gate — the feeder skips it anyway",
			status: func(e liftEnv) uuid.UUID { return e.backlogID },
			mut:    func(t *domain.Task) { t.HumanGate = true },
			body:   "@howard дорабатывай", slug: "howard",
			wantOut: domain.DeliverySkipped, wantReason: domain.ReasonTaskGated,
		},
		{
			name:   "start_after in the future",
			status: func(e liftEnv) uuid.UUID { return e.backlogID },
			mut:    func(t *domain.Task) { t.StartAfter = &future },
			body:   "@howard дорабатывай", slug: "howard",
			wantOut: domain.DeliverySkipped, wantReason: domain.ReasonTaskScheduled,
		},
		{
			name:   "person names someone who is not the assignee",
			status: func(e liftEnv) uuid.UUID { return e.backlogID },
			body:   "@other глянь", slug: "other",
			wantOut: domain.DeliverySkipped, wantReason: domain.ReasonNotAssignee,
		},
		{
			name:   "fyi-exempt handle is addressing, not a go",
			status: func(e liftEnv) uuid.UUID { return e.backlogID },
			body:   "fyi: @howard запарковано, не трогай", slug: "howard",
			wantOut: domain.DeliverySkipped, wantReason: domain.ReasonStatusNotFed,
		},
		{
			// AC control: the same comment on a todo card is delivered, no plate.
			name:   "control: todo card — already in the queue, nothing to lift",
			status: func(e liftEnv) uuid.UUID { return e.todoID },
			body:   "@howard дорабатывай", slug: "howard",
			wantOut: domain.DeliveryDelivered, wantReason: domain.ReasonTaskQueue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setupLiftEnv(t)
			task := e.seed(tc.status(e), tc.mut)

			c := e.comment(t, task.ID, domain.ActorTypeUser, tc.body)

			assert.Empty(t, e.mover.calls(), "no lift expected")
			row := deliveryFor(c, tc.slug)
			require.NotNil(t, row)
			assert.Equal(t, tc.wantOut, row.Outcome)
			assert.Equal(t, tc.wantReason, row.Reason)
			if tc.wantOut == domain.DeliverySkipped {
				assert.NotEmpty(t, row.Hint, "every skipped verdict an author can fix carries a hint")
			} else {
				assert.Empty(t, row.Hint)
			}
		})
	}
}

// A failed move must not be reported as a lift: the card stays where it was
// and the verdict honestly says so (the UI then offers the same move).
func TestAssigneeMentionLift_MoveFails_VerdictStaysHonest(t *testing.T) {
	e := setupLiftEnv(t)
	e.mover.err = errors.New("transition refused")
	task := e.seed(e.backlogID, nil)

	c := e.comment(t, task.ID, domain.ActorTypeUser, "@howard дорабатывай")

	require.Len(t, e.mover.calls(), 1, "the lift was attempted")
	row := deliveryFor(c, "howard")
	require.NotNil(t, row)
	assert.Equal(t, domain.DeliverySkipped, row.Outcome)
	assert.Equal(t, domain.ReasonStatusNotFed, row.Reason)
}

// The decision itself, without Create: a gated or scheduled todo card is NOT a
// reaching path, because the feeder drops it before the lane sees it.
func TestDecideDelivery_FrozenTodoCardIsNotDelivered(t *testing.T) {
	base := deliveryFacts{
		Slug: "howard", Agent: agentWithHeartbeat(time.Second),
		InTaskQueue: true, IsAssignee: true, StatusCategory: "todo",
		Presence: domain.ComputedStatusOnline,
	}
	gated := base
	gated.Gated = true
	scheduled := base
	scheduled.Scheduled = true

	_, r1, _, _ := decideDelivery(base)
	o2, r2, _, _ := decideDelivery(gated)
	o3, r3, _, _ := decideDelivery(scheduled)

	assert.Equal(t, domain.ReasonTaskQueue, r1)
	assert.Equal(t, domain.DeliverySkipped, o2)
	assert.Equal(t, domain.ReasonTaskGated, r2)
	assert.Equal(t, domain.DeliverySkipped, o3)
	assert.Equal(t, domain.ReasonTaskScheduled, r3)
}

// Done/cancelled/in_progress/review/triage are not "parked": the predicate
// alone, because Create on a closed card also routes a follow-up card, which
// is a separate path with its own tests (comment_closed_task_followup).
func TestAssigneeMentionLift_OnlyBacklogQualifies(t *testing.T) {
	e := setupLiftEnv(t)
	ctx := context.Background()
	for _, st := range []uuid.UUID{e.todoID, e.doneID} {
		task := e.seed(st, nil)
		c := &domain.Comment{TaskID: task.ID, AuthorType: domain.ActorTypeUser, Body: "@howard дорабатывай"}
		_, ok := e.svc.assigneeMentionLiftTarget(ctx, c, task, e.projectWS(t))
		assert.False(t, ok, "only a backlog card is lifted")
	}
	task := e.seed(e.backlogID, nil)
	c := &domain.Comment{TaskID: task.ID, AuthorType: domain.ActorTypeUser, Body: "@howard дорабатывай"}
	to, ok := e.svc.assigneeMentionLiftTarget(ctx, c, task, e.projectWS(t))
	assert.True(t, ok, "positive control: the same predicate fires on backlog")
	assert.Equal(t, e.todoID, to)
}

func (e liftEnv) projectWS(t *testing.T) uuid.UUID {
	t.Helper()
	return e.howard.WorkspaceID
}
