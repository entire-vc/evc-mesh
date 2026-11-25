package middleware

import "testing"

// The acceptance criterion for task ce1bc187 names three tools explicitly:
// a session's tool_breakdown must show recall/remember/get_task. These three
// cases are the ones that must never silently regress.
func TestResolveMCPToolName_NamedInAcceptanceCriteria(t *testing.T) {
	cases := []struct {
		method, path, want string
	}{
		{"GET", "/api/v1/memories/search", "recall"},
		{"POST", "/api/v1/memories", "remember"},
		{"GET", "/api/v1/tasks/:task_id", "get_task"},
		// Memory-widget U1: every memory read route resolves to its tool.
		{"GET", "/api/v1/memories/recall-final/v1", "recall"},
		{"GET", "/api/v1/memories/recall_graph", "recall_with_graph"},
		{"GET", "/api/v1/memories/by-key", "get_memory"},
		{"GET", "/api/v1/memories/:id", "get_memory"},
		{"GET", "/api/v1/projects/:proj_id/knowledge", "get_project_knowledge"},
		{"GET", "/api/v1/projects/:proj_id/updates", "get_project_updates"},
		{"GET", "/api/v1/projects/:proj_id/updates/latest", "get_project_updates"},
		{"POST", "/api/v1/projects/:proj_id/updates", "post_project_update"},
	}
	for _, tc := range cases {
		got := resolveMCPToolName(tc.method, tc.path)
		if got != tc.want {
			t.Errorf("resolveMCPToolName(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestResolveMCPToolName_UnmappedRouteFallsBackToRouteKey(t *testing.T) {
	got := resolveMCPToolName("GET", "/api/v1/workspaces/:ws_id/violations")
	want := "route:GET /api/v1/workspaces/:ws_id/violations"
	if got != want {
		t.Errorf("resolveMCPToolName fallback = %q, want %q", got, want)
	}
}

// The three-way collisions documented on mcpToolRoutes (set_project_knowledge
// vs pavel_decision, publish_event vs publish_summary/report_error) are a
// known, deliberate limitation, not a bug — this test pins the current,
// documented choice so a future edit that silently changes it gets caught.
func TestResolveMCPToolName_DocumentedAmbiguousRoutesPinnedChoice(t *testing.T) {
	if got := resolveMCPToolName("POST", "/api/v1/projects/:proj_id/knowledge"); got != "set_project_knowledge" {
		t.Errorf("ambiguous knowledge route resolved to %q, want set_project_knowledge", got)
	}
	if got := resolveMCPToolName("POST", "/api/v1/projects/:proj_id/events"); got != "publish_event" {
		t.Errorf("ambiguous events route resolved to %q, want publish_event", got)
	}
}

func TestResolveMCPToolName_EmptyRouteIsFallbackNotEmptyString(t *testing.T) {
	got := resolveMCPToolName("GET", "")
	if got == "" {
		t.Fatal("resolveMCPToolName must never return an empty tool name for a real request")
	}
}
