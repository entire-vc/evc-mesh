package service

import (
	"regexp"
	"strings"
)

// moveSourcePattern is the shape a client may declare as the producer of a
// move (POST /tasks/:id/move "source"): a short lowercase token such as
// "mcp", "ui", "mesh-runner" or "dispatcher.session_exit".
var moveSourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,63}$`)

// reservedMoveSources are the sources the server writes for its own moves.
// A client claiming one would forge the audit trail, and "auto_transition"
// also changes the transition itself (no parked-wait refusal, no human-gate
// freeze check), so the API refuses them instead of recording them.
var reservedMoveSources = map[string]bool{
	"auto_transition":    true, // auto_transition.go
	"checkout":           true, // task_service.go checkout auto-progression
	"finish_task":        true, // typed_outcome_commands.go
	"monitor_due_sweep":  true, // monitor_promotion.go
	"human_gate_release": true, // human_gate_triage_return.go
	"transition_receipt": true, // transition_receipt_repo.go
	"system":             true,
}

// reservedMoveSourcePrefixes cover families the server derives at runtime:
// "lease_reaper.<mode>" (task_lease_reaper.go) and the audit spellings
// "auto_transition.parent|dependency" (task_transition_audit.go).
var reservedMoveSourcePrefixes = []string{"auto_transition.", "lease_reaper"}

// IsReservedMoveSource reports whether source is one the server writes itself.
func IsReservedMoveSource(source string) bool {
	if reservedMoveSources[source] {
		return true
	}
	for _, p := range reservedMoveSourcePrefixes {
		if strings.HasPrefix(source, p) {
			return true
		}
	}
	return false
}

// ValidClientMoveSource reports whether a client may declare source on a move.
func ValidClientMoveSource(source string) bool {
	return moveSourcePattern.MatchString(source) && !IsReservedMoveSource(source)
}
