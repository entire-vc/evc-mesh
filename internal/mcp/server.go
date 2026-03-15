// Package mcp contains MCP (Model Context Protocol) tool handlers.
// These tools provide an agent-friendly interface for task management,
// event bus interaction, and artifact handling.
//
// Architecture: tools call the REST API via RESTClient instead of
// accessing the database directly. This decouples the MCP server from
// the data layer and allows the MCP server to run as a lightweight proxy.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Profile constants for MCP server tool sets.
const (
	// ProfileCore registers only the 20 essential tools for lightweight agents.
	ProfileCore = "core"
	// ProfileFull registers all tools (core + advanced). This is the default.
	ProfileFull = "full"
)

// AgentSession holds the authenticated agent context for the MCP session.
type AgentSession struct {
	AgentID     uuid.UUID
	WorkspaceID uuid.UUID
	AgentName   string
	AgentType   string
}

// agentSessionKey is the context key for storing AgentSession in context.
type agentSessionKey struct{}

// restClientKey is the context key for storing a per-request RESTClient in context (SSE mode).
type restClientKey struct{}

// ContextWithSession returns a new context with the given AgentSession attached.
func ContextWithSession(ctx context.Context, session *AgentSession) context.Context {
	return context.WithValue(ctx, agentSessionKey{}, session)
}

// SessionFromContext retrieves the AgentSession from the context, or nil if not present.
func SessionFromContext(ctx context.Context) *AgentSession {
	if session, ok := ctx.Value(agentSessionKey{}).(*AgentSession); ok {
		return session
	}
	return nil
}

// ContextWithRESTClient returns a new context with the given RESTClient attached.
// Used in SSE mode to inject per-connection REST clients.
func ContextWithRESTClient(ctx context.Context, client *RESTClient) context.Context {
	return context.WithValue(ctx, restClientKey{}, client)
}

// RESTClientFromContext retrieves a per-request RESTClient from context.
// Returns nil if none was injected (stdio mode uses the server's shared client).
func RESTClientFromContext(ctx context.Context) *RESTClient {
	if client, ok := ctx.Value(restClientKey{}).(*RESTClient); ok {
		return client
	}
	return nil
}

// NewAgentSession creates an AgentSession by parsing UUID strings from the REST API response.
// Returns an error if any UUID is malformed.
func NewAgentSession(agentID, workspaceID, agentName, agentType string) (AgentSession, error) {
	aID, err := uuid.Parse(agentID)
	if err != nil {
		return AgentSession{}, fmt.Errorf("invalid agent_id %q: %w", agentID, err)
	}
	wsID, err := uuid.Parse(workspaceID)
	if err != nil {
		return AgentSession{}, fmt.Errorf("invalid workspace_id %q: %w", workspaceID, err)
	}
	return AgentSession{
		AgentID:     aID,
		WorkspaceID: wsID,
		AgentName:   agentName,
		AgentType:   agentType,
	}, nil
}

// Server wraps an mcp-go MCPServer with a REST API client.
type Server struct {
	mcpServer  *mcpserver.MCPServer
	session    *AgentSession // static session for stdio mode; nil for SSE mode
	restClient *RESTClient   // default REST client; may be overridden per-request in SSE mode
	tracker    *SessionTracker
	profile    string
}

// getSession returns the AgentSession for the current request.
// It first checks the context (for SSE per-connection sessions),
// then falls back to the static session (for stdio mode).
func (s *Server) getSession(ctx context.Context) *AgentSession {
	if session := SessionFromContext(ctx); session != nil {
		return session
	}
	return s.session
}

// getRESTClient returns the REST client for the current request.
// In SSE mode, a per-connection client may be stored in context.
// Falls back to the shared client for stdio mode.
func (s *Server) getRESTClient(ctx context.Context) *RESTClient {
	if client := RESTClientFromContext(ctx); client != nil {
		return client
	}
	return s.restClient
}

// ServerConfig holds all configuration needed to build the MCP server.
type ServerConfig struct {
	// Session is the static agent session for stdio mode. Nil for SSE mode.
	Session *AgentSession
	// RESTClient is the HTTP client used to call the Mesh REST API.
	RESTClient *RESTClient
	// Profile controls which tools are registered: "core" (20 essential tools)
	// or "full" (all tools, default).
	Profile string
}

// NewServer creates a new MCP server with tools registered according to the profile.
func NewServer(cfg ServerConfig) *Server {
	profile := cfg.Profile
	if profile == "" {
		profile = ProfileFull
	}

	serverName := "evc-mesh-mcp"
	if profile == ProfileCore {
		serverName = "evc-mesh-mcp-core"
	}

	s := &Server{
		mcpServer:  mcpserver.NewMCPServer(serverName, "0.1.0"),
		session:    cfg.Session,
		restClient: cfg.RESTClient,
		tracker:    NewSessionTracker(),
		profile:    profile,
	}

	s.registerCoreTools()
	if profile == ProfileFull {
		s.registerAdvancedTools()
	}
	return s
}

