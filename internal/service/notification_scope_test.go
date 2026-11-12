package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These tests pin the subscription scope of a preference row (config.scope):
// "all" adds every task event of the workspace the subscriber can read, "mine"
// (the default) keeps the participants-only audience, "mentions" keeps only
// events addressed to the subscriber alone. The typical case they exist for is
// a task created by an agent and assigned to nobody: its comments and status
// changes name no person, so before scopes they reached nobody at all.

// setScope writes scope into config.scope of every row of user, keeping the
// rest of the config (the Telegram chat binding lives there too).
func (s *threeSubscribers) setScope(t *testing.T, user uuid.UUID, scope string) {
	t.Helper()
	for i := range s.repo.prefs {
		p := &s.repo.prefs[i]
		if p.UserID == nil || *p.UserID != user {
			continue
		}
		cfg := map[string]any{}
		if len(p.Config) > 0 {
			require.NoError(t, json.Unmarshal(p.Config, &cfg))
		}
		cfg["scope"] = scope
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		p.Config = raw
	}
}

// agentTaskEvent is what the producers raise for an event on a task that an
// agent created and nobody is assigned to: no participant, no person acting.
func agentTaskEvent(eventType string) domain.NotificationEvent {
	taskID, projID := uuid.New(), uuid.New()
	return domain.NotificationEvent{
		TaskID: &taskID, ProjectID: &projID,
		EventType: eventType, Title: "New comment on: agent card", Body: "progress note",
	}
}

