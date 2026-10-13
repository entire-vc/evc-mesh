package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// These tests answer one question end to end: when something happens to a
// task, who is told? Each one runs the real producer, takes the event it
// emitted, and pushes it through the real dispatch against three subscribers
// with every channel on — so a recipient set that is right in the producer but
// ignored by a channel (or the other way round) fails here.

// threeSubscribers is a workspace of A, B and C who are members and subscribed
// to eventType on in-app, email and Telegram.
type threeSubscribers struct {
	ws       uuid.UUID
	a, b, c  uuid.UUID
	repo     *fakeNotificationRepo
	mailer   *fakeEmailService
	telegram *fakeTelegramClient
	svc      *notificationService
}

func newThreeSubscribers(eventType string) *threeSubscribers {
	s := &threeSubscribers{
		ws: uuid.New(), a: uuid.New(), b: uuid.New(), c: uuid.New(),
		mailer:   &fakeEmailService{enabled: true},
		telegram: &fakeTelegramClient{},
	}
	chat := map[uuid.UUID]int64{s.a: 1, s.b: 2, s.c: 3}
	var prefs []domain.NotificationPreference
	for _, u := range []uuid.UUID{s.a, s.b, s.c} {
		for _, p := range []domain.NotificationPreference{webPushPref(s.ws, u), emailPref(s.ws, u), telegramPref(s.ws, u, chat[u])} {
			p.Events = pq.StringArray{eventType}
			prefs = append(prefs, p)
		}
	}
	s.repo = &fakeNotificationRepo{prefs: prefs, members: map[uuid.UUID]bool{s.a: true, s.b: true, s.c: true}}
	emails := map[uuid.UUID]string{s.a: "a@example.com", s.b: "b@example.com", s.c: "c@example.com"}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	s.svc = NewNotificationService(s.repo,
		WithEmailService(s.mailer, &fakeUserRepoForEmail{emails: emails}, "https://mesh.example.com"),
		WithTelegramService(s.telegram, integrations, nil, nil),
	).(*notificationService)
	return s
}

// deliver dispatches the event and returns who got it, per channel.
func (s *threeSubscribers) deliver(event domain.NotificationEvent) (inApp []uuid.UUID, mail, chats []string) {
	event.WorkspaceID = s.ws
	s.svc.dispatch(event)
	settleDispatch()
	inApp = s.repo.notifiedUsers()
	mail = s.mailer.recipients()
	for _, m := range s.telegram.messages() {
		chats = append(chats, map[int64]string{1: "A", 2: "B", 3: "C"}[m.chatID])
	}
	return inApp, mail, chats
}

// statusChangeEvent moves a task assigned to A and created by B, as actor, and
// returns the status_changed notification the task service raised.
func statusChangeEvent(t *testing.T, a, b, actor uuid.UUID) domain.NotificationEvent {
	t.Helper()
	env := setupTaskServiceForReviewer()
	projID, taskID, fromID, toID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	env.projects.items[projID] = &domain.Project{ID: projID, WorkspaceID: testDefaultWorkspaceID}
	env.statuses.items[fromID] = &domain.TaskStatus{ID: fromID, ProjectID: projID, Category: domain.StatusCategoryTodo}
	env.statuses.items[toID] = &domain.TaskStatus{ID: toID, ProjectID: projID, Category: domain.StatusCategoryInProgress}
	env.tasks.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, StatusID: fromID, Title: "Ship it",
		AssigneeID: &a, AssigneeType: domain.AssigneeTypeUser,
		CreatedBy: b, CreatedByType: domain.ActorTypeUser,
	}

	ctx := actorctx.WithActor(context.Background(), actor, domain.ActorTypeUser)
	require.NoError(t, env.svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID}))

	var got []domain.NotificationEvent
	require.Eventually(t, func() bool {
		got = nil
		for _, e := range env.userNotify.Calls() {
			if e.EventType == "task.status_changed" {
				got = append(got, e)
			}
		}
		return len(got) > 0
	}, time.Second, 5*time.Millisecond, "the move raised no task.status_changed notification")
	require.Len(t, got, 1)
	return got[0]
}

