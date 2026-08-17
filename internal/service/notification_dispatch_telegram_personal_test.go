package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Telegram is a message in somebody's pocket, so these tests are about the two
// questions that decide whether it should ever be sent: is this person entitled
// to the contents (membership), and is this event any of their business
// (relevance). The event body used throughout carries comment text, which is
// what makes getting either answer wrong expensive.

// settleDispatch waits out the goroutines dispatch spawns, so that asserting
// "nothing was sent" means nothing was sent rather than nothing had been sent
// yet. Every negative assertion below goes through it.
func settleDispatch() { time.Sleep(50 * time.Millisecond) }

// captureLog, which these tests read the undeliverable-channel lines back
// through, is the email channel's helper in
// notification_dispatch_email_gaps_test.go — the two channels report a channel
// that cannot deliver in the same way, so they assert against it the same way.

// telegramSvc builds a notification service with the Telegram channel wired to
// an active bot, which is the starting point for most of these tests.
func telegramSvc(repo notificationRepository, client TelegramClient) *notificationService {
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	return NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService)
}

// --- membership ---------------------------------------------------------------

// TestDispatch_TelegramNonMemberGetsNothingEvenWhenRelevant is the requirement
// stated as its own test: somebody who is not in the workspace receives no
// Telegram message, and no amount of being named by the event changes that.
//
// It is deliberately stricter than asserting the member's message arrived. The
// only way to prove a message was not sent to a stranger is to let the dispatch
// finish and then look at everything that was sent — a "the member got theirs"
// assertion can pass while a stranger's message is still in flight.
func TestDispatch_TelegramNonMemberGetsNothingEvenWhenRelevant(t *testing.T) {
	wsID := uuid.New()
	member, stranger := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			telegramPref(wsID, member, 111),
			telegramPref(wsID, stranger, 222),
		},
		// The stranger's row is enabled, names the right workspace and the right
		// event, and is bound to a real chat. The one thing it is not is a
		// membership.
		members: map[uuid.UUID]bool{member: true},
	}

	event := commentEvent(wsID)
	// Both are named as relevant, so relevance cannot be what saves us here.
	event.RelevantUserIDs = []uuid.UUID{member, stranger}
	telegramSvc(repo, client).dispatch(event)

	settleDispatch()
	for _, m := range client.messages() {
		assert.NotEqualValues(t, 222, m.chatID,
			"a non-member was sent the contents of this workspace's comment")
	}
	require.Len(t, client.messages(), 1)
	assert.EqualValues(t, 111, client.messages()[0].chatID)
}

// TestDispatch_TelegramUnresolvableMembershipDeliversNothing: the membership
// question failing is not permission to send. Relevance does not override it.
func TestDispatch_TelegramUnresolvableMembershipDeliversNothing(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs:      []domain.NotificationPreference{telegramPref(wsID, member, 111)},
		membersErr: assert.AnError,
	}

	event := commentEvent(wsID)
	event.RelevantUserIDs = []uuid.UUID{member}
	telegramSvc(repo, client).dispatch(event)

	settleDispatch()
	assert.Empty(t, client.messages())
}

// --- relevance ----------------------------------------------------------------

// TestDispatch_TelegramDeliversOnlyToTheEventSOwnPeople is the personal-channel
// rule. Both users are members and both subscribed to comment.created; only the
// one the comment is actually about gets a message in their pocket.
//
// Before this, subscribing to Telegram meant receiving the text of every
// comment on every task in the workspace — a feed, not a personal channel.
func TestDispatch_TelegramDeliversOnlyToTheEventSOwnPeople(t *testing.T) {
	wsID := uuid.New()
	participant, bystander := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			telegramPref(wsID, participant, 111),
			telegramPref(wsID, bystander, 222),
		},
		members: map[uuid.UUID]bool{participant: true, bystander: true},
	}

	event := commentEvent(wsID)
	event.RelevantUserIDs = []uuid.UUID{participant}
	telegramSvc(repo, client).dispatch(event)

	settleDispatch()
	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.EqualValues(t, 111, msgs[0].chatID)
	assert.Contains(t, msgs[0].text, "the confidential contents of somebody else's comment")
}

// TestDispatch_TelegramEventWithoutRelevanceStillBroadcasts: nil is "no
// relevance information", not "nobody". A producer that has not been taught to
// name its recipients must keep delivering to its subscribers rather than
// silently going dark.
func TestDispatch_TelegramEventWithoutRelevanceStillBroadcasts(t *testing.T) {
	wsID := uuid.New()
	a, b := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			telegramPref(wsID, a, 111),
			telegramPref(wsID, b, 222),
		},
		members: map[uuid.UUID]bool{a: true, b: true},
	}

	telegramSvc(repo, client).dispatch(commentEvent(wsID)) // RelevantUserIDs unset

	require.Eventually(t, func() bool { return len(client.messages()) == 2 }, time.Second, 5*time.Millisecond)
}

