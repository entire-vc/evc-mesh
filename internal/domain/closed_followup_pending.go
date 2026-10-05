package domain

import (
	"time"

	"github.com/google/uuid"
)

// ClosedFollowUpPending is one undelivered closed-card follow-up finding
// (#db1c6c7a): the comment exists, the delivery (root search, claim, card
// create or reopen) could not be completed and did NOT silently fail open —
// the finding is parked here for the reconcile job to re-deliver, with a
// visible system comment on the source card saying so.
//
// CommentID is the PK: a comment carries one finding delivery, so it can
// occupy at most one pending row. Attempts counts reconcile attempts only;
// the inline retry ladder runs before the row is ever written and is not
// part of this counter.
//
// EscalatedAt is the escalation's completion stamp: NULL until the «нужен
// человек» notice has actually LANDED on the source card. It is written only
// after the notice comment exists, because a row frozen at the budget with
// no notice anywhere is the one silent loss this mechanism still had
// (codex-review P1, round 3) — while it is NULL, the row stays in the due
// set so later passes retry the notice.
//
// NoticedAt is the park's visibility stamp: NULL until the «не подтверждена»
// notice has landed. Same rule, one step earlier — a queued finding its own
// commenter cannot see is a queue, not a delivery (codex-review P1, round
// 4) — and every reconcile pass that works the row retries the notice while
// it is NULL. A row past the budget leaves the due set only when BOTH
// stamps are set: retiring on EscalatedAt alone stranded a park notice that
// never landed (codex-review P1, round 5).
//
// ClaimedUntil is the reconcile lease (#a2368528, from the R7 finding on
// !1083): while it is in the future, one pass owns the row and every other
// pass — a second replica's, or an overlapping inline delivery — must skip
// it, because ListDue carries no lock of its own. NULL (or past) means
// unleased. The lease covers the WHOLE row unit — no progress write
// releases it mid-row (codex-review round 2 on !1085: a MarkNoticed release
// reopened the overlap window before delivery) — so the row comes back only
// at its unit's end (the loop's ReleaseClaim, the row's Delete) or by TTL
// expiry, and a row that survives a pass keeps its 5-minute retry cadence.
type ClosedFollowUpPending struct {
	CommentID    uuid.UUID  `db:"comment_id"`
	SourceTaskID uuid.UUID  `db:"source_task_id"`
	FindingKey   string     `db:"finding_key"`
	Attempts     int        `db:"attempts"`
	LastError    string     `db:"last_error"`
	CreatedAt    time.Time  `db:"created_at"`
	EscalatedAt  *time.Time `db:"escalated_at"`
	NoticedAt    *time.Time `db:"noticed_at"`
	ClaimedUntil *time.Time `db:"claimed_until"`
}
