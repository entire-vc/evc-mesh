package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// notifyUserMention / notifyMentions (human @-mention branch)
//
// Before this, "task.mentioned" was emitted solely to agents via
// AgentNotifyService — the "When someone @mentions you" toggle the settings
// page offers a human could be switched on and would never produce anything
// on any channel, because nothing ever called NotificationService.Notify for
// a user mention. These tests cover the dispatch added on that path: see the
// doc comment on notifyUserMention in comment_service.go for why
// TargetUserID (not a workspace-wide broadcast) is what keeps a mention
// private to the person named.
// ---------------------------------------------------------------------------

// userMentionTestEnv mirrors mentionTestEnv (used by the agent-mention tests
// above) but additionally wires a NotificationService double, since the
// human-mention path this file tests dispatches through notifySvc rather
// than agentNotifySvc.
type userMentionTestEnv struct {
	svc         *commentService
	commentRepo *MockCommentRepository
	taskRepo    *MockTaskRepository
	userRepo    *MockUserRepository
	agentSvc    *MockAgentService
	agentNotify *MockAgentNotifyService
	notifySvc   *MockNotificationService
	wsID        uuid.UUID
	projID      uuid.UUID
}

func setupCommentServiceWithUserMentions() userMentionTestEnv {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	userRepo := NewMockUserRepository()
	agentSvc := NewMockAgentService()
	agentNotify := NewMockAgentNotifyService()
	notifySvc := NewMockNotificationService()

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentAgentService(agentSvc),
		WithCommentAgentNotify(agentNotify),
		WithCommentUserRepo(userRepo),
		WithCommentProjectRepo(projectRepo),
		WithCommentNotificationService(notifySvc),
	).(*commentService)

	return userMentionTestEnv{svc, commentRepo, taskRepo, userRepo, agentSvc, agentNotify, notifySvc, wsID, projID}
}

// TestNotifyMentions_UserMentionFiresTaskMentionedEvent is the core wiring
// this change adds: an @-mention resolving to a human now reaches
// NotificationService, targeted at that one person via TargetUserID rather
// than broadcast to every task.mentioned subscriber in the workspace (a
// comment naming one member must not disclose its body to the rest of the
// workspace).
func TestNotifyMentions_UserMentionFiresTaskMentionedEvent(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "Ship the thing"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "please review @pavel"}
	// Actor is a different agent, not the mentioned user, so the self-mention
	// guard (covered separately below) does not suppress this.
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1, "one @-mentioned human should produce exactly one task.mentioned event")
	assert.Equal(t, "task.mentioned", calls[0].EventType)
	require.NotNil(t, calls[0].TargetUserID, "the event must be targeted, not fanned out to the whole workspace")
	assert.Equal(t, user.ID, *calls[0].TargetUserID)
}

// TestNotifyMentions_UserSelfMentionProducesNoNotification: the existing
// self-mention guard (actorType/actorID matching the resolved user) already
// stops a comment_mentions row and the WS badge from being created when
// someone @-mentions themselves; this pins that the new NotificationService
// dispatch is downstream of the same guard rather than a second path that
// could bypass it.
func TestNotifyMentions_UserSelfMentionProducesNoNotification(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	userID := uuid.New()
	user := &domain.User{ID: userID, Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@pavel note to self"}
	ctx := actorctx.WithActor(context.Background(), userID, domain.ActorTypeUser)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	assert.Empty(t, env.notifySvc.Calls(), "mentioning yourself must not notify yourself")
}

// TestNotifyMentions_AgentMentionDoesNotFireNotificationServiceEvent: an
// @-mention that resolves to an agent (not a user) must stay on the
// AgentNotifyService path it always used — agents have no
// NotificationService preference rows, so routing them through notifySvc
// as well would either double-notify or silently vanish, depending on
// how dispatch treats an agent-shaped TargetUserID.
func TestNotifyMentions_AgentMentionDoesNotFireNotificationServiceEvent(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "garfield"}
	env.agentSvc.AddAgent(env.wsID, agent)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@garfield please check"}

	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	assert.Empty(t, env.notifySvc.Calls(), "an agent mention must not reach NotificationService")
	agentCalls := filterByEvent(env.agentNotify.Calls(), "task.mentioned")
	require.Len(t, agentCalls, 1, "the agent mention should still be delivered via AgentNotifyService, unaffected by this change")
	assert.Equal(t, agent.ID, agentCalls[0].AgentID)
}