// TestDispatch_TelegramRelevanceNeverWidensDelivery: the recipient set can only
// remove people the other checks already allowed. Naming a stranger, a
// non-subscriber or somebody who is not the TargetUserID does not let any of
// them through.
func TestDispatch_TelegramRelevanceNeverWidensDelivery(t *testing.T) {
	wsID := uuid.New()
	reviewer, other := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			telegramPref(wsID, reviewer, 111),
			telegramPref(wsID, other, 222),
		},
		members: map[uuid.UUID]bool{reviewer: true, other: true},
	}

	event := commentEvent(wsID)
	event.TargetUserID = &reviewer
	event.RelevantUserIDs = []uuid.UUID{reviewer, other}
	telegramSvc(repo, client).dispatch(event)

	settleDispatch()
	msgs := client.messages()
	require.Len(t, msgs, 1)
	assert.EqualValues(t, 111, msgs[0].chatID)
}

// TestDispatch_RelevanceNarrowsEveryChannelNotJustTelegram is the decision this
// change turns on, written down as a test.
//
// It would have been less invasive to consult RelevantUserIDs only in the
// Telegram loop and leave the in-app bell broadcasting, on the theory that the
// bell is a surface people choose to look at. That was rejected: who an event
// is about is a property of the event, not of the pipe it travels down, and a
// codebase where one channel keeps a wider audience than the others is one
// where the next channel is a coin flip. The bell is asserted here because it
// is the channel the narrowing was most tempting to skip.
func TestDispatch_RelevanceNarrowsEveryChannelNotJustTelegram(t *testing.T) {
	wsID := uuid.New()
	participant, bystander := uuid.New(), uuid.New()

	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			webPushPref(wsID, participant),
			webPushPref(wsID, bystander),
		},
		members: map[uuid.UUID]bool{participant: true, bystander: true},
	}

	event := commentEvent(wsID)
	event.RelevantUserIDs = []uuid.UUID{participant}
	NewNotificationService(repo).(*notificationService).dispatch(event)

	settleDispatch()
	assert.Equal(t, []uuid.UUID{participant}, repo.notifiedUsers(),
		"the in-app bell still stored a comment body for somebody the comment was not about")
}

// TestDispatch_NoRelevanceSetStillBroadcasts is the other half, and the reason
// deliberately workspace-wide events did not have to be touched: an event whose
// producer says nothing about relevance keeps reaching everyone subscribed. A
// producer that has not been taught to fill the set in must not go silent.
func TestDispatch_NoRelevanceSetStillBroadcasts(t *testing.T) {
	wsID := uuid.New()
	one, two := uuid.New(), uuid.New()

	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			webPushPref(wsID, one),
			webPushPref(wsID, two),
		},
		members: map[uuid.UUID]bool{one: true, two: true},
	}

	event := commentEvent(wsID) // no RelevantUserIDs
	NewNotificationService(repo).(*notificationService).dispatch(event)

	settleDispatch()
	assert.ElementsMatch(t, []uuid.UUID{one, two}, repo.notifiedUsers())
}

// --- delivery failures are visible ---------------------------------------------

// TestDispatch_TelegramLogsWhenNoBotIsConfigured: "Telegram notifications don't
// arrive" had three different causes and no way to tell them apart. This is the
// workspace-has-no-bot one.
func TestDispatch_TelegramLogsWhenNoBotIsConfigured(t *testing.T) {
	logged := captureLog(t)
	wsID, member := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(false, "bot-token")} // inactive
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 111)},
		members: map[uuid.UUID]bool{member: true},
	}
	NewNotificationService(repo, WithTelegramService(client, integrations, nil, nil)).(*notificationService).
		dispatch(commentEvent(wsID))

	settleDispatch()
	assert.Empty(t, client.messages())
	out := logged()
	assert.Contains(t, out, "NOT DELIVERED")
	assert.Contains(t, out, "no active Telegram bot configured")
	assert.Contains(t, out, "1 subscriber(s)")
}

// TestDispatch_TelegramLogsWhenChannelIsNotWiredUp: the deps-absent case, which
// on a self-hosted instance is the difference between "misconfigured" and
// "nobody has subscribed yet".
func TestDispatch_TelegramLogsWhenChannelIsNotWiredUp(t *testing.T) {
	logged := captureLog(t)
	wsID, member := uuid.New(), uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 111)},
		members: map[uuid.UUID]bool{member: true},
	}
	NewNotificationService(repo).(*notificationService).dispatch(commentEvent(wsID))

	settleDispatch()
	assert.Contains(t, logged(), "the Telegram channel is not wired up on this instance")
}

