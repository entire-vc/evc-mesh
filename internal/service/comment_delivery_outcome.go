package service

import (
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// deliveryFacts is everything the decision needs, gathered by the caller.
//
// It is a plain struct with no I/O so the verdict can be exercised directly:
// the ordering below is the part that is easy to get subtly wrong and
// impossible to see going wrong in production, because every wrong verdict
// still renders as a confident label next to the comment.
type deliveryFacts struct {
	// Slug as written (lowercased).
	Slug string

	// Resolution. Exactly one of these is set; both nil means the handle
	// resolved to nobody.
	Agent *domain.Agent
	User  *domain.User

	// SelfMention is true when the comment's author is the addressed party.
	SelfMention bool

	// StreamConnected is true when the recipient agent has a live event-stream
	// connection open right now.
	StreamConnected bool

	// InTaskQueue is true when this task is assigned to the recipient agent AND
	// its status is in the todo category — i.e. it is in the feed the agent
	// polls. This is the only path that reaches an agent that is not holding a
	// stream open, which makes it the path that matters most of the time.
	InTaskQueue bool

	// Presence is the recipient agent's computed presence.
	Presence domain.ComputedAgentStatus

	// HasSubscription is true when the mentioned person has at least one
	// notification preference row that would carry this event.
	HasSubscription bool
}

// decideDelivery returns the verdict for one addressed handle.
//
// The ordering is the design, so it is written out rather than left to be
// inferred from the branches:
//
//  1. Self-mention and unresolved handles come first because they are certain
//     and terminal — no path exists and none was ever going to.
//
//  2. Then the two reaching paths, live stream before queue. Either one is a
//     genuine delivery; naming which one is what stops "delivered" from being
//     the unfalsifiable sender-side claim it replaces.
//
//  3. Only then the two ways of not reaching, and their order is the whole
//     point of AC2. An agent whose lane is down and who has no claim on this
//     task gets recipient_offline; an agent that is demonstrably alive and
//     still cannot see this comment gets no_queue_path. Those need different
//     fixes — restart the lane versus assign the card — and a single merged
//     "not delivered" would tell the sender neither.
//
// A deliberate non-obvious consequence: an agent that is fast asleep but whose
// own card sits in their own todo queue is reported delivered, not skipped.
// The queue is durable; they will be handed this comment when they wake. To
// call that skipped would be to log a failure that did not happen — the exact
// inversion of the silent success this record exists to stop.
func decideDelivery(f deliveryFacts) (outcome, reason, channel, presence string) {
	if f.SelfMention {
		return domain.DeliverySkipped, domain.ReasonSelfMention, domain.ChannelNone, presenceOf(f)
	}

	if f.Agent == nil && f.User == nil {
		// Nothing to be online or subscribed: the handle names nobody.
		return domain.DeliverySkipped, domain.ReasonRecipientUnknown, domain.ChannelNone, string(domain.ComputedStatusOffline)
	}

	if f.User != nil {
		// A person is reached through the channels they subscribed to, and the
		// subscription is the precondition nobody else can create for them.
		// Without a preference row the dispatch has no recipient rule to match
		// and produces nothing on any channel — silently, which is why this is
		// worth recording rather than assuming.
		if !f.HasSubscription {
			return domain.DeliverySkipped, domain.ReasonNoSubscription, domain.ChannelNone, "n/a"
		}
		return domain.DeliveryDelivered, domain.ReasonNotification, domain.ChannelNotification, "n/a"
	}

	p := presenceOf(f)

	if f.StreamConnected {
		return domain.DeliveryDelivered, domain.ReasonEventStream, domain.ChannelEventStream, p
	}
	if f.InTaskQueue {
		return domain.DeliveryDelivered, domain.ReasonTaskQueue, domain.ChannelTaskQueue, p
	}
	if f.Presence == domain.ComputedStatusOffline {
		return domain.DeliverySkipped, domain.ReasonRecipientOffline, domain.ChannelNone, p
	}
	return domain.DeliverySkipped, domain.ReasonNoQueuePath, domain.ChannelNone, p
}

func presenceOf(f deliveryFacts) string {
	if f.Agent == nil {
		return "n/a"
	}
	if f.Presence == "" {
		return "unknown"
	}
	return string(f.Presence)
}

// newOutcomeRow assembles the persisted row for one handle.
func newOutcomeRow(
	commentID uuid.UUID,
	f deliveryFacts,
	decidedAt time.Time,
) domain.CommentDeliveryOutcome {
	outcome, reason, channel, presence := decideDelivery(f)

	row := domain.CommentDeliveryOutcome{
		CommentID:         commentID,
		RecipientSlug:     f.Slug,
		RecipientKind:     domain.RecipientKindUnknown,
		Outcome:           outcome,
		Reason:            reason,
		Channel:           channel,
		RecipientPresence: presence,
		DecidedAt:         decidedAt,
	}
	switch {
	case f.Agent != nil:
		id := f.Agent.ID
		row.RecipientID = &id
		row.RecipientKind = domain.RecipientKindAgent
	case f.User != nil:
		id := f.User.ID
		row.RecipientID = &id
		row.RecipientKind = domain.RecipientKindUser
	}
	return row
}
