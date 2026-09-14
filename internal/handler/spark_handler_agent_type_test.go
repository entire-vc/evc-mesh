package handler

import "testing"

// resolveAgentType maps a Spark manifest's agent_type to our local domain.AgentType,
// falling back to "custom" for anything it doesn't recognize. Covers every harness
// currently in the whitelist (internal/domain/agent.go's AgentType consts) plus the
// unknown/case-folding fallback behavior.
func TestResolveAgentType(t *testing.T) {
	cases := []struct {
		sparkType string
		want      string
	}{
		{"claude_code", "claude_code"},
		{"openclaw", "openclaw"},
		{"cline", "cline"},
		{"aider", "aider"},
		{"custom", "custom"},
		{"codex", "codex"},
		{"cursor", "cursor"},
		{"copilot", "copilot"},
		{"gemini_cli", "gemini_cli"},
		{"CODEX", "codex"},      // case-folded
		{"unknown-x", "custom"}, // unrecognized falls back to custom
		{"", "custom"},
	}
	for _, tc := range cases {
		t.Run(tc.sparkType, func(t *testing.T) {
			if got := resolveAgentType(tc.sparkType); got != tc.want {
				t.Errorf("resolveAgentType(%q) = %q, want %q", tc.sparkType, got, tc.want)
			}
		})
	}
}
