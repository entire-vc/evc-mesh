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
	// a path they demonstrably consume: the task queue an agent polls, or a
	// notification channel a person subscribed to. Which one is in Channel.
	//
	// The bar is consumption, not transmission. Anything Mesh merely emitted
	// belongs in presence or in a log, not here.
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

	// ReasonNoQueuePath — LEGACY, no longer written (#ed60c795). It merged
	// three situations with three different fixes into one verdict, and its
	// hint named only one of them ("assign it"), so on a card that WAS the
	// recipient's own — parked in backlog — it sent the author to an action
	// that changes nothing. Kept so rows recorded before the split still read
	// back with a hint that is at least not wrong. New rows carry one of the
	// three reasons below instead.
	ReasonNoQueuePath = "no_queue_path"

	// ReasonNotAssignee — the recipient agent is alive, but the task is
	// assigned to somebody else. Fix: assign it to them.
	ReasonNotAssignee = "not_assignee"

	// ReasonStatusNotFed — the task IS the recipient's, but sits in a status
	// the lane's queue does not poll (backlog, triage, in_progress, review,
	// done…). Which one is in TaskStatusCategory. Fix: move it to todo.
	ReasonStatusNotFed = "status_not_fed"

	// ReasonTaskGated — the task is the recipient's, but an armed human_gate
	// holds it: the feeder skips gated cards whatever their status. Moving or
	// assigning changes nothing until the gate is answered.
	ReasonTaskGated = "task_gated"

	// ReasonTaskScheduled — the task is the recipient's, but its start_after
	// lies in the future: the feeder will not hand it over before then.
	ReasonTaskScheduled = "task_scheduled"

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
// No ChannelEventStream. Holding an event-stream connection open is recorded
// as presence, never as reach: measured on prod, every lane in this fleet
// keeps that socket open and discards the event body, so a stream-based
// "delivered" was true of the socket and false of the recipient.
const (
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

	// TaskStatusCategory is the task's status category at decision time
	// (backlog, todo, …). Recorded because the hint for status_not_fed has to
	// name WHERE the card is parked, and the task may have moved since — the
	// verdict is about the moment the comment was written. Nil on rows
	// recorded before the column existed and when the status was unreadable.
	TaskStatusCategory *string `json:"task_status_category,omitempty" db:"task_status_category"`

	// Hint is computed at read time from Reason, never persisted — see
	// ApplyHint. Empty for a reason with nothing actionable to say (delivered,
	// self-mention, unknown handle): a hint only exists for the outcome the
	// author of the comment can actually do something about.
	Hint string `json:"hint,omitempty" db:"-"`
}

// hintsByReason names, for a Reason where the comment's author holds a fix,
// what that fix is. Deliberately not exhaustive: a reason with no entry here
// leaves Hint empty rather than restating the reason as prose, which would
// just be Reason repeated in English.
//
// Each hint names the ONE action that would change the outcome. A hint that
// points at the wrong action is worse than none: it is followed, nothing
// changes, and the author concludes the system is broken (#ed60c795 — "assign
// it" shown on a card that was already the recipient's own).
var hintsByReason = map[string]string{
	ReasonNoQueuePath:   "recipient is alive but this task isn't in their queue — it is assigned to someone else or not in todo",
	ReasonNotAssignee:   "recipient is alive but this task is assigned to someone else — assign it to them if they should act on it",
	ReasonTaskGated:     "this task is frozen by an armed human gate — they won't pick it up until the gate is answered",
	ReasonTaskScheduled: "this task is scheduled for later (start_after) — they won't pick it up before that date",
}

// ApplyHint sets Hint from o.Reason, in place. Safe to call on a row that
// already carries a hint (idempotent) or on one with no entry (leaves it
// empty rather than erroring).
func (o *CommentDeliveryOutcome) ApplyHint() {
	if o.Reason == ReasonStatusNotFed {
		where := "a status their queue doesn't poll"
		if o.TaskStatusCategory != nil && *o.TaskStatusCategory != "" {
			where = *o.TaskStatusCategory + ", which their queue doesn't poll"
		}
		o.Hint = "this task is theirs but sits in " + where + " — move it to todo if they should act on it"
		return
	}
	o.Hint = hintsByReason[o.Reason]
}

// ApplyHints runs ApplyHint over a batch — the shape every call site actually
// has, whether fresh out of notifyMentions or read back from the repository.
func ApplyHints(rows []CommentDeliveryOutcome) {
	for i := range rows {
		rows[i].ApplyHint()
	}
}