// TestDispatch_TelegramSilentWhenNobodyIsWaiting: an unavailable channel with no
// affected subscribers says nothing. The log line exists to name people who
// missed something, not to narrate every event on instances without Telegram.
func TestDispatch_TelegramSilentWhenNobodyIsWaiting(t *testing.T) {
	logged := captureLog(t)
	wsID, member := uuid.New(), uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{webPushPref(wsID, member)},
		members: map[uuid.UUID]bool{member: true},
	}
	NewNotificationService(repo).(*notificationService).dispatch(commentEvent(wsID))

	settleDispatch()
	assert.NotContains(t, logged(), "[notification][telegram]")
}

// TestDispatch_TelegramUnavailableCountIgnoresIrrelevantSubscribers: the number
// in the log is the number of people who actually missed this event, so it has
// to apply the same relevance filter the fan-out would have.
func TestDispatch_TelegramUnavailableCountIgnoresIrrelevantSubscribers(t *testing.T) {
	logged := captureLog(t)
	wsID := uuid.New()
	participant, bystander := uuid.New(), uuid.New()

	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			telegramPref(wsID, participant, 111),
			telegramPref(wsID, bystander, 222),
		},
		members: map[uuid.UUID]bool{participant: true, bystander: true},
	}
	event := commentEvent(wsID)
	event.RelevantUserIDs = []uuid.UUID{participant}
	NewNotificationService(repo).(*notificationService).dispatch(event)

	settleDispatch()
	assert.Contains(t, logged(), "1 subscriber(s)")
}

// TestDispatch_TelegramLogsUnboundSubscribers: enabled, a username on file, and
// /start never completed. This is the most common "it doesn't work" report and
// it used to produce no evidence at all.
func TestDispatch_TelegramLogsUnboundSubscribers(t *testing.T) {
	logged := captureLog(t)
	wsID, member := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 0)},
		members: map[uuid.UUID]bool{member: true},
	}
	telegramSvc(repo, client).dispatch(commentEvent(wsID))

	settleDispatch()
	assert.Empty(t, client.messages())
	out := logged()
	assert.Contains(t, out, "NOT DELIVERED")
	assert.Contains(t, out, "have not completed /start")
}

// TestDispatch_TelegramLogsBlockedBot: a user who blocked the bot is both
// logged and unsubscribed, and the log says which of the two happened.
func TestDispatch_TelegramLogsBlockedBot(t *testing.T) {
	logged := captureLog(t)
	wsID, member := uuid.New(), uuid.New()

	client := &fakeTelegramClient{sendErr: &TelegramAPIError{StatusCode: 403, Description: "bot was blocked by the user"}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 111)},
		members: map[uuid.UUID]bool{member: true},
	}
	telegramSvc(repo, client).dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool {
		return strings.Contains(logged(), "has blocked the bot")
	}, time.Second, 5*time.Millisecond)
	assert.Contains(t, logged(), "NOT DELIVERED")
}

// TestDispatch_TelegramLogsTransientSendFailure: a failure that is not a block
// is reported as a failed delivery and nothing else — the subscription stays.
func TestDispatch_TelegramLogsTransientSendFailure(t *testing.T) {
	logged := captureLog(t)
	wsID, member := uuid.New(), uuid.New()

	client := &fakeTelegramClient{sendErr: &TelegramAPIError{StatusCode: 429, Description: "flood control"}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 111)},
		members: map[uuid.UUID]bool{member: true},
	}
	telegramSvc(repo, client).dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool {
		return strings.Contains(logged(), "send to chat 111 failed")
	}, time.Second, 5*time.Millisecond)
	assert.NotContains(t, logged(), "has blocked the bot")
	assert.Empty(t, repo.upsertedPrefs(), "a transient failure unsubscribed the user")
}

// --- backlog -------------------------------------------------------------------

// TestDispatch_TelegramSendsOnlyTheCurrentEvent is the "do not flood the
// partner's 397 stored notifications into a chat the moment they connect"
// property, asserted where it actually lives: dispatch is driven by the event
// it was handed and never reads the notifications table, so binding a chat
// cannot replay anything. One event in, at most one message out per subscriber.
func TestDispatch_TelegramSendsOnlyTheCurrentEvent(t *testing.T) {
	wsID, member := uuid.New(), uuid.New()

	client := &fakeTelegramClient{}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{telegramPref(wsID, member, 111)},
		members: map[uuid.UUID]bool{member: true},
	}
	// A backlog of unread in-app notifications for this user, of exactly the
	// kind an instance accumulates before anyone turns Telegram on.
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.CreateNotification(nil, &domain.Notification{ //nolint:staticcheck // fake ignores ctx
			WorkspaceID: wsID, UserID: &member, EventType: "comment.created",
		}))
	}

	telegramSvc(repo, client).dispatch(commentEvent(wsID))

	settleDispatch()
	assert.Len(t, client.messages(), 1, "dispatch replayed stored notifications into Telegram")
}
