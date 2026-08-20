package domain

import (
	"time"

	"github.com/google/uuid"
)

// Delivery outcomes for one @-addressed handle on a comment.
//
// The three values answer one question — did this comment reach a path the
// named recipient actually consumes — and they are deliberately coarse. What
// makes the record useful is not the verb but the reason beside it.
const (
	// DeliveryDelivered means the comment is reachable by the recipient through
	// a path they consume: a live event stream, the task queue they poll, or a
	// notification channel they subscribed to. Which one is in Channel.
	DeliveryDelivered = "delivered"

	// DeliverySkipped means no delivery was attempted, or none could reach.
	// Never bare: Reason always names which.
	DeliverySkipped = "skipped"

	// DeliveryFailed means a write delivery depends on returned an error.
	DeliveryFailed = "failed"
)

// Named reasons. Every outcome row carries one, including delivered rows —
// "delivered" without saying by what route is the same unfalsifiable claim as
// a sender-side counter, which is the failure this whole record replaces.
const (
	// ReasonEventStream — the recipient has a live event-stream connection
	// open, so the mention is on a channel they are reading right now.
	ReasonEventStream = "event_stream"

	// ReasonTaskQueue — the task is assigned to the recipient and sits in a
	// todo-category status, so it is in the feed they poll
	// (GET /agents/me/tasks?status_category=todo). This holds whether or not
	// they are awake: work waiting in a queue is delivered work.
	ReasonTaskQueue = "task_queue"

	// ReasonNotification — the mentioned person has a notification preference
	// row for this event, so at least one subscribed channel will carry it.
	ReasonNotification = "notification"

	// ReasonSelfMention — the author named themselves. Nothing to deliver.
	ReasonSelfMention = "self_mention"

	// ReasonRecipientUnknown — the handle resolved to no agent and no user in
	// this workspace. Today this leaves no trace at all: the mention is
	// published, the name is highlighted, and nothing records that it went
	// nowhere. This is the reason that exists to end that.
	ReasonRecipientUnknown = "recipient_unknown"

	// ReasonRecipientOffline — the recipient is an agent with no live stream,
	// no heartbeat inside the offline threshold, and no claim on this task.
	// The lane is down AND the card would not reach it anyway.
	ReasonRecipientOffline = "recipient_offline"

	// ReasonNoQueuePath — the recipient is an agent that is alive, but this
	// task is not in the feed they poll: it is assigned to somebody else, or
	// its status is not in the todo category. Being named in a comment does
	// not put a task in anyone's queue, so a live agent still never sees it.
	// This is the common case and the expensive one, because the sender has
	// every reason to believe the handoff happened.
	ReasonNoQueuePath = "no_queue_path"

	// ReasonNoSubscription — the mentioned person has no notification
	// preference row, so no channel is configured to carry this to them.
	// The subscription is the precondition, and only the person themselves
	// can create it.
	ReasonNoSubscription = "no_subscription"

	// ReasonEventPersistFailed — the durable write to the recipient's event
	// store returned an error. Set asynchronously, after the fact.
	ReasonEventPersistFailed = "event_persist_failed"
)

// Delivery channels — what the verdict is about.
const (
	ChannelEventStream  = "event_stream"
	ChannelTaskQueue    = "task_queue"
	ChannelNotification = "notification"
	ChannelNone         = "none"
)

// Recipient kinds.
const (
	RecipientKindAgent   = "agent"
	RecipientKindUser    = "user"
	RecipientKindUnknown = "unknown"
)

// CommentDeliveryOutcome records what happened to one @-addressed handle on
// one comment.
type CommentDeliveryOutcome struct {
	CommentID     uuid.UUID `json:"comment_id"     db:"comment_id"`
	RecipientSlug string    `json:"recipient_slug" db:"recipient_slug"`
	// Nil when the handle resolved to nobody — a recorded state, not a gap.
	RecipientID       *uuid.UUID `json:"recipient_id,omitempty" db:"recipient_id"`
	RecipientKind     string     `json:"recipient_kind"     db:"recipient_kind"`
	Outcome           string     `json:"outcome"            db:"outcome"`
	Reason            string     `json:"reason"             db:"reason"`
	Channel           string     `json:"channel"            db:"channel"`
	RecipientPresence string     `json:"recipient_presence" db:"recipient_presence"`
	DecidedAt         time.Time  `json:"decided_at"         db:"decided_at"`
}