func TestStatusChanged_ReachesOnlyTheTasksPeopleOnEveryChannel(t *testing.T) {
	// C is a bystander who moves the status: A (assignee) and B (creator) hear
	// about it, C — the actor, and not otherwise involved — does not.
	s := newThreeSubscribers("task.status_changed")
	inApp, mail, chats := s.deliver(statusChangeEvent(t, s.a, s.b, s.c))
	assert.ElementsMatch(t, []uuid.UUID{s.a, s.b}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"a@example.com", "b@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"A", "B"}, chats, "telegram")

	// A moves it: only B is left. A did it themselves, C has nothing to do with it.
	s = newThreeSubscribers("task.status_changed")
	inApp, mail, chats = s.deliver(statusChangeEvent(t, s.a, s.b, s.a))
	assert.ElementsMatch(t, []uuid.UUID{s.b}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"b@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"B"}, chats, "telegram")
}

func TestHumanGateDefaultApplied_ReachesGateAuthorAndTaskPeopleOnly(t *testing.T) {
	s := newThreeSubscribers("task.human_gate_default_applied")
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryInProgress)
	task, _ := h.mover.GetByID(context.Background(), taskID)
	require.NoError(t, h.projectRepo.Create(context.Background(), &domain.Project{ID: task.ProjectID, WorkspaceID: s.ws}))

	// The task belongs to A (assignee); B wrote the gate; C is a bystander.
	task.AssigneeID, task.AssigneeType = &s.a, domain.AssigneeTypeUser
	h.mover.seed(task)
	h.taskRepo.items[taskID].GateAuthor = &s.b
	h.taskRepo.items[taskID].GateAuthorType = actorTypePtr(domain.ActorTypeUser)

	_, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	require.Len(t, h.notify.calls, 1)

	inApp, mail, chats := s.deliver(h.notify.calls[0])
	assert.ElementsMatch(t, []uuid.UUID{s.a, s.b}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"a@example.com", "b@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"A", "B"}, chats, "telegram")
}

// A gate written by an agent has no person to tell about its author, and an
// agent id must never end up in the human recipient set.
func TestHumanGateDefaultApplied_AgentAuthorIsNotARecipient(t *testing.T) {
	h := newDefaultTimeoutHarness(t)
	taskID := h.seedExpiredGate(t, domain.StatusCategoryInProgress)
	task, _ := h.mover.GetByID(context.Background(), taskID)
	require.NoError(t, h.projectRepo.Create(context.Background(), &domain.Project{ID: task.ProjectID, WorkspaceID: uuid.New()}))
	assignee := uuid.New()
	task.AssigneeID, task.AssigneeType = &assignee, domain.AssigneeTypeUser
	h.mover.seed(task)

	_, err := h.svc.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	require.Len(t, h.notify.calls, 1)
	assert.Equal(t, []uuid.UUID{assignee}, h.notify.calls[0].RelevantUserIDs)
}

// An event that names nobody and is not Broadcast is delivered to nobody on any
// channel — the failure mode where a forgotten recipient list silently becomes
// "everyone subscribed" is closed — and the drop is logged.
func TestDispatch_EventWithoutAudienceReachesNobody(t *testing.T) {
	readLog := captureLog(t)
	s := newThreeSubscribers("task.status_changed")
	taskID := uuid.New()

	inApp, mail, chats := s.deliver(domain.NotificationEvent{
		TaskID: &taskID, EventType: "task.status_changed", Title: "Task status changed: Ship it",
	})

	assert.Empty(t, inApp, "in-app")
	assert.Empty(t, mail, "email")
	assert.Empty(t, chats, "telegram")
	waitFor(t, func() bool { return strings.Contains(readLog(), "names no recipients") })
	assert.Contains(t, readLog(), "task.status_changed")
}

func TestDispatch_BroadcastIsTheOnlyWayToReachEveryone(t *testing.T) {
	s := newThreeSubscribers("task.status_changed")
	inApp, mail, chats := s.deliver(domain.NotificationEvent{EventType: "task.status_changed", Title: "t", Broadcast: true})
	assert.Len(t, inApp, 3)
	assert.Len(t, mail, 3)
	assert.Len(t, chats, 3)
}
