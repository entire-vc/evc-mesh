package main

import (
	"testing"
	"time"
)

// TestReviewTriageInitialWait pins the fix for task #c5b5fb48: the review-triage
// scheduler's first wait must be computed from wall-clock time since the job's
// persisted last_run_at, not reset to a fresh full interval on every process
// restart. Measured in production before this fix: 70 restarts / 6 days against
// a 24h-ticker job → only 2 real runs.
func TestReviewTriageInitialWait(t *testing.T) {
	const interval = 24 * time.Hour
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		lastRun *time.Time
		want    time.Duration
	}{
		{
			name:    "never run before — run immediately, don't wait a fresh interval",
			lastRun: nil,
			want:    0,
		},
		{
			name:    "restart 3h after a real run 3h ago — wait the remaining 21h, NOT a fresh 24h",
			lastRun: timePtr(now.Add(-3 * time.Hour)),
			want:    21 * time.Hour,
		},
		{
			name:    "restart just 1 minute after the last run — wait almost the full interval",
			lastRun: timePtr(now.Add(-1 * time.Minute)),
			want:    interval - time.Minute,
		},
		{
			name:    "last run exactly one interval ago — overdue, run immediately",
			lastRun: timePtr(now.Add(-interval)),
			want:    0,
		},
		{
			name:    "last run well over an interval ago (many missed restarts) — run immediately, not negative",
			lastRun: timePtr(now.Add(-72 * time.Hour)),
			want:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewTriageInitialWait(tc.lastRun, now, interval)
			if got != tc.want {
				t.Fatalf("reviewTriageInitialWait() = %v, want %v", got, tc.want)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }
