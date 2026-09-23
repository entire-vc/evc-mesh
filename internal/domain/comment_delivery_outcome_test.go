package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestApplyHint proves the read-time hint is set for a reason the comment's
// author can act on, and left empty for every other reason — no
// unconditional prose, only the one that names an actual fix.
func TestApplyHint(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		wantHint string
	}{
		{
			name:     "legacy no_queue_path keeps a hint that names both possible fixes",
			reason:   ReasonNoQueuePath,
			wantHint: "recipient is alive but this task isn't in their queue — it is assigned to someone else or not in todo",
		},
		{
			name:     "not_assignee says assign",
			reason:   ReasonNotAssignee,
			wantHint: "recipient is alive but this task is assigned to someone else — assign it to them if they should act on it",
		},
		{
			name:     "task_gated says the gate, not a move",
			reason:   ReasonTaskGated,
			wantHint: "this task is frozen by an armed human gate — they won't pick it up until the gate is answered",
		},
		{
			name:     "task_scheduled says the date",
			reason:   ReasonTaskScheduled,
			wantHint: "this task is scheduled for later (start_after) — they won't pick it up before that date",
		},
		{
			name:     "status_not_fed without a recorded status still points at todo",
			reason:   ReasonStatusNotFed,
			wantHint: "this task is theirs but sits in a status their queue doesn't poll — move it to todo if they should act on it",
		},
		{
			name:     "delivered reason has nothing to fix, no hint",
			reason:   ReasonTaskQueue,
			wantHint: "",
		},
		{
			name:     "self-mention has nothing to fix, no hint",
			reason:   ReasonSelfMention,
			wantHint: "",
		},
		{
			name:     "unknown recipient has nothing to fix, no hint",
			reason:   ReasonRecipientUnknown,
			wantHint: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := CommentDeliveryOutcome{Reason: tc.reason}
			row.ApplyHint()
			assert.Equal(t, tc.wantHint, row.Hint)
		})
	}
}

// TestApplyHints proves the batch helper mutates every row in place —
// the shape every real call site (attachDeliveryOutcomes) actually uses.
func TestApplyHints(t *testing.T) {
	rows := []CommentDeliveryOutcome{
		{RecipientSlug: "bob", Reason: ReasonNoQueuePath},
		{RecipientSlug: "alice", Reason: ReasonTaskQueue},
	}

	ApplyHints(rows)

	assert.NotEmpty(t, rows[0].Hint, "no_queue_path row must carry a hint")
	assert.Empty(t, rows[1].Hint, "delivered row must not carry a hint")
}

// TestApplyHint_StatusNotFedNamesTheStatus pins #ed60c795 AC1: on the author's
// own parked card the hint names where the card sits — never "not assigned".
func TestApplyHint_StatusNotFedNamesTheStatus(t *testing.T) {
	cat := "backlog"
	row := CommentDeliveryOutcome{Reason: ReasonStatusNotFed, TaskStatusCategory: &cat}
	row.ApplyHint()
	assert.Equal(t, "this task is theirs but sits in backlog, which their queue doesn't poll — move it to todo if they should act on it", row.Hint)
	assert.NotContains(t, row.Hint, "assign", "a parked own card must not be told to assign")
}