// tracked wraps a tool handler to record the tool call in the session tracker.
func (s *Server) tracked(name string, handler func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error)) func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		if s.tracker != nil {
			s.tracker.RecordToolCall(name)
		}
		return handler(ctx, req)
	}
}

// contextWarning returns a non-empty warning string when the agent has not yet
// loaded project context (via get_project_knowledge, recall, or get_context)
// in the current session. Returns an empty string once context has been loaded.
func (s *Server) contextWarning() string {
	if s.tracker == nil {
		return ""
	}
	_, detail := s.tracker.ComplianceScore()
	if !detail["called_get_project_knowledge"] && !detail["called_get_context"] {
		return "Warning: You haven't loaded project context. Call get_project_knowledge() or get_context() first to avoid conflicting with existing decisions."
	}
	return ""
}

// MCPServer returns the underlying mcp-go server for use with transports.
func (s *Server) MCPServer() *mcpserver.MCPServer {
	return s.mcpServer
}

// registerCoreTools registers the 20 essential tools with optimized, directive descriptions.
func (s *Server) registerCoreTools() {
	// --- ACP / Session tools ---
	s.mcpServer.AddTool(mcpsdk.NewTool("heartbeat",
		mcpsdk.WithDescription("Send heartbeat to stay visible. Call at session START with status=online, periodically during work with status=busy. Reports current_task_id, message, and metadata."),
		mcpsdk.WithString("current_task_id", mcpsdk.Description("ID of the task currently being worked on.")),
		mcpsdk.WithString("status", mcpsdk.Description("Agent status: online, busy, error.")),
		mcpsdk.WithString("message", mcpsdk.Description("Short human-readable status message (e.g. 'running tests', 'waiting for review').")),
		mcpsdk.WithObject("metadata", mcpsdk.Description("Arbitrary JSON metadata to store with the heartbeat.")),
	), s.tracked("heartbeat", s.handleHeartbeat))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_project_knowledge",
		mcpsdk.WithDescription("Get ALL PERMANENT KNOWLEDGE for a project: decisions, conventions, accumulated context. Call at session start (ACP Step 2). Returns workspace-level + project-level memories. For RECENT events, use get_context instead."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project UUID.")),
	), s.tracked("get_project_knowledge", s.handleGetProjectKnowledge))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_my_rules",
		mcpsdk.WithDescription("Get ALL governance rules that apply to you: workflow constraints, assignment policies, behavioral requirements. Includes workspace and project-level rules with source annotations. Call at session start (ACP Step 3)."),
		mcpsdk.WithString("project_id", mcpsdk.Description("Optional project ID to get project-specific effective rules.")),
	), s.tracked("get_my_rules", s.handleGetMyRules))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_context",
		mcpsdk.WithDescription("Get RECENT ACTIVITY for a project (last 24h by default): event stream with summaries, decisions, errors, plus accumulated project knowledge. Use for ACP Step 4 — what happened recently. For searching specific knowledge, use recall."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithString("since", mcpsdk.Description("Only events after this timestamp (RFC3339).")),
		mcpsdk.WithArray("event_types", mcpsdk.Description("Filter by event types."), mcpsdk.WithStringItems()),
		mcpsdk.WithArray("tags", mcpsdk.Description("Filter by tags."), mcpsdk.WithStringItems()),
		mcpsdk.WithNumber("limit", mcpsdk.Description("Max events to return (default 50).")),
	), s.tracked("get_context", s.handleGetContext))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_my_tasks",
		mcpsdk.WithDescription("Get YOUR assigned tasks (ACP Step 5). Filter by status_category to focus on active work. Use at session start and after completing tasks to pick up the next assignment."),
		mcpsdk.WithString("status_category", mcpsdk.Description("Filter by status category: backlog, todo, in_progress, review, done, cancelled.")),
		mcpsdk.WithString("project_id", mcpsdk.Description("Filter by project.")),
		mcpsdk.WithNumber("limit", mcpsdk.Description("Max results (default 50).")),
	), s.tracked("get_my_tasks", s.handleGetMyTasks))

	// --- Task CRUD ---
	s.mcpServer.AddTool(mcpsdk.NewTool("list_tasks",
		mcpsdk.WithDescription("List tasks with filters."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithString("status_category", mcpsdk.Description("Filter by status category: backlog, todo, in_progress, review, done, cancelled.")),
		mcpsdk.WithString("assignee_type", mcpsdk.Description("Filter by assignee type: user, agent, unassigned.")),
		mcpsdk.WithString("priority", mcpsdk.Description("Filter by priority: urgent, high, medium, low, none.")),
		mcpsdk.WithArray("labels", mcpsdk.Description("Filter by labels."), mcpsdk.WithStringItems()),
		mcpsdk.WithString("search", mcpsdk.Description("Search in title and description.")),
		mcpsdk.WithNumber("limit", mcpsdk.Description("Max results to return (default 50, max 200).")),
		mcpsdk.WithString("sort", mcpsdk.Description("Sort field: created_at, updated_at, priority, due_date.")),
	), s.tracked("list_tasks", s.handleListTasks))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_task",
		mcpsdk.WithDescription("Get full task details with optional comments, artifacts, and dependencies."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithBoolean("include_comments", mcpsdk.Description("Include comments."), mcpsdk.DefaultBool(false)),
		mcpsdk.WithBoolean("include_artifacts", mcpsdk.Description("Include artifacts."), mcpsdk.DefaultBool(false)),
		mcpsdk.WithBoolean("include_dependencies", mcpsdk.Description("Include dependencies."), mcpsdk.DefaultBool(false)),
	), s.tracked("get_task", s.handleGetTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("create_task",
		mcpsdk.WithDescription("Create a new task. Check get_my_tasks and list_tasks FIRST to avoid duplicates. Set status_slug for initial status (defaults to project's first status)."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithString("title", mcpsdk.Required(), mcpsdk.Description("Task title.")),
		mcpsdk.WithString("description", mcpsdk.Description("Task description.")),
		mcpsdk.WithString("status_slug", mcpsdk.Description("Status slug (e.g. 'todo'). Uses project default if omitted.")),
		mcpsdk.WithString("priority", mcpsdk.Description("Priority: urgent, high, medium, low, none."), mcpsdk.DefaultString("medium")),
		mcpsdk.WithString("assignee_id", mcpsdk.Description("Assignee ID (user or agent UUID).")),
		mcpsdk.WithString("assignee_type", mcpsdk.Description("Assignee type: user, agent."), mcpsdk.DefaultString("unassigned")),
		mcpsdk.WithArray("labels", mcpsdk.Description("Task labels."), mcpsdk.WithStringItems()),
		mcpsdk.WithObject("custom_fields", mcpsdk.Description("Custom field values as key-value pairs.")),
		mcpsdk.WithString("parent_task_id", mcpsdk.Description("Parent task ID for subtask.")),
		mcpsdk.WithString("due_date", mcpsdk.Description("Due date in RFC3339 format.")),
		mcpsdk.WithNumber("estimated_hours", mcpsdk.Description("Estimated hours for the task.")),
	), s.tracked("create_task", s.handleCreateTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("update_task",
		mcpsdk.WithDescription("Update task fields."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithString("title", mcpsdk.Description("New title.")),
		mcpsdk.WithString("description", mcpsdk.Description("New description.")),
		mcpsdk.WithString("priority", mcpsdk.Description("New priority.")),
		mcpsdk.WithArray("labels", mcpsdk.Description("New labels."), mcpsdk.WithStringItems()),
		mcpsdk.WithObject("custom_fields", mcpsdk.Description("Custom field values to update.")),
		mcpsdk.WithString("due_date", mcpsdk.Description("Due date in RFC3339 format.")),
		mcpsdk.WithNumber("estimated_hours", mcpsdk.Description("Estimated hours.")),
	), s.tracked("update_task", s.handleUpdateTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("move_task",
		mcpsdk.WithDescription("Change task status (e.g. todo → in_progress → done). Use status SLUGS (not UUIDs). Add a comment to explain why."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithString("status_slug", mcpsdk.Required(), mcpsdk.Description("Target status slug (e.g. 'in_progress', 'done').")),
		mcpsdk.WithString("comment", mcpsdk.Description("Optional comment to add when moving.")),
	), s.tracked("move_task", s.handleMoveTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("assign_task",
		mcpsdk.WithDescription("Assign a task to a user or agent."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithString("assignee_id", mcpsdk.Description("Assignee UUID. Omit to unassign.")),
		mcpsdk.WithString("assignee_type", mcpsdk.Description("Assignee type: user, agent."), mcpsdk.DefaultString("agent")),
		mcpsdk.WithBoolean("assign_to_self", mcpsdk.Description("Assign to the calling agent."), mcpsdk.DefaultBool(false)),
	), s.tracked("assign_task", s.handleAssignTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_task_context",
		mcpsdk.WithDescription("Get EVERYTHING about ONE TASK in a single call: full details + comments + artifacts + dependencies + activity. Use when working on a specific task instead of calling get_task + list_comments + list_artifacts separately."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
	), s.tracked("get_task_context", s.handleGetTaskContext))

	// --- Communication ---
	s.mcpServer.AddTool(mcpsdk.NewTool("add_comment",
		mcpsdk.WithDescription("Add a comment to a task."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithString("body", mcpsdk.Required(), mcpsdk.Description("Comment body (markdown supported).")),
		mcpsdk.WithBoolean("is_internal", mcpsdk.Description("Mark as internal (agent-only visible)."), mcpsdk.DefaultBool(false)),
		mcpsdk.WithString("parent_comment_id", mcpsdk.Description("Parent comment ID for threading.")),
		mcpsdk.WithObject("metadata", mcpsdk.Description("Additional metadata as key-value pairs.")),
	), s.tracked("add_comment", s.handleAddComment))

	s.mcpServer.AddTool(mcpsdk.NewTool("publish_event",
		mcpsdk.WithDescription("Publish an event to the event bus. For summaries, use event_type='summary'. Add memory={persist:true, key:'decision-name'} to also save as permanent memory. Replaces the deprecated publish_summary tool."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithString("event_type", mcpsdk.Required(), mcpsdk.Description("Event type: summary, status_change, context_update, error, dependency_resolved, custom.")),
		mcpsdk.WithString("subject", mcpsdk.Required(), mcpsdk.Description("Event subject line.")),
		mcpsdk.WithObject("payload", mcpsdk.Required(), mcpsdk.Description("Event payload as key-value pairs.")),
		mcpsdk.WithString("task_id", mcpsdk.Description("Related task ID.")),
		mcpsdk.WithArray("tags", mcpsdk.Description("Event tags for filtering."), mcpsdk.WithStringItems()),
		mcpsdk.WithNumber("ttl_hours", mcpsdk.Description("Time-to-live in hours (default 24).")),
		mcpsdk.WithObject("memory", mcpsdk.Description("Optional memory hint to persist alongside the event (e.g. key decisions, conventions).")),
	), s.tracked("publish_event", s.handlePublishEvent))

	// --- Memory ---
	s.mcpServer.AddTool(mcpsdk.NewTool("recall",
		mcpsdk.WithDescription("SEARCH memory by keywords. Use to find a SPECIFIC piece of knowledge, e.g. 'API convention' or 'license decision'. Returns ranked results with scores. For loading ALL project knowledge at session start, use get_project_knowledge instead."),
		mcpsdk.WithString("query", mcpsdk.Required(), mcpsdk.Description("Full-text search query.")),
		mcpsdk.WithString("project_id", mcpsdk.Description("Filter to a specific project.")),
		mcpsdk.WithString("scope", mcpsdk.Description("Filter by scope: workspace, project, agent, or all (default).")),
		mcpsdk.WithArray("tags", mcpsdk.Description("Filter by tags."), mcpsdk.WithStringItems()),
		mcpsdk.WithNumber("limit", mcpsdk.Description("Max results (default 10, max 50).")),
	), s.tracked("recall", s.handleRecall))

	s.mcpServer.AddTool(mcpsdk.NewTool("remember",
		mcpsdk.WithDescription("Save knowledge to persistent memory. Use for decisions, conventions, preferences. UPSERT by key — calling with same key updates the existing entry."),
		mcpsdk.WithString("key", mcpsdk.Required(), mcpsdk.Description("Slug key for UPSERT (e.g. 'api-convention', 'license-decision').")),
		mcpsdk.WithString("content", mcpsdk.Required(), mcpsdk.Description("What to remember (markdown).")),
		mcpsdk.WithString("scope", mcpsdk.Description("workspace | project | agent (default: project).")),
		mcpsdk.WithString("project_id", mcpsdk.Description("Project ID (required for project scope).")),
		mcpsdk.WithArray("tags", mcpsdk.Description("Tags for categorization and filtering."), mcpsdk.WithStringItems()),
	), s.tracked("remember", s.handleRemember))

	s.mcpServer.AddTool(mcpsdk.NewTool("forget",
		mcpsdk.WithDescription("Delete a memory entry. Agents can only delete their own agent-scope memories."),
		mcpsdk.WithString("memory_id", mcpsdk.Required(), mcpsdk.Description("UUID of the memory to delete.")),
	), s.tracked("forget", s.handleForget))

	// --- Utility ---
	s.mcpServer.AddTool(mcpsdk.NewTool("list_projects",
		mcpsdk.WithDescription("List available projects in the workspace."),
		mcpsdk.WithString("workspace_id", mcpsdk.Description("Workspace ID. Defaults to agent's workspace.")),
		mcpsdk.WithBoolean("include_archived", mcpsdk.Description("Include archived projects."), mcpsdk.DefaultBool(false)),
	), s.tracked("list_projects", s.handleListProjects))

	s.mcpServer.AddTool(mcpsdk.NewTool("report_error",
		mcpsdk.WithDescription("Report an error encountered during work."),
		mcpsdk.WithString("task_id", mcpsdk.Description("Related task ID.")),
		mcpsdk.WithString("error_message", mcpsdk.Required(), mcpsdk.Description("Error message.")),
		mcpsdk.WithString("stack_trace", mcpsdk.Description("Stack trace or details.")),
		mcpsdk.WithString("severity", mcpsdk.Description("Severity: low, medium, high, critical."), mcpsdk.DefaultString("medium")),
		mcpsdk.WithBoolean("recoverable", mcpsdk.Description("Whether the error is recoverable."), mcpsdk.DefaultBool(true)),
	), s.tracked("report_error", s.handleReportError))

	s.mcpServer.AddTool(mcpsdk.NewTool("session_report",
		mcpsdk.WithDescription("Report session metrics. Call before session end. Returns compliance score and session stats."),
		mcpsdk.WithString("model", mcpsdk.Description("LLM model used (e.g. 'claude-sonnet-4').")),
		mcpsdk.WithNumber("tokens_in", mcpsdk.Description("Total input tokens this session.")),
		mcpsdk.WithNumber("tokens_out", mcpsdk.Description("Total output tokens this session.")),
		mcpsdk.WithNumber("estimated_cost", mcpsdk.Description("Estimated cost in USD.")),
	), s.tracked("session_report", s.handleSessionReport))
}

// registerAdvancedTools registers tools beyond the core set.
// These are available in the full profile only.
func (s *Server) registerAdvancedTools() {
	// --- Projects ---
	s.mcpServer.AddTool(mcpsdk.NewTool("get_project",
		mcpsdk.WithDescription("Get project details with statuses and custom fields."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
	), s.tracked("get_project", s.handleGetProject))

	// --- Task operations ---
	s.mcpServer.AddTool(mcpsdk.NewTool("create_subtask",
		mcpsdk.WithDescription("Create a subtask under a parent task."),
		mcpsdk.WithString("parent_task_id", mcpsdk.Required(), mcpsdk.Description("Parent task ID.")),
		mcpsdk.WithString("title", mcpsdk.Required(), mcpsdk.Description("Subtask title.")),
		mcpsdk.WithString("description", mcpsdk.Description("Subtask description.")),
		mcpsdk.WithString("priority", mcpsdk.Description("Priority: urgent, high, medium, low, none."), mcpsdk.DefaultString("medium")),
	), s.tracked("create_subtask", s.handleCreateSubtask))

	s.mcpServer.AddTool(mcpsdk.NewTool("add_dependency",
		mcpsdk.WithDescription("Add a dependency between two tasks."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithString("depends_on_task_id", mcpsdk.Required(), mcpsdk.Description("ID of the task this depends on.")),
		mcpsdk.WithString("dependency_type", mcpsdk.Description("Dependency type: blocks, relates_to, is_child_of."), mcpsdk.DefaultString("blocks")),
	), s.tracked("add_dependency", s.handleAddDependency))

	// --- Atomic Task Checkout ---
	s.mcpServer.AddTool(mcpsdk.NewTool("checkout_task",
		mcpsdk.WithDescription("Atomically acquire an exclusive lock on a task. Prevents other agents from checking out the same task simultaneously. The lock is TTL-based and will expire automatically. Use before starting work on a task to ensure exclusive access."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID to check out.")),
	), s.tracked("checkout_task", s.handleCheckoutTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("release_task",
		mcpsdk.WithDescription("Release the exclusive lock on a task acquired via checkout_task. Call when done with the task or if you need to hand it off. The lock is also released automatically when it expires."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID to release.")),
	), s.tracked("release_task", s.handleReleaseTask))

	// --- Comments & Artifacts ---
	s.mcpServer.AddTool(mcpsdk.NewTool("list_comments",
		mcpsdk.WithDescription("List comments on a task."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithBoolean("include_internal", mcpsdk.Description("Include internal (agent-only) comments."), mcpsdk.DefaultBool(true)),
		mcpsdk.WithNumber("limit", mcpsdk.Description("Max comments to return (default 50).")),
	), s.tracked("list_comments", s.handleListComments))

	s.mcpServer.AddTool(mcpsdk.NewTool("upload_artifact",
		mcpsdk.WithDescription("Upload an artifact (file, code, log, etc.) to a task."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
		mcpsdk.WithString("name", mcpsdk.Required(), mcpsdk.Description("Artifact filename.")),
		mcpsdk.WithString("content", mcpsdk.Required(), mcpsdk.Description("Artifact content (text or base64-encoded).")),
		mcpsdk.WithString("artifact_type", mcpsdk.Description("Type: file, code, log, report, link, image, data."), mcpsdk.DefaultString("file")),
		mcpsdk.WithString("mime_type", mcpsdk.Description("MIME type. Auto-detected from name if omitted.")),
		mcpsdk.WithObject("metadata", mcpsdk.Description("Additional metadata.")),
	), s.tracked("upload_artifact", s.handleUploadArtifact))

	s.mcpServer.AddTool(mcpsdk.NewTool("list_artifacts",
		mcpsdk.WithDescription("List artifacts attached to a task."),
		mcpsdk.WithString("task_id", mcpsdk.Required(), mcpsdk.Description("Task ID.")),
	), s.tracked("list_artifacts", s.handleListArtifacts))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_artifact",
		mcpsdk.WithDescription("Get artifact details and optionally its content."),
		mcpsdk.WithString("artifact_id", mcpsdk.Required(), mcpsdk.Description("Artifact ID.")),
		mcpsdk.WithBoolean("include_content", mcpsdk.Description("Include content for text files under 1MB."), mcpsdk.DefaultBool(false)),
	), s.tracked("get_artifact", s.handleGetArtifact))

	// --- Event Bus ---
	s.mcpServer.AddTool(mcpsdk.NewTool("publish_summary",
		mcpsdk.WithDescription("Publish a work summary event (convenience wrapper for publish_event with type=summary). Kept for backward compatibility — prefer publish_event with event_type='summary'."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithString("task_id", mcpsdk.Description("Related task ID.")),
		mcpsdk.WithString("summary", mcpsdk.Required(), mcpsdk.Description("Summary of work done.")),
		mcpsdk.WithArray("key_decisions", mcpsdk.Description("Key decisions made."), mcpsdk.WithStringItems()),
		mcpsdk.WithArray("artifacts_created", mcpsdk.Description("Artifacts created."), mcpsdk.WithStringItems()),
		mcpsdk.WithArray("blockers", mcpsdk.Description("Current blockers."), mcpsdk.WithStringItems()),
		mcpsdk.WithArray("next_steps", mcpsdk.Description("Suggested next steps."), mcpsdk.WithStringItems()),
		mcpsdk.WithObject("metrics", mcpsdk.Description("Metrics (lines changed, tests passed, etc.).")),
	), s.tracked("publish_summary", s.handlePublishSummary))

	s.mcpServer.AddTool(mcpsdk.NewTool("subscribe_events",
		mcpsdk.WithDescription("Configure push notification delivery for task events. Optionally sets a callback URL that Mesh will POST events to. Returns SSE and long-poll endpoint URLs for alternative delivery mechanisms."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithArray("event_types", mcpsdk.Description("Event types to subscribe to."), mcpsdk.WithStringItems()),
		mcpsdk.WithString("callback_url", mcpsdk.Description("Optional URL where Mesh will POST task events (task.assigned, task.created, task.status_changed). Leave empty to only use SSE or long-polling.")),
	), s.tracked("subscribe_events", s.handleSubscribeEvents))

	// --- Agent Hierarchy ---
	s.mcpServer.AddTool(mcpsdk.NewTool("register_sub_agent",
		mcpsdk.WithDescription("Register a sub-agent under the calling agent."),
		mcpsdk.WithString("name", mcpsdk.Required(), mcpsdk.Description("Sub-agent name.")),
		mcpsdk.WithString("agent_type", mcpsdk.Required(), mcpsdk.Description("Agent type: claude_code, openclaw, cline, aider, custom.")),
		mcpsdk.WithObject("capabilities", mcpsdk.Description("Agent capabilities as key-value pairs.")),
	), s.tracked("register_sub_agent", s.handleRegisterSubAgent))

	s.mcpServer.AddTool(mcpsdk.NewTool("list_sub_agents",
		mcpsdk.WithDescription("List sub-agents of an agent."),
		mcpsdk.WithString("agent_id", mcpsdk.Description("Parent agent ID. Defaults to the calling agent.")),
		mcpsdk.WithBoolean("recursive", mcpsdk.Description("Return all descendants (up to 10 levels deep)."), mcpsdk.DefaultBool(false)),
	), s.tracked("list_sub_agents", s.handleListSubAgents))

	// --- Team & Rules ---
	s.mcpServer.AddTool(mcpsdk.NewTool("get_team_directory",
		mcpsdk.WithDescription("Get the workspace team directory listing all agents and human members with their profiles."),
	), s.tracked("get_team_directory", s.handleGetTeamDirectory))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_project_rules",
		mcpsdk.WithDescription("Get all rules configured for a project (all scopes: workspace + project). Kept for backward compatibility — prefer get_my_rules for agent-scoped effective rules."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
	), s.tracked("get_project_rules", s.handleGetProjectRules))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_assignment_rules",
		mcpsdk.WithDescription("Get effective assignment rules for a project, merged from workspace and project level with source annotations."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
	), s.tracked("get_assignment_rules", s.handleGetAssignmentRules))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_workflow_rules",
		mcpsdk.WithDescription("Get workflow rules for a project including allowed transitions, policies, and permissions for the calling agent."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
	), s.tracked("get_workflow_rules", s.handleGetWorkflowRules))

	s.mcpServer.AddTool(mcpsdk.NewTool("update_agent_profile",
		mcpsdk.WithDescription("Update the calling agent's profile fields such as role, capabilities, responsibility zone, and working hours."),
		mcpsdk.WithString("role", mcpsdk.Description("Agent role (e.g. developer, reviewer, tester).")),
		mcpsdk.WithArray("capabilities", mcpsdk.Description("List of capability strings (e.g. go, react, testing)."), mcpsdk.WithStringItems()),
		mcpsdk.WithString("responsibility_zone", mcpsdk.Description("Area of responsibility (e.g. Backend, Frontend).")),
		mcpsdk.WithString("escalation_to", mcpsdk.Description("Agent ID or name to escalate issues to.")),
		mcpsdk.WithArray("accepts_from", mcpsdk.Description("Agent IDs or types this agent accepts tasks from."), mcpsdk.WithStringItems()),
		mcpsdk.WithNumber("max_concurrent_tasks", mcpsdk.Description("Maximum number of concurrent tasks this agent can handle.")),
		mcpsdk.WithString("working_hours", mcpsdk.Description("Working hours description (e.g. 24/7, 9-17 UTC).")),
		mcpsdk.WithString("description", mcpsdk.Description("Human-readable description of the agent's purpose.")),
		mcpsdk.WithString("callback_url", mcpsdk.Description("URL where Mesh will POST task events (task.assigned, task.status_changed, task.commented). Set to empty string to disable.")),
	), s.tracked("update_agent_profile", s.handleUpdateAgentProfile))

	// --- Config ---
	s.mcpServer.AddTool(mcpsdk.NewTool("import_workspace_config",
		mcpsdk.WithDescription("Import workspace configuration from YAML. Applies rules, statuses, and project templates defined in the YAML."),
		mcpsdk.WithString("yaml_content", mcpsdk.Required(), mcpsdk.Description("YAML configuration content as a string.")),
	), s.tracked("import_workspace_config", s.handleImportWorkspaceConfig))

	s.mcpServer.AddTool(mcpsdk.NewTool("export_workspace_config",
		mcpsdk.WithDescription("Export the current workspace configuration as YAML, including rules, project templates, and settings."),
	), s.tracked("export_workspace_config", s.handleExportWorkspaceConfig))

	// --- Push Notifications ---
	s.mcpServer.AddTool(mcpsdk.NewTool("poll_tasks",
		mcpsdk.WithDescription("Long-poll for new task assignments. Blocks until a task is assigned to this agent or the timeout expires. Returns current assigned tasks and whether any change occurred. Kept for backward compatibility — prefer get_my_tasks for non-blocking access."),
		mcpsdk.WithNumber("timeout", mcpsdk.Description("Maximum seconds to wait for new assignments (default 30, max 120).")),
	), s.tracked("poll_tasks", s.handlePollTasks))

	// --- Recurring Tasks ---
	s.mcpServer.AddTool(mcpsdk.NewTool("create_recurring_task",
		mcpsdk.WithDescription("Creates a recurring task schedule that automatically spawns task instances on a schedule. Each instance gets access to the previous instance's summary. Use this for regular automated work: weekly reports, daily checks, periodic audits."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Target project UUID.")),
		mcpsdk.WithString("title_template", mcpsdk.Required(), mcpsdk.Description("Task title template. Supports {{.Date}}, {{.Number}}, {{.Week}}, {{.Month}}.")),
		mcpsdk.WithString("frequency", mcpsdk.Required(), mcpsdk.Description("Recurrence frequency: daily, weekly, monthly, custom. Use 'custom' with cron_expr for fine-grained control.")),
		mcpsdk.WithString("description_template", mcpsdk.Description("Task description template. Also supports {{.PrevSummary}} for previous instance context.")),
		mcpsdk.WithString("cron_expr", mcpsdk.Description("5-field cron expression (required if frequency=custom). Example: '0 9 * * 1' = every Monday at 9am.")),
		mcpsdk.WithString("timezone", mcpsdk.Description("IANA timezone for schedule evaluation. Default: UTC.")),
		mcpsdk.WithString("assignee_id", mcpsdk.Description("Agent or user UUID to assign each instance.")),
		mcpsdk.WithString("assignee_type", mcpsdk.Description("Assignee type: user, agent, unassigned."), mcpsdk.DefaultString("unassigned")),
		mcpsdk.WithString("priority", mcpsdk.Description("Priority: urgent, high, medium, low, none."), mcpsdk.DefaultString("none")),
		mcpsdk.WithArray("labels", mcpsdk.Description("Labels to apply to each instance."), mcpsdk.WithStringItems()),
		mcpsdk.WithString("starts_at", mcpsdk.Description("When to start the schedule (RFC3339). Default: now.")),
		mcpsdk.WithString("ends_at", mcpsdk.Description("When to stop the schedule (RFC3339). Default: no end.")),
		mcpsdk.WithNumber("max_instances", mcpsdk.Description("Maximum number of instances to create. Default: unlimited.")),
	), s.tracked("create_recurring_task", s.handleCreateRecurringTask))

	s.mcpServer.AddTool(mcpsdk.NewTool("list_recurring_schedules",
		mcpsdk.WithDescription("Lists all recurring task schedules for a project."),
		mcpsdk.WithString("project_id", mcpsdk.Required(), mcpsdk.Description("Project ID.")),
		mcpsdk.WithBoolean("active_only", mcpsdk.Description("Only return active schedules."), mcpsdk.DefaultBool(true)),
	), s.tracked("list_recurring_schedules", s.handleListRecurringSchedules))

	s.mcpServer.AddTool(mcpsdk.NewTool("get_recurring_history",
		mcpsdk.WithDescription("Returns the history of all instances for a recurring task schedule. ALWAYS call this when you receive a recurring task — it gives you context on what previous instances accomplished, what issues were found, and what artifacts were produced. Use it to continue work intelligently rather than starting from scratch."),
		mcpsdk.WithString("recurring_schedule_id", mcpsdk.Required(), mcpsdk.Description("UUID of the recurring schedule. Available in task.recurring_schedule_id field.")),
		mcpsdk.WithNumber("limit", mcpsdk.Description("Number of most recent instances to return. Default: 5. Use higher value for deep historical context.")),
	), s.tracked("get_recurring_history", s.handleGetRecurringHistory))

	s.mcpServer.AddTool(mcpsdk.NewTool("trigger_recurring_now",
		mcpsdk.WithDescription("Immediately creates the next instance of a recurring schedule, without waiting for the scheduled time. Useful for testing or urgent execution."),
		mcpsdk.WithString("recurring_schedule_id", mcpsdk.Required(), mcpsdk.Description("UUID of the recurring schedule.")),
	), s.tracked("trigger_recurring_now", s.handleTriggerRecurringNow))
}

// --- Helper functions ---

// parseUUID parses a UUID string, returning an error result if invalid.
func parseUUID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, fmt.Errorf("UUID is required")
	}
	return uuid.Parse(s)
}

// jsonResult marshals the value to JSON and returns a text result.
func jsonResult(v any) (*mcpsdk.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcpsdk.NewToolResultError("failed to marshal response"), nil
	}
	return mcpsdk.NewToolResultText(string(data)), nil
}

// errResult returns an error tool result with a formatted message.
func errResult(format string, args ...any) (*mcpsdk.CallToolResult, error) {
	return mcpsdk.NewToolResultError(fmt.Sprintf(format, args...)), nil
}

// parseStringSlice extracts a string slice from request arguments.
func parseStringSlice(request mcpsdk.CallToolRequest, key string) []string {
	args := request.GetArguments()
	if args == nil {
		return nil
	}
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

// detectMIMEType guesses MIME type from file extension.
func detectMIMEType(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "application/x-yaml"
	case strings.HasSuffix(lower, ".xml"):
		return "application/xml"
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "text/html"
	case strings.HasSuffix(lower, ".css"):
		return "text/css"
	case strings.HasSuffix(lower, ".js"):
		return "application/javascript"
	case strings.HasSuffix(lower, ".ts"):
		return "application/typescript"
	case strings.HasSuffix(lower, ".go"):
		return "text/x-go"
	case strings.HasSuffix(lower, ".py"):
		return "text/x-python"
	case strings.HasSuffix(lower, ".rs"):
		return "text/x-rust"
	case strings.HasSuffix(lower, ".md"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".csv"):
		return "text/csv"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".zip"):
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

// truncate shortens a string to at most maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// resolveStatusSlug looks up a status UUID from its slug by querying the REST API.
// Returns the status ID and name on success.
func (s *Server) resolveStatusSlug(ctx context.Context, projectID, slug string) (string, string, error) {
	statuses, err := s.getRESTClient(ctx).GetProjectStatuses(ctx, projectID)
	if err != nil {
		return "", "", fmt.Errorf("get statuses: %w", err)
	}
	for _, st := range statuses {
		stSlug, _ := st["slug"].(string)
		if stSlug == slug {
			stID, _ := st["id"].(string)
			stName, _ := st["name"].(string)
			return stID, stName, nil
		}
	}
	return "", "", fmt.Errorf("status '%s' not found in project", slug)
}

// defaultStatusForProject returns the default status ID for a project by querying the REST API.
func (s *Server) defaultStatusForProject(ctx context.Context, projectID string) (string, error) {
	statuses, err := s.getRESTClient(ctx).GetProjectStatuses(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("get statuses: %w", err)
	}
	// First pass: find the default status.
	for _, st := range statuses {
		if isDefault, _ := st["is_default"].(bool); isDefault {
			stID, _ := st["id"].(string)
			return stID, nil
		}
	}
	// Second pass: return first status.
	if len(statuses) > 0 {
		stID, _ := statuses[0]["id"].(string)
		return stID, nil
	}
	return "", fmt.Errorf("no statuses defined for project")
}
