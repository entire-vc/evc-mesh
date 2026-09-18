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
			name:     "no_queue_path carries the actionable hint",
			reason:   ReasonNoQueuePath,
			wantHint: "recipient is alive but this task isn't assigned to them — assign it if you need them to see this",
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
