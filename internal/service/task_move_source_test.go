package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The durable audit keeps a client-declared producer instead of
// folding it into "api"; server names keep their existing spelling.
// The reserved names the service passes to MoveTask itself are kept.
func TestTransitionSource_KeepsDeclaredProducer(t *testing.T) {
	cases := map[string]string{
		"mcp":                  "mcp",
		"mesh-runner":          "mesh-runner",
		"board/fastlane":       "board/fastlane",
		"":                     "api",
		"Not Valid":            "api",
		"checkout":             "checkout",
		"lease_reaper.expired": "lease_reaper.expired",
		"finish_task":          "finish_task",
		"monitor_due_sweep":    "monitor_due_sweep",
		"human_gate_release":   "human_gate_release",
		"system":               "api",
		"transition_receipt":   "api",
	}
	for in, want := range cases {
		assert.Equal(t, want, transitionSource(in, ""), in)
	}
	assert.Equal(t, "auto_transition.dependency", transitionSource("auto_transition", "blocking_dep_resolved"))
	assert.Equal(t, "auto_transition.parent", transitionSource("auto_transition", "all_children_done"))
}

func TestValidClientMoveSource(t *testing.T) {
	for _, ok := range []string{"mcp", "api", "ui", "mesh-runner", "verify_driver", "a"} {
		assert.True(t, ValidClientMoveSource(ok), ok)
	}
	for _, bad := range []string{"", "auto_transition", "auto_transition.parent", "checkout", "lease_reaper.x", "UI", "_x", "x y"} {
		assert.False(t, ValidClientMoveSource(bad), bad)
	}
}