// TestNotifyMentions_NilNotificationServiceDoesNotPanic: notifySvc is an
// optional dependency (WithCommentNotificationService), so a deployment or
// test wiring that omits it must not crash the comment path — notifyUserMention
// guards on s.notifySvc == nil for exactly this reason.
func TestNotifyMentions_NilNotificationServiceDoesNotPanic(t *testing.T) {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	userRepo := NewMockUserRepository()

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}
	userRepo.AddUser(wsID, &domain.User{ID: uuid.New(), Username: "pavel"})

	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentUserRepo(userRepo),
		WithCommentProjectRepo(projectRepo),
		// Deliberately no WithCommentNotificationService — notifySvc stays nil.
	).(*commentService)

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projID}
	task := taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@pavel ping"}

	assert.NotPanics(t, func() {
		svc.notifyMentions(context.Background(), comment, task, "", wsID)
	}, "notifyUserMention must no-op on a nil notifySvc, not dereference a nil interface")
}

// TestNotifyUserMention_BodyTruncatedTo200Chars mirrors the comment.created
// truncation a few lines above notifyUserMention in comment_service.go — the
// notification Body is a preview, not the full comment, and the cap keeps a
// very long comment from blowing up whatever the delivery channel's own
// length limit is (push payloads, Telegram messages, etc).
func TestNotifyUserMention_BodyTruncatedTo200Chars(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	longBody := strings.Repeat("a", 250)
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: longBody}

	env.svc.notifyUserMention(context.Background(), comment, task, env.wsID, uuid.New(), "Garfield")

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	assert.Len(t, calls[0].Body, 200)
	assert.Equal(t, longBody[:200], calls[0].Body)
}

// TestNotifyUserMention_TitleFallsBackWhenActorNameEmpty: actorName is best-effort
// (derived from request context and not always populated — e.g. a system-ish
// caller or a context that never set one via actorctx.WithActorName). The title
// must still read as a complete sentence rather than starting with a blank.
func TestNotifyUserMention_TitleFallsBackWhenActorNameEmpty(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "Ship the thing"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "ping"}

	env.svc.notifyUserMention(context.Background(), comment, task, env.wsID, uuid.New(), "")

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "You were mentioned on: Ship the thing", calls[0].Title)
}

// TestNotifyUserMention_TitleIncludesActorNameWhenPresent is the complement of
// the fallback case above: when the actor's name IS known, the title should
// say who did the mentioning rather than the generic passive form.
func TestNotifyUserMention_TitleIncludesActorNameWhenPresent(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "Ship the thing"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "ping"}

	env.svc.notifyUserMention(context.Background(), comment, task, env.wsID, uuid.New(), "Garfield")

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "Garfield mentioned you on: Ship the thing", calls[0].Title)
}

// ---------------------------------------------------------------------------
// HasSubscription must consider the event a comment ACTUALLY carries (#4e1d249f)
//
// Found by an independent verifier 2026-09-06: the per-comment delivery
// outcome — the field agents actually check to tell whether a mention landed
// — hardcoded task.mentioned regardless of what the comment asked for. Once a
// person's subscription is narrowed to task.blocking_triage only (Pavel's
// "только блокеры" decision), every future "❓ Blocking @pavel" recorded
// outcome=skipped/no_subscription even though enforceBlockingTriage's own
// Notify() call delivered it correctly — a false negative in exactly the
// field this whole notification-delivery task exists to make trustworthy.
// ---------------------------------------------------------------------------

