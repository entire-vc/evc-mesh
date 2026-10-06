package service

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// autoTransitionGate only restricts automatic moves. Manual API/user transitions
// continue through MoveTask's existing policy. Independent waits cannot be
// discharged by a dependency or child-completion event.
func autoTransitionGate(task *domain.Task, now time.Time) string {
	if reason := humanGateReason(task); reason != "" {
		return reason
	}
	if task.StartAfter != nil && task.StartAfter.After(now) {
		return "future start_after"
	}
	if len(task.CustomFields) != 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(task.CustomFields, &fields); err != nil {
			return "invalid park metadata"
		}
		if raw, present := fields["park_reason"]; present {
			var reason string
			if err := json.Unmarshal(raw, &reason); err != nil || reason != "dependency" {
				return "unknown, independent or conflicting park_reason"
			}
		}
	}
	for _, label := range task.Labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if _, held := backlogPassiveWaitLabels[label]; held {
			return "independent park label: " + label
		}
		if label == "wait-external" || label == "manual-park" {
			return "independent park label: " + label
		}
		if (strings.HasPrefix(label, "park:") || strings.HasPrefix(label, "wake:")) && !dependencyParkLabel(label) {
			return "independent or unknown park label: " + label
		}
	}
	return ""
}

// The canonical reason is custom_fields.park_reason. Only an explicit legacy
// marker may substitute for it; the mere presence of edges is not a park reason.
func dependencyParkIneligibility(task *domain.Task, now time.Time) string {
	if reason := autoTransitionGate(task, now); reason != "" {
		return reason
	}
	var fields map[string]json.RawMessage
	if len(task.CustomFields) != 0 {
		if err := json.Unmarshal(task.CustomFields, &fields); err != nil {
			return "invalid park metadata"
		}
	}
	explicit := false
	if raw, present := fields["park_reason"]; present {
		var reason string
		if err := json.Unmarshal(raw, &reason); err != nil || reason != "dependency" {
			return "unknown, independent or conflicting park_reason"
		}
		explicit = true
	}
	for _, label := range task.Labels {
		if dependencyParkLabel(strings.ToLower(strings.TrimSpace(label))) {
			explicit = true
		}
	}
	if !explicit {
		return "unknown park_reason: explicit dependency marker required"
	}
	return ""
}

func dependencyParkLabel(label string) bool {
	switch label {
	case "park:dependency", "park:wait-dependency", "wake:dependency_edges":
		return true
	default:
		return false
	}
}
