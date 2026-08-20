package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func agentWithHeartbeat(age time.Duration) *domain.Agent {
	hb := time.Now().Add(-age)
	return &domain.Agent{ID: uuid.New(), LastHeartbeat: &hb}
}

// The two scenarios the card names explicitly: a recipient who is asleep and a
// recipient the card does not belong to. They must not collapse into one
// reason, because they call for two different repairs — restart the lane, or
// assign the card. A single merged "not delivered" would tell the sender to do
// neither, which is only marginally better than today's silence.
func TestDecideDelivery_AsleepAndNotInQueueAreDifferentReasons(t *testing.T) {
	asleep := deliveryFacts{
		Slug:        "gandalf",
		Agent:       agentWithHeartbeat(3 * time.Hour),
		InTaskQueue: false,
		Presence:    domain.ComputedStatusOffline,
	}
	notTheirCard := deliveryFacts{
		Slug:        "gandalf",
		Agent:       agentWithHeartbeat(30 * time.Second),
		InTaskQueue: false,
		Presence:    domain.ComputedStatusOnline,
	}

	outAsleep, reasonAsleep, chAsleep, presAsleep := decideDelivery(asleep)
	outCard, reasonCard, chCard, presCard := decideDelivery(notTheirCard)

	assert.Equal(t, domain.DeliverySkipped, outAsleep)
	assert.Equal(t, domain.DeliverySkipped, outCard)

	assert.Equal(t, domain.ReasonRecipientOffline, reasonAsleep)
	assert.Equal(t, domain.ReasonNoQueuePath, reasonCard)

	// The property the card actually asks for, asserted as a property rather
	// than as two literals: whatever the reasons are called, they must differ.
	assert.NotEqual(t, reasonAsleep, reasonCard,
		"a sleeping lane and a card that is not the recipient's must not report the same reason")

	assert.Equal(t, domain.ChannelNone, chAsleep)
	assert.Equal(t, domain.ChannelNone, chCard)
	assert.Equal(t, string(domain.ComputedStatusOffline), presAsleep)
	assert.Equal(t, string(domain.ComputedStatusOnline), presCard)
}

// Neither reason may be vacuous: a named reason no reachable input can produce
// is a label that will never appear, and a report nobody can trigger is
// indistinguishable from a report that is broken. Both are reached above with
// ordinary inputs; this pins that they are reached for the stated cause and
// not by accident of some earlier branch.
func TestDecideDelivery_BothSkipReasonsAreReachable(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range []deliveryFacts{
		{Slug: "a", Agent: agentWithHeartbeat(3 * time.Hour), Presence: domain.ComputedStatusOffline},
		{Slug: "b", Agent: agentWithHeartbeat(time.Minute), Presence: domain.ComputedStatusOnline},
		{Slug: "c", Agent: agentWithHeartbeat(20 * time.Minute), Presence: domain.ComputedStatusIdle},
	} {
		_, reason, _, _ := decideDelivery(f)
		seen[reason] = true
	}
	assert.True(t, seen[domain.ReasonRecipientOffline], "recipient_offline must be reachable")
	assert.True(t, seen[domain.ReasonNoQueuePath], "no_queue_path must be reachable")
}

// The load-bearing honesty case. An agent can be soundly asleep and still be
// reached, because the queue is durable and the card is theirs — fiddler hands
// it over on the next poll. Reporting that as skipped would log a failure that
// never happened, which is the same class of lie as today's silent success,
// only inverted.
func TestDecideDelivery_AsleepButCardIsTheirsIsDelivered(t *testing.T) {
	out, reason, channel, presence := decideDelivery(deliveryFacts{
		Slug:        "linus",
		Agent:       agentWithHeartbeat(8 * time.Hour),
		InTaskQueue: true,
		Presence:    domain.ComputedStatusOffline,
	})
	assert.Equal(t, domain.DeliveryDelivered, out)
	assert.Equal(t, domain.ReasonTaskQueue, reason)
	assert.Equal(t, domain.ChannelTaskQueue, channel)
	// Presence is still recorded honestly — delivered, but to somebody asleep.
	assert.Equal(t, string(domain.ComputedStatusOffline), presence,
		"presence must be reported even when the verdict is delivered")
}