func TestUserHasSubscriptionForAnyEvent_MatchesAnyListedEvent(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	userID := uuid.New()
	env.notifySvc.SeedPreference(domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: env.wsID,
		UserID:      &userID,
		Channel:     "email",
		Events:      []string{"task.blocking_triage"},
		IsEnabled:   true,
	})

	assert.False(t,
		env.svc.userHasSubscriptionForAnyEvent(context.Background(), userID, []string{"task.mentioned"}),
		"a row scoped to task.blocking_triage must not report a task.mentioned-only subscription",
	)
	assert.True(t,
		env.svc.userHasSubscriptionForAnyEvent(context.Background(), userID, []string{"task.mentioned", "task.blocking_triage"}),
		"the same row must count once task.blocking_triage is itself one of the wanted events",
	)
}

// deliveryRepoUserMentionEnv wires a fakeDeliveryRepo (from
// comment_delivery_recording_test.go) alongside the user-mention env, so
// notifyMentions's recorded outcome can be inspected end to end rather than
// only at the userHasSubscriptionForAnyEvent unit level above.
type deliveryRepoUserMentionEnv struct {
	userMentionTestEnv
	deliveryRepo *fakeDeliveryRepo
}

func setupCommentServiceWithUserMentionsAndDeliveryRepo() deliveryRepoUserMentionEnv {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	userRepo := NewMockUserRepository()
	agentSvc := NewMockAgentService()
	agentNotify := NewMockAgentNotifyService()
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
		WithCommentProjectRepo(projectRepo),
		WithCommentNotificationService(notifySvc),
		WithCommentDeliveryOutcomeRepo(deliveryRepo),
	).(*commentService)

	return deliveryRepoUserMentionEnv{
		userMentionTestEnv{svc, commentRepo, taskRepo, userRepo, agentSvc, agentNotify, notifySvc, wsID, projID},
		deliveryRepo,
	}
}

// TestNotifyMentions_BlockingMarker_RecordsDeliveredViaBlockingTriageSubscription
// pins the end-to-end fix: a genuine "❓ Blocking @pavel" comment against a
// user subscribed ONLY to task.blocking_triage must record outcome=delivered,
// not skipped/no_subscription.
func TestNotifyMentions_BlockingMarker_RecordsDeliveredViaBlockingTriageSubscription(t *testing.T) {
	env := setupCommentServiceWithUserMentionsAndDeliveryRepo()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)
	env.notifySvc.SeedPreference(domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: env.wsID,
		UserID:      &user.ID,
		Channel:     "email",
		Events:      []string{"task.blocking_triage"},
		IsEnabled:   true,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "❓ **Blocking @pavel**: need a decision"}
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	require.Len(t, env.deliveryRepo.rows, 1)
	row := env.deliveryRepo.rows[0]
	assert.Equal(t, domain.DeliveryDelivered, row.Outcome,
		"a blocking-marker comment must be recorded as delivered when the person is subscribed to task.blocking_triage, even without a task.mentioned row")
	assert.Equal(t, domain.ReasonNotification, row.Reason)
}

// TestNotifyMentions_PlainMention_StaysSkippedOnBlockingTriageOnlySubscription
// is the negative control: a PLAIN @-mention (no blocking marker) against the
// same task.blocking_triage-only subscription must still record
// skipped/no_subscription — being subscribed to blockers only must not make
// every ordinary mention look delivered too.
func TestNotifyMentions_PlainMention_StaysSkippedOnBlockingTriageOnlySubscription(t *testing.T) {
	env := setupCommentServiceWithUserMentionsAndDeliveryRepo()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)
	env.notifySvc.SeedPreference(domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: env.wsID,
		UserID:      &user.ID,
		Channel:     "email",
		Events:      []string{"task.blocking_triage"},
		IsEnabled:   true,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "please look, @pavel"}
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	require.Len(t, env.deliveryRepo.rows, 1)
	row := env.deliveryRepo.rows[0]
	assert.Equal(t, domain.DeliverySkipped, row.Outcome,
		"a plain mention must not ride along on a blocking_triage-only subscription")
	assert.Equal(t, domain.ReasonNoSubscription, row.Reason)
}