func TestScope_AllReceivesEventOnTaskWithoutParticipants(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	s.setScope(t, s.a, domain.NotificationScopeAll)
	s.setScope(t, s.b, domain.NotificationScopeMine)
	s.setScope(t, s.c, domain.NotificationScopeMentions)

	inApp, mail, chats := s.deliver(agentTaskEvent("comment.created"))

	assert.ElementsMatch(t, []uuid.UUID{s.a}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"a@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"A"}, chats, "telegram")
}

func TestScope_AllReceivesStatusChangeOnTaskWithoutParticipants(t *testing.T) {
	s := newThreeSubscribers("task.status_changed")
	s.setScope(t, s.b, domain.NotificationScopeAll)

	inApp, mail, chats := s.deliver(agentTaskEvent("task.status_changed"))

	assert.ElementsMatch(t, []uuid.UUID{s.b}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"b@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"B"}, chats, "telegram")
}

// A row with no scope at all is "mine": the behaviour of every row written
// before scopes existed does not change.
func TestScope_AbsentScopeIsMine(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	inApp, mail, chats := s.deliver(agentTaskEvent("comment.created"))
	assert.Empty(t, inApp, "in-app")
	assert.Empty(t, mail, "email")
	assert.Empty(t, chats, "telegram")
}

// The actor is spared their own action even though "all" reaches past
// RelevantUserIDs, which is where the actor used to be left out. The event comes
// from the real producer, so ActorUserID is what MoveTask actually fills in.
func TestScope_AllDoesNotEchoTheActorsOwnAction(t *testing.T) {
	s := newThreeSubscribers("task.status_changed")
	s.setScope(t, s.a, domain.NotificationScopeAll)
	s.setScope(t, s.c, domain.NotificationScopeAll)

	// A is the assignee and moves the task; B created it; C is a bystander.
	event := statusChangeEvent(t, s.a, s.b, s.a)
	require.NotNil(t, event.ActorUserID, "the producer did not name the acting person")
	require.Equal(t, s.a, *event.ActorUserID)

	inApp, mail, chats := s.deliver(event)
	assert.ElementsMatch(t, []uuid.UUID{s.b, s.c}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"b@example.com", "c@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"B", "C"}, chats, "telegram")
}

func TestScope_AllDoesNotReachANonMember(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	s.setScope(t, s.a, domain.NotificationScopeAll)
	s.setScope(t, s.c, domain.NotificationScopeAll)
	delete(s.repo.members, s.c)

	inApp, mail, chats := s.deliver(agentTaskEvent("comment.created"))

	assert.ElementsMatch(t, []uuid.UUID{s.a}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"a@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"A"}, chats, "telegram")
}

// A workspace member who may not read the task's project (not a project member,
// not an owner or admin) hears nothing about it through "all".
func TestScope_AllDoesNotReachAMemberWithoutProjectAccess(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	s.setScope(t, s.a, domain.NotificationScopeAll)
	s.setScope(t, s.c, domain.NotificationScopeAll)
	s.repo.projectReaders = map[uuid.UUID]bool{s.a: true}

	inApp, mail, chats := s.deliver(agentTaskEvent("comment.created"))

	assert.ElementsMatch(t, []uuid.UUID{s.a}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"a@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"A"}, chats, "telegram")
}

// "all" is a subscription to the workspace's task events, not to other
// people's personal messages: "you were assigned" addressed to B stays B's.
func TestScope_AllDoesNotReceiveEventsAddressedToSomebodyElse(t *testing.T) {
	s := newThreeSubscribers("task.assigned")
	s.setScope(t, s.a, domain.NotificationScopeAll)
	event := agentTaskEvent("task.assigned")
	event.TargetUserID = &s.b

	inApp, mail, chats := s.deliver(event)

	assert.ElementsMatch(t, []uuid.UUID{s.b}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"b@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"B"}, chats, "telegram")
}

// "all" widens task events only; an event about no task and naming nobody is
// still delivered to nobody.
func TestScope_AllDoesNotWidenEventsWithoutATask(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	s.setScope(t, s.a, domain.NotificationScopeAll)

	inApp, mail, chats := s.deliver(domain.NotificationEvent{EventType: "comment.created", Title: "t"})

	assert.Empty(t, inApp, "in-app")
	assert.Empty(t, mail, "email")
	assert.Empty(t, chats, "telegram")
}

func TestScope_MentionsReceivesAMention(t *testing.T) {
	s := newThreeSubscribers("task.mentioned")
	s.setScope(t, s.c, domain.NotificationScopeMentions)
	event := agentTaskEvent("task.mentioned")
	event.TargetUserID = &s.c

	inApp, mail, chats := s.deliver(event)

	assert.ElementsMatch(t, []uuid.UUID{s.c}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"c@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"C"}, chats, "telegram")
}

// "mentions" drops the participant fan-out: a comment on C's own task does not
// reach C, while A with the default scope still hears about it.
func TestScope_MentionsDoesNotReceiveCommentOnOwnTask(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	s.setScope(t, s.c, domain.NotificationScopeMentions)
	event := agentTaskEvent("comment.created")
	event.RelevantUserIDs = []uuid.UUID{s.a, s.c}

	inApp, mail, chats := s.deliver(event)

	assert.ElementsMatch(t, []uuid.UUID{s.a}, inApp, "in-app")
	assert.ElementsMatch(t, []string{"a@example.com"}, mail, "email")
	assert.ElementsMatch(t, []string{"A"}, chats, "telegram")
}

// Scope is per channel row: "all" on Telegram and the default on email reach
// the same person on Telegram only.
func TestScope_IsPerChannel(t *testing.T) {
	s := newThreeSubscribers("comment.created")
	for i := range s.repo.prefs {
		p := &s.repo.prefs[i]
		if *p.UserID == s.a && p.Channel == "telegram" {
			p.Config = json.RawMessage(`{"telegram_username":"member","telegram_chat_id":1,"scope":"all"}`)
		}
	}

	inApp, mail, chats := s.deliver(agentTaskEvent("comment.created"))

	assert.Empty(t, inApp, "in-app")
	assert.Empty(t, mail, "email")
	assert.ElementsMatch(t, []string{"A"}, chats, "telegram")
}

// Without any matching scope=all row, an event naming nobody is still dropped
// up front and the drop is logged — the 48ac63ae behaviour is unchanged.
func TestScope_NoAllSubscriberStillDropsAndLogs(t *testing.T) {
	readLog := captureLog(t)
	s := newThreeSubscribers("comment.created")
	// An "all" row for another event type does not open comment.created.
	s.setScope(t, s.a, domain.NotificationScopeAll)
	for i := range s.repo.prefs {
		s.repo.prefs[i].Events = []string{"task.status_changed"}
	}

	inApp, mail, chats := s.deliver(agentTaskEvent("comment.created"))

	assert.Empty(t, inApp)
	assert.Empty(t, mail)
	assert.Empty(t, chats)
	waitFor(t, func() bool { return strings.Contains(readLog(), "names no recipients") })
}

func TestPreferenceScope_Reading(t *testing.T) {
	for raw, want := range map[string]string{
		``:                       domain.NotificationScopeMine,
		`{}`:                     domain.NotificationScopeMine,
		`{"scope":"all"}`:        domain.NotificationScopeAll,
		`{"scope":"mentions"}`:   domain.NotificationScopeMentions,
		`{"scope":"mine"}`:       domain.NotificationScopeMine,
		`{"scope":"bogus"}`:      domain.NotificationScopeMine,
		`not json`:               domain.NotificationScopeMine,
		`{"email":"a@b.c"}`:      domain.NotificationScopeMine,
		`{"telegram_chat_id":5}`: domain.NotificationScopeMine,
	} {
		assert.Equal(t, want, domain.PreferenceScope(json.RawMessage(raw)), raw)
	}
}