// Regression, found on prod and not findable here: an OPEN EVENT STREAM IS
// NOT DELIVERY.
//
// The first version of this decision treated a live stream connection as a
// reaching path. Measured against production the day it shipped, that made
// nearly every agent mention report `delivered/event_stream` — because every
// lane in this fleet holds the stream open and discards the event body, then
// polls for todo-category tasks and finds nothing. The recipient never saw
// the comment; the record said delivered.
//
// That is exactly the silent success this whole feature exists to abolish,
// so a connected stream must NOT lift the verdict on its own. It is recorded
// as presence, where it is simply a true statement about a socket.
func TestDecideDelivery_OpenStreamIsPresenceNotDelivery(t *testing.T) {
	out, reason, channel, presence := decideDelivery(deliveryFacts{
		Slug:            "bill",
		Agent:           agentWithHeartbeat(time.Second),
		StreamConnected: true,  // socket open …
		InTaskQueue:     false, // … but the card is not in their queue
		Presence:        domain.ComputedStatusOnline,
	})
	assert.Equal(t, domain.DeliverySkipped, out,
		"an open stream must not be reported as reach on its own")
	assert.Equal(t, domain.ReasonNoQueuePath, reason)
	assert.Equal(t, domain.ChannelNone, channel)
	assert.Equal(t, string(domain.ComputedStatusOnline), presence,
		"the connection is still recorded — as presence, which is what it is")
}

// The other half of that regression: a connected stream must not suppress a
// genuine queue delivery either. Fixing the over-claim by ignoring the field
// entirely would be a different wrong answer.
func TestDecideDelivery_OpenStreamDoesNotMaskAQueueDelivery(t *testing.T) {
	out, reason, channel, _ := decideDelivery(deliveryFacts{
		Slug:            "bill",
		Agent:           agentWithHeartbeat(time.Second),
		StreamConnected: true,
		InTaskQueue:     true,
		Presence:        domain.ComputedStatusOnline,
	})
	assert.Equal(t, domain.DeliveryDelivered, out)
	assert.Equal(t, domain.ReasonTaskQueue, reason)
	assert.Equal(t, domain.ChannelTaskQueue, channel)
}

// The case with no trace at all today: a handle that names nobody. The comment
// publishes, the name renders highlighted, and the sender believes the handoff
// happened. This is the row that ends that.
func TestDecideDelivery_UnknownHandleIsRecordedNotDropped(t *testing.T) {
	out, reason, channel, _ := decideDelivery(deliveryFacts{Slug: "daedalus"})
	assert.Equal(t, domain.DeliverySkipped, out)
	assert.Equal(t, domain.ReasonRecipientUnknown, reason)
	assert.Equal(t, domain.ChannelNone, channel)
}

func TestDecideDelivery_SelfMentionIsSkippedNotFailed(t *testing.T) {
	out, reason, _, _ := decideDelivery(deliveryFacts{
		Slug:        "garfield",
		Agent:       agentWithHeartbeat(time.Second),
		SelfMention: true,
		InTaskQueue: true,
		Presence:    domain.ComputedStatusOnline,
	})
	assert.Equal(t, domain.DeliverySkipped, out)
	assert.Equal(t, domain.ReasonSelfMention, reason,
		"naming yourself is not a delivery failure and must not be reported as one")
}

// A person with no notification preference row receives nothing on any
// channel, silently — dispatch has no rule to match and simply produces
// nothing. Reporting that as delivered would advertise a subscription that
// does not exist.
func TestDecideDelivery_UserWithoutSubscriptionIsSkippedWithNamedReason(t *testing.T) {
	out, reason, channel, _ := decideDelivery(deliveryFacts{
		Slug:            "pavel",
		User:            &domain.User{ID: uuid.New()},
		HasSubscription: false,
	})
	assert.Equal(t, domain.DeliverySkipped, out)
	assert.Equal(t, domain.ReasonNoSubscription, reason)
	assert.Equal(t, domain.ChannelNone, channel)
}

