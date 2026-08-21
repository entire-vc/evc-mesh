package main

import (
	"os"
	"testing"
	"time"
)

// TestHumanGateSoftTimeoutWindow covers AC5 (contract docs/human-gate-decision-recorded.md
// §5, task #4dc9467b): the sweep window must be externally configurable, not a recompile,
// while still refusing a value at or below the sweeper's own 15-minute tick.
func TestHumanGateSoftTimeoutWindow(t *testing.T) {
	const envVar = "HUMAN_GATE_SOFT_TIMEOUT"
	t.Cleanup(func() { os.Unsetenv(envVar) })

	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset falls back to service default (0)", "", 0},
		{"valid override honored", "2h", 2 * time.Hour},
		{"valid override at max bound honored", "720h", 720 * time.Hour},
		{"unparseable falls back to default (0)", "not-a-duration", 0},
		{"below sweeper tick refused, falls back to default (0)", "5m", 0},
		{"above max bound refused, falls back to default (0)", "31d", 0}, // also unparseable (no "d" unit)
		{"exactly at min bound honored", "15m", 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.raw == "" {
				os.Unsetenv(envVar)
			} else {
				os.Setenv(envVar, tt.raw)
			}
			if got := humanGateSoftTimeoutWindow(); got != tt.want {
				t.Errorf("humanGateSoftTimeoutWindow() with %s=%q = %s, want %s", envVar, tt.raw, got, tt.want)
			}
		})
	}
}
