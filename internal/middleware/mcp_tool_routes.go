package middleware

// mcpToolRoutes maps an "HTTP-METHOD route-pattern" key (route-pattern being
// exactly what echo.Context.Path() returns for a matched request, e.g.
// "/api/v1/tasks/:task_id") to the canonical MCP tool name documented in
// docs/mcp-reference.md. It exists so agent_sessions.tool_breakdown can be
// keyed by the names agents actually call ("recall", "remember",
// "get_task", ...) instead of raw HTTP routes — the audit finding this
// closes (§1.6, task ce1bc187) specifically asked for a tool_breakdown that
// shows those three names, not an internal route inventory.
//
// Coverage is NOT all 63 documented tools. A handful have no route of their
// own on this backend that a request could be resolved back to:
//
//   - get_canonical, subscribe_events, register_sub_agent — these are
//     composed or handled entirely inside the MCP server
//     (entire-vc/evc-mesh-mcp, a separate repo); there is no Mesh REST route
//     that is theirs alone.
//   - pavel_decision and set_project_knowledge both resolve to
//     POST /api/v1/projects/:proj_id/knowledge — the route can't tell them
//     apart, so both are counted under "set_project_knowledge".
//   - publish_summary and report_error are documented convenience wrappers
//     around publish_event (POST /api/v1/projects/:proj_id/events with a
//     different `type` value in the body) — all three are counted under
//     "publish_event".
//
// A call on a route that isn't in this map still gets counted — see
// resolveMCPToolName's "route:" fallback below — so no agent-authenticated
// call is silently dropped from tool_breakdown even where the exact tool
// name can't be recovered from the route alone.
var mcpToolRoutes = map[string]string{
	// Project & Task Management
	"GET /api/v1/workspaces/:ws_id/projects":   "list_projects",
	"GET /api/v1/projects/:proj_id":            "get_project",
	"GET /api/v1/projects/:proj_id/tasks":      "list_tasks",
	"GET /api/v1/tasks/:task_id":               "get_task",
	"GET /api/v1/tasks/by-short-id/:short":     "get_task",
	"POST /api/v1/projects/:proj_id/tasks":     "create_task",
	"PATCH /api/v1/tasks/:task_id":             "update_task",
	"POST /api/v1/tasks/:task_id/move":         "move_task",
	"POST /api/v1/tasks/:task_id/subtasks":     "create_subtask",
	"POST /api/v1/tasks/:task_id/dependencies": "add_dependency",
	"POST /api/v1/tasks/:task_id/assign":       "assign_task",
	"POST /api/v1/tasks/:task_id/vcs-links":    "add_vcs_link",

	// Comments & Artifacts
	"POST /api/v1/tasks/:task_id/comments":  "add_comment",
	"GET /api/v1/tasks/:task_id/comments":   "list_comments",
	"POST /api/v1/tasks/:task_id/artifacts": "upload_artifact",
	"GET /api/v1/tasks/:task_id/artifacts":  "list_artifacts",
	"GET /api/v1/artifacts/:artifact_id":    "get_artifact",

	// Documents
	"GET /api/v1/projects/:proj_id/documents":           "list_docs",
	"GET /api/v1/projects/:proj_id/documents/search":    "search_docs",
	"GET /api/v1/documents/:doc_id":                     "get_doc",
	"GET /api/v1/projects/:proj_id/documents/by-path/*": "get_doc",
	"POST /api/v1/projects/:proj_id/documents":          "create_doc",
	"PATCH /api/v1/documents/:doc_id":                   "update_doc",
	"POST /api/v1/documents/:doc_id/comments":           "comment_doc",
	"GET /api/v1/documents/:doc_id/comments":            "list_doc_comments",

	// Memory & Knowledge
	"POST /api/v1/memories":                    "remember",
	"GET /api/v1/memories/search":              "recall",
	"GET /api/v1/memories/recall_graph":        "recall_with_graph",
	"DELETE /api/v1/memories/:id":              "forget",
	"GET /api/v1/projects/:proj_id/knowledge":  "get_project_knowledge",
	"POST /api/v1/projects/:proj_id/knowledge": "set_project_knowledge", // also pavel_decision — see doc comment above
	"GET /api/v1/canonical_updates":            "get_canonical_updates",

	// Event Bus
	"POST /api/v1/projects/:proj_id/events": "publish_event", // also publish_summary, report_error — see doc comment above
	"GET /api/v1/projects/:proj_id/events":  "get_context",
	"GET /api/v1/tasks/:task_id/context":    "get_task_context",

	// Agent Hierarchy
	"GET /api/v1/agents/:agent_id/sub-agents": "list_sub_agents",

	// Utility
	"POST /api/v1/agents/heartbeat":          "heartbeat",
	"GET /api/v1/agents/me/tasks":            "get_my_tasks",
	"POST /api/v1/agents/me/sessions/report": "session_report",

	// Governance Rules
	"GET /api/v1/workspaces/:ws_id/rules/effective": "get_my_rules",
	"GET /api/v1/projects/:proj_id/rules/effective": "get_my_rules",
	"GET /api/v1/projects/:proj_id/rules":           "get_project_rules",

	// Team & Configuration
	"GET /api/v1/workspaces/:ws_id/team":             "get_team_directory",
	"GET /api/v1/projects/:proj_id/rules/assignment": "get_assignment_rules",
	"GET /api/v1/projects/:proj_id/rules/workflow":   "get_workflow_rules",
	"PUT /api/v1/agents/:agent_id/profile":           "update_agent_profile",
	"POST /api/v1/workspaces/:ws_id/config/import":   "import_workspace_config",
	"GET /api/v1/workspaces/:ws_id/config/export":    "export_workspace_config",

	// Push Notifications
	"GET /api/v1/agents/me/tasks/poll": "poll_tasks",

	// Recurring Tasks
	"POST /api/v1/projects/:proj_id/recurring":     "create_recurring_task",
	"GET /api/v1/projects/:proj_id/recurring":      "list_recurring_schedules",
	"GET /api/v1/recurring/:recurring_id/history":  "get_recurring_history",
	"POST /api/v1/recurring/:recurring_id/trigger": "trigger_recurring_now",
	"PATCH /api/v1/recurring/:recurring_id":        "update_recurring_schedule",
	"DELETE /api/v1/recurring/:recurring_id":       "delete_recurring_schedule",

	// Task Checkout
	"POST /api/v1/tasks/:task_id/checkout":   "checkout_task",
	"DELETE /api/v1/tasks/:task_id/checkout": "release_task",
	"PATCH /api/v1/tasks/:task_id/checkout":  "extend_checkout",

	// Human Gate
	"POST /api/v1/tasks/:task_id/human-gate":   "set_human_gate",
	"DELETE /api/v1/tasks/:task_id/human-gate": "clear_human_gate",
}

// resolveMCPToolName returns the canonical MCP tool name for a request's
// method and matched route pattern, or a stable "route:<METHOD> <pattern>"
// fallback when the route isn't in mcpToolRoutes. The fallback keeps every
// agent-authenticated call countable in tool_breakdown even for the routes
// mcpToolRoutes' doc comment lists as uncovered, and for any route this map
// simply hasn't been kept in sync with (new tool added, route renamed).
func resolveMCPToolName(method, routePath string) string {
	if name, ok := mcpToolRoutes[method+" "+routePath]; ok {
		return name
	}
	return "route:" + method + " " + routePath
}