func TestDecideDelivery_SubscribedUserIsDelivered(t *testing.T) {
	out, reason, channel, _ := decideDelivery(deliveryFacts{
		Slug:            "pavel",
		User:            &domain.User{ID: uuid.New()},
		HasSubscription: true,
	})
	assert.Equal(t, domain.DeliveryDelivered, out)
	assert.Equal(t, domain.ReasonNotification, reason)
	assert.Equal(t, domain.ChannelNotification, channel)
}

// AC1's hard half, asserted over every branch rather than the ones that came
// to mind: no input may produce an empty reason or an empty channel. The DB
// enforces this too — this test is what tells us which branch broke before
// the constraint has to.
func TestDecideDelivery_NoBranchEverReturnsAnEmptyReason(t *testing.T) {
	live := agentWithHeartbeat(time.Second)
	dead := agentWithHeartbeat(4 * time.Hour)
	user := &domain.User{ID: uuid.New()}

	cases := []deliveryFacts{
		{Slug: "unknown"},
		{Slug: "self", Agent: live, SelfMention: true},
		{Slug: "self-user", User: user, SelfMention: true},
		{Slug: "stream", Agent: live, StreamConnected: true, Presence: domain.ComputedStatusOnline},
		{Slug: "stream-queued", Agent: live, StreamConnected: true, InTaskQueue: true, Presence: domain.ComputedStatusOnline},
		{Slug: "queue", Agent: live, InTaskQueue: true, Presence: domain.ComputedStatusOnline},
		{Slug: "offline", Agent: dead, Presence: domain.ComputedStatusOffline},
		{Slug: "noqueue", Agent: live, Presence: domain.ComputedStatusOnline},
		{Slug: "idle-noqueue", Agent: live, Presence: domain.ComputedStatusIdle},
		{Slug: "user-sub", User: user, HasSubscription: true},
		{Slug: "user-nosub", User: user, HasSubscription: false},
		// Degenerate input: an agent with presence never computed.
		{Slug: "no-presence", Agent: live},
	}

	for _, f := range cases {
		out, reason, channel, presence := decideDelivery(f)
		require.NotEmpty(t, reason, "empty reason for %q", f.Slug)
		require.NotEmpty(t, channel, "empty channel for %q", f.Slug)
		require.NotEmpty(t, presence, "empty presence for %q", f.Slug)
		require.Contains(t,
			[]string{domain.DeliveryDelivered, domain.DeliverySkipped, domain.DeliveryFailed},
			out, "unexpected outcome for %q", f.Slug)
	}
}

// The row assembly must carry the resolved id when there is one and leave it
// nil when there is not — a sentinel id here would put the unresolved case
// back on the resolved path and undo the whole point.
func TestNewOutcomeRow_CarriesRecipientIdentityOrHonestlyNothing(t *testing.T) {
	commentID := uuid.New()
	at := time.Now()

	ag := agentWithHeartbeat(time.Second)
	agentRow := newOutcomeRow(commentID, deliveryFacts{
		Slug: "linus", Agent: ag, InTaskQueue: true, Presence: domain.ComputedStatusOnline,
	}, at)
	require.NotNil(t, agentRow.RecipientID)
	assert.Equal(t, ag.ID, *agentRow.RecipientID)
	assert.Equal(t, domain.RecipientKindAgent, agentRow.RecipientKind)
	assert.Equal(t, at, agentRow.DecidedAt)

	u := &domain.User{ID: uuid.New()}
	userRow := newOutcomeRow(commentID, deliveryFacts{Slug: "pavel", User: u, HasSubscription: true}, at)
	require.NotNil(t, userRow.RecipientID)
	assert.Equal(t, u.ID, *userRow.RecipientID)
	assert.Equal(t, domain.RecipientKindUser, userRow.RecipientKind)

	unknownRow := newOutcomeRow(commentID, deliveryFacts{Slug: "nobody"}, at)
	assert.Nil(t, unknownRow.RecipientID,
		"an unresolved handle must record no recipient id rather than a placeholder")
	assert.Equal(t, domain.RecipientKindUnknown, unknownRow.RecipientKind)
	assert.Equal(t, "nobody", unknownRow.RecipientSlug)
}
