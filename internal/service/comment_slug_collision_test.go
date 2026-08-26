package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Slug collision: an agent slug and a human username can be the same string
// (task f4f47938 — "hugh" names both the QA-Mesh agent and a real person,
// "ralph" names both the QA-Team-Relay agent and a real person). Nothing in
// the workspace keeps the two namespaces disjoint, so any client that happens
// to register a username matching an existing agent slug (or vice versa)
// reproduces this.
//
// notifyMentions used to try the agent lookup first and `continue` past the
// slug the instant it matched an agent, so the user branch never ran for a
// colliding slug: the person got no notification and, worse, no recorded
// "skipped" row either — the delivery table looked exactly like a mention
// that was never sent. Meanwhile enforceBlockingTriage (a fully separate
// lookup, not exercised by these tests — see comment_triage_test.go) still
// resolved the human and froze the card, so the task looked like a question
// was successfully handed to a person who was never told about it.
// ---------------------------------------------------------------------------

// collisionTestEnv wires every dependency notifyMentions can reach, so a
// collision test can assert on both the agent path (AgentNotifyService,
// task-queue delivery) and the human path (NotificationService, the
// delivery-outcome table) in the same call.
type collisionTestEnv struct {
	svc          *commentService
	taskRepo     *MockTaskRepository
	agentSvc     *MockAgentService
	agentNotify  *MockAgentNotifyService
	userRepo     *MockUserRepository
	notifySvc    *MockNotificationService
	deliveryRepo *fakeDeliveryRepo
	wsID         uuid.UUID
	projID       uuid.UUID
}

func setupCollisionTestEnv() collisionTestEnv {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	agentSvc := NewMockAgentService()
	agentNotify := NewMockAgentNotifyService()
	userRepo := NewMockUserRepository()
	notifySvc := NewMockNotificationService()
	deliveryRepo := &fakeDeliveryRepo{}

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentAgentService(agentSvc),
		WithCommentAgentNotify(agentNotify),
		WithCommentUserRepo(userRepo),
		WithCommentNotificationService(notifySvc),
		WithCommentProjectRepo(projectRepo),
		WithCommentDeliveryOutcomeRepo(deliveryRepo),
	).(*commentService)

	return collisionTestEnv{svc, taskRepo, agentSvc, agentNotify, userRepo, notifySvc, deliveryRepo, wsID, projID}
}

func outcomesBySlugAndKind(rows []domain.CommentDeliveryOutcome, slug, kind string) *domain.CommentDeliveryOutcome {
	for i := range rows {
		if rows[i].RecipientSlug == slug && rows[i].RecipientKind == kind {
			return &rows[i]
		}
	}
	return nil
}

// The task's own acceptance criterion: one comment naming the colliding
// slug produces delivery for BOTH sides, not whichever the agent lookup
// happened to find first.
func TestNotifyMentions_CollidingSlugDeliversToBothAgentAndUser(t *testing.T) {
	env := setupCollisionTestEnv()

	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "hugh"}
	env.agentSvc.AddAgent(env.wsID, agent)
	user := &domain.User{ID: uuid.New(), Username: "hugh", Name: "Hugh"}
	env.userRepo.AddUser(env.wsID, user)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, Title: "T",
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	task := env.taskRepo.items[taskID]

	// A different actor, so neither the agent's nor the user's own self-mention
	// guard suppresses either branch.
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@hugh please take a look"}

	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	// The agent path: unchanged from before this fix, still fires.
	agentCalls := filterByEvent(env.agentNotify.Calls(), "task.mentioned")
	require.Len(t, agentCalls, 1, "the agent side of the collision must still be notified")
	assert.Equal(t, agent.ID, agentCalls[0].AgentID)

	// The human path: this is what the old `continue` skipped entirely.
	userCalls := env.notifySvc.Calls()
	require.Len(t, userCalls, 1, "the human side of the collision must ALSO be notified — this is the bug")
	require.NotNil(t, userCalls[0].TargetUserID)
	assert.Equal(t, user.ID, *userCalls[0].TargetUserID)

	// Both sides leave a trace in the delivery table — the load-bearing
	// acceptance criterion: even if a future policy decision changes WHO wins
	// a collision, a verdict must exist for both, or the failure is invisible
	// again.
	rows := env.deliveryRepo.snapshot()
	require.Len(t, rows, 2, "a colliding slug must produce one outcome row per addressed party, not one winner-takes-all row")

	agentRow := outcomesBySlugAndKind(rows, "hugh", domain.RecipientKindAgent)
	userRow := outcomesBySlugAndKind(rows, "hugh", domain.RecipientKindUser)
	require.NotNil(t, agentRow, "agent side of the collision has no recorded verdict")
	require.NotNil(t, userRow, "human side of the collision has no recorded verdict — the exact defect this test guards against: the person got neither a notification nor a trace that one was skipped")

	assert.Equal(t, agent.ID, *agentRow.RecipientID)
	assert.Equal(t, user.ID, *userRow.RecipientID)
}

// Negative control: a plain non-colliding agent slug must behave exactly as
// it did before this change — one outcome row, one agent notification, and
// critically, the extra user lookup this fix adds must be a harmless no-op
// rather than fabricating a phantom "unresolved" or "skipped" row for a
// person who was never named.
func TestNotifyMentions_NonCollidingAgentSlugUnaffected(t *testing.T) {
	env := setupCollisionTestEnv()

	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "garfield"}
	env.agentSvc.AddAgent(env.wsID, agent)
	// Deliberately no user named "garfield" — no collision.

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, Title: "T",
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@garfield please check"}
	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	assert.Empty(t, env.notifySvc.Calls(), "no user named garfield exists — must not fabricate a notification")

	rows := env.deliveryRepo.snapshot()
	require.Len(t, rows, 1, "a non-colliding agent slug must still produce exactly one row, same as before this fix")
	assert.Equal(t, domain.RecipientKindAgent, rows[0].RecipientKind)
	assert.Equal(t, agent.ID, *rows[0].RecipientID)
}

// Positive control on the probe itself: the delivery table must be able to
// show a kind=user row at all when a slug resolves ONLY to a human (no
// collision) — otherwise "no user row" in the collision test above would be
// indistinguishable from "the probe can't see user rows in the first place".
func TestNotifyMentions_PureUserSlugRecordsAUserRow(t *testing.T) {
	env := setupCollisionTestEnv()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@pavel a question"}
	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	rows := env.deliveryRepo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, domain.RecipientKindUser, rows[0].RecipientKind)
	assert.Equal(t, user.ID, *rows[0].RecipientID)
}
