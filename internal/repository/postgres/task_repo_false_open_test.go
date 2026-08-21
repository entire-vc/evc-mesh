package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TestTaskRow_FalseOpenSignal covers taskRow.falseOpenSignal() — the pure Go
// business logic behind domain.FalseOpenSignal (task #c80fe88f). The raw
// stats it operates on (FalseOpenChildrenCount etc.) come from the
// falseOpenComputedCols* SQL fragments; this test exercises the threshold
// decision in isolation, without a database.
func TestTaskRow_FalseOpenSignal(t *testing.T) {
	restore := freezeTimeNow(t, time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC))
	defer restore()

	staleActivity := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)  // 20 days ago, >= threshold
	freshActivity := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) // 6 days ago, < threshold

	tests := []struct {
		name string
		row  taskRow
		want *domain.FalseOpenSignal
	}{
		{
			// The task class this feature exists for: #65dc5949, umbrella open,
			// every subtask closed, no own activity in 20 days.
			name: "all children terminal and stale -> AllChildrenClosed",
			row: taskRow{
				SubtaskCount:                    12,
				FalseOpenOwnCategory:            domain.StatusCategoryInProgress,
				FalseOpenChildrenCount:          0,
				FalseOpenNonparkedChildrenCount: 0,
				FalseOpenLastActivityAt:         staleActivity,
			},
			want: &domain.FalseOpenSignal{
				AllChildrenClosed:      true,
				OnlyParkedChildrenLeft: false,
				OpenChildrenCount:      0,
				StaleDays:              20,
			},
		},
		{
			// #65dc5949's actual shape per the task #c80fe88f review (2026-08-20):
			// 12 done children + one still-open BACKLOG child (c0afa1c5). Must land
			// in OnlyParkedChildrenLeft, never AllChildrenClosed — merging the two
			// would equally catch umbrellas correctly blocked on a live parked dep.
			name: "one open backlog child, rest terminal, stale -> OnlyParkedChildrenLeft only",
			row: taskRow{
				SubtaskCount:                    13,
				FalseOpenOwnCategory:            domain.StatusCategoryTodo,
				FalseOpenChildrenCount:          1,
				FalseOpenNonparkedChildrenCount: 0,
				FalseOpenLastActivityAt:         staleActivity,
			},
			want: &domain.FalseOpenSignal{
				AllChildrenClosed:      false,
				OnlyParkedChildrenLeft: true,
				OpenChildrenCount:      1,
				StaleDays:              20,
			},
		},
		{
			// Negative control: at least one open child is genuinely live (todo/
			// in_progress/review/triage, not backlog) -> neither flag fires, even
			// though the parent itself is stale. This is the "correctly blocked/
			// actively worked" case the signal must not paint as neglected.
			name: "live non-backlog open child -> neither flag",
			row: taskRow{
				SubtaskCount:                    4,
				FalseOpenOwnCategory:            domain.StatusCategoryInProgress,
				FalseOpenChildrenCount:          2,
				FalseOpenNonparkedChildrenCount: 1,
				FalseOpenLastActivityAt:         staleActivity,
			},
			want: &domain.FalseOpenSignal{
				AllChildrenClosed:      false,
				OnlyParkedChildrenLeft: false,
				OpenChildrenCount:      2,
				StaleDays:              20,
			},
		},
		{
			// All children just closed minutes/days ago -- not yet past
			// FalseOpenStaleDays. The parent hasn't "caught up" yet; this must not
			// fire so freshly-finished umbrellas aren't immediately flagged.
			name: "all children terminal but not yet stale -> no flag",
			row: taskRow{
				SubtaskCount:                    3,
				FalseOpenOwnCategory:            domain.StatusCategoryReview,
				FalseOpenChildrenCount:          0,
				FalseOpenNonparkedChildrenCount: 0,
				FalseOpenLastActivityAt:         freshActivity,
			},
			want: &domain.FalseOpenSignal{
				AllChildrenClosed:      false,
				OnlyParkedChildrenLeft: false,
				OpenChildrenCount:      0,
				StaleDays:              6,
			},
		},
		{
			// Leaf task -- no subtasks at all. Not a false-open candidate; the
			// field must be nil (omitted from the JSON response), not a
			// struct with every flag false, so clients can tell "not applicable"
			// from "checked, doesn't apply".
			name: "no subtasks -> nil",
			row: taskRow{
				SubtaskCount:            0,
				FalseOpenOwnCategory:    domain.StatusCategoryInProgress,
				FalseOpenChildrenCount:  0,
				FalseOpenLastActivityAt: staleActivity,
			},
			want: nil,
		},
		{
			// Own status already terminal -- the umbrella itself isn't "open",
			// so the false-open question doesn't apply to it either.
			name: "own status already done -> nil",
			row: taskRow{
				SubtaskCount:            5,
				FalseOpenOwnCategory:    domain.StatusCategoryDone,
				FalseOpenChildrenCount:  0,
				FalseOpenLastActivityAt: staleActivity,
			},
			want: nil,
		},
		{
			name: "own status cancelled -> nil",
			row: taskRow{
				SubtaskCount:            5,
				FalseOpenOwnCategory:    domain.StatusCategoryCancelled,
				FalseOpenChildrenCount:  0,
				FalseOpenLastActivityAt: staleActivity,
			},
			want: nil,
		},
		{
			// Exactly at the threshold boundary: FalseOpenStaleDays (14) days
			// idle must already count as stale (>=, not >).
			name: "exactly at stale threshold -> fires",
			row: taskRow{
				SubtaskCount:            2,
				FalseOpenOwnCategory:    domain.StatusCategoryTodo,
				FalseOpenChildrenCount:  0,
				FalseOpenLastActivityAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), // exactly 14 days before frozen now
			},
			want: &domain.FalseOpenSignal{
				AllChildrenClosed:      true,
				OnlyParkedChildrenLeft: false,
				OpenChildrenCount:      0,
				StaleDays:              14,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.row.falseOpenSignal()
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tc.want, *got)
		})
	}
}

// freezeTimeNow overrides the package-level timeNow for the duration of the
// test and returns a restore func. FalseOpenStaleDays math reads timeNow()
// directly (see taskRow.falseOpenSignal), so this is the only hook needed to
// make the staleness boundary deterministic.
func freezeTimeNow(t *testing.T, now time.Time) func() {
	t.Helper()
	prev := timeNow
	timeNow = func() time.Time { return now }
	return func() { timeNow = prev }
}
