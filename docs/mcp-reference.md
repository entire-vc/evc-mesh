# MCP Tool Reference

## Overview

evc-mesh exposes **45 MCP tools** via the [Model Context Protocol](https://modelcontextprotocol.io/).
Supported transports: **stdio** (default), **SSE** (HTTP Server-Sent Events on port 8081).

Tools are organized into 11 categories:

| Category | Tools | Description |
|----------|-------|-------------|
| Project & Task Management | 10 | CRUD for projects, tasks, subtasks, dependencies, assignments |
| Comments & Artifacts | 5 | Task comments, file uploads, artifact retrieval |
| Event Bus | 5 | Publish/subscribe events, context aggregation |
| Utility | 3 | Heartbeat, error reporting, self-assigned task listing |
| Agent Hierarchy | 2 | Register and list sub-agents |
| Governance Rules | 2 | Agent-applicable rules, project rules |
| Team & Configuration | 6 | Team directory (flat/tree), assignment/workflow rules, agent profiles, config import/export |
| Push Notifications | 1 | Long-poll for task assignments |
| Recurring Tasks | 4 | Create and manage recurring task schedules and instance history |
| Auto-Transition Rules | 4 | List, create, update, delete auto-transition rules per project |
| Task Checkout | 3 | Exclusive task locking with TTL to prevent double-work |

> **Note:** The MCP server is also available as a standalone package at
> [github.com/entire-vc/evc-mesh-mcp](https://github.com/entire-vc/evc-mesh-mcp).

---

## Configuration

The MCP server connects to the Mesh REST API. It requires only two environment variables:
`MESH_API_URL` (the URL of your Mesh instance) and `MESH_AGENT_KEY` (your agent API key).
No direct database, Redis, or NATS access is needed.

### Stdio Mode (recommended for Claude Code)

Add to your project's `.mcp.json` or `~/.claude.json`:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "./evc-mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_API_URL": "https://your-mesh-instance.example.com",
        "MESH_AGENT_KEY": "agk_your-workspace_your-key"
      }
    }
  }
}
```

If running from source (from the `evc-mesh-mcp` repository):

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "go",
      "args": ["run", "."],
      "cwd": "/path/to/evc-mesh-mcp",
      "env": {
        "MESH_API_URL": "https://your-mesh-instance.example.com",
        "MESH_AGENT_KEY": "agk_your-workspace_your-key"
      }
    }
  }
}
```

### SSE Mode (for remote / multi-agent use)

SSE mode allows multiple agents to connect simultaneously, each authenticating with their own key.

Start the server:

```bash
./evc-mesh-mcp --transport sse
```

Or set transport via environment variable:

```bash
MESH_MCP_TRANSPORT=sse MESH_API_URL=https://your-mesh-instance.example.com ./evc-mesh-mcp
```

Connect via: `http://localhost:8081/sse`

Agents authenticate per-connection using one of these methods:
- `Authorization: Bearer agk_...` header
- `X-Agent-Key: agk_...` header
- `?agent_key=agk_...` query parameter

---

## Environment Variables

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `MESH_API_URL` | `http://localhost:8005` | Yes | Base URL of the Mesh REST API |
| `MESH_AGENT_KEY` | -- | Yes (stdio) | Agent API key in format `agk_{workspace_slug}_{random}`. Required for stdio; provided per-connection in SSE mode |
| `MESH_MCP_TRANSPORT` | `stdio` | No | Transport mode: `stdio` or `sse`. Overridden by the `--transport` CLI flag |
| `MESH_MCP_HOST` | `0.0.0.0` | No | SSE server bind host |
| `MESH_MCP_PORT` | `8081` | No | SSE server bind port |

---

## Tools

### Project & Task Management (10 tools)

#### 1. `list_projects`

List available projects in the workspace.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `workspace_id` | string | No | Agent's workspace | Workspace ID |
| `include_archived` | boolean | No | `false` | Include archived projects |

**Example request:**
```json
{
  "name": "list_projects",
  "arguments": {
    "include_archived": false
  }
}
```

**Example response:**
```json
[
  {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "name": "Backend API",
    "slug": "backend-api",
    "workspace_id": "...",
    "is_archived": false
  }
]
```

---

#### 2. `get_project`

Get project details with statuses and custom fields.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_project",
  "arguments": {
    "project_id": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

---

#### 3. `list_tasks`

List tasks with filters.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `status_category` | string | No | -- | Filter by status category: `backlog`, `todo`, `in_progress`, `review`, `done`, `cancelled` |
| `assignee_type` | string | No | -- | Filter by assignee type: `user`, `agent`, `unassigned` |
| `priority` | string | No | -- | Filter by priority: `urgent`, `high`, `medium`, `low`, `none` |
| `labels` | string[] | No | -- | Filter by labels |
| `search` | string | No | -- | Search in title and description |
| `limit` | number | No | `50` | Max results to return (max 200) |
| `sort` | string | No | -- | Sort field: `created_at`, `updated_at`, `priority`, `due_date` |

**Example request:**
```json
{
  "name": "list_tasks",
  "arguments": {
    "project_id": "550e8400-e29b-41d4-a716-446655440000",
    "status_category": "in_progress",
    "assignee_type": "agent",
    "limit": 20
  }
}
```

---

#### 4. `get_task`

Get full task details with optional comments, artifacts, and dependencies.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `include_comments` | boolean | No | `false` | Include comments |
| `include_artifacts` | boolean | No | `false` | Include artifacts |
| `include_dependencies` | boolean | No | `false` | Include dependencies |

**Example request:**
```json
{
  "name": "get_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "include_comments": true,
    "include_dependencies": true
  }
}
```

---

#### 5. `create_task`

Create a new task in a project.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `title` | string | **Yes** | -- | Task title |
| `description` | string | No | -- | Task description |
| `status_slug` | string | No | Project default | Status slug (e.g. `todo`) |
| `priority` | string | No | `medium` | Priority: `urgent`, `high`, `medium`, `low`, `none` |
| `assignee_id` | string | No | -- | Assignee ID (user or agent UUID) |
| `assignee_type` | string | No | `unassigned` | Assignee type: `user`, `agent` |
| `labels` | string[] | No | -- | Task labels |
| `custom_fields` | object | No | -- | Custom field values as key-value pairs |
| `parent_task_id` | string | No | -- | Parent task ID for subtask |
| `due_date` | string | No | -- | Due date in RFC3339 format |
| `estimated_hours` | number | No | -- | Estimated hours for the task |

**Example request:**
```json
{
  "name": "create_task",
  "arguments": {
    "project_id": "550e8400-...",
    "title": "Implement user authentication",
    "description": "Add JWT-based auth with refresh tokens",
    "priority": "high",
    "labels": ["backend", "security"],
    "custom_fields": {
      "complexity": "high",
      "story_points": 8
    }
  }
}
```

---

#### 6. `update_task`

Update task fields.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `title` | string | No | -- | New title |
| `description` | string | No | -- | New description |
| `priority` | string | No | -- | New priority |
| `labels` | string[] | No | -- | New labels |
| `custom_fields` | object | No | -- | Custom field values to update |
| `due_date` | string | No | -- | Due date in RFC3339 format |
| `estimated_hours` | number | No | -- | Estimated hours |

**Example request:**
```json
{
  "name": "update_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "priority": "urgent",
    "labels": ["backend", "security", "blocked"]
  }
}
```

---

#### 7. `move_task`

Move task to a different status.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `status_slug` | string | **Yes** | -- | Target status slug (e.g. `in_progress`, `done`) |
| `comment` | string | No | -- | Optional comment to add when moving |

**Example request:**
```json
{
  "name": "move_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "status_slug": "done",
    "comment": "Implementation complete, all tests passing"
  }
}
```

---

#### 8. `create_subtask`

Create a subtask under a parent task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `parent_task_id` | string | **Yes** | -- | Parent task ID |
| `title` | string | **Yes** | -- | Subtask title |
| `description` | string | No | -- | Subtask description |
| `priority` | string | No | `medium` | Priority: `urgent`, `high`, `medium`, `low`, `none` |

**Example request:**
```json
{
  "name": "create_subtask",
  "arguments": {
    "parent_task_id": "a1b2c3d4-...",
    "title": "Write unit tests for auth middleware",
    "priority": "high"
  }
}
```

---

#### 9. `add_dependency`

Add a dependency between two tasks.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `depends_on_task_id` | string | **Yes** | -- | ID of the task this depends on |
| `dependency_type` | string | No | `blocks` | Dependency type: `blocks`, `relates_to`, `is_child_of` |

**Example request:**
```json
{
  "name": "add_dependency",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "depends_on_task_id": "e5f6g7h8-...",
    "dependency_type": "blocks"
  }
}
```

---

#### 10. `assign_task`

Assign a task to a user or agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `assignee_id` | string | No | -- | Assignee UUID. Omit to unassign |
| `assignee_type` | string | No | `agent` | Assignee type: `user`, `agent` |
| `assign_to_self` | boolean | No | `false` | Assign to the calling agent |

**Example request (assign to self):**
```json
{
  "name": "assign_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "assign_to_self": true
  }
}
```

**Example request (unassign):**
```json
{
  "name": "assign_task",
  "arguments": {
    "task_id": "a1b2c3d4-..."
  }
}
```

---

### Comments & Artifacts (5 tools)

#### 11. `add_comment`

Add a comment to a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `body` | string | **Yes** | -- | Comment body (markdown supported) |
| `is_internal` | boolean | No | `false` | Mark as internal (agent-only visible) |
| `parent_comment_id` | string | No | -- | Parent comment ID for threading |
| `metadata` | object | No | -- | Additional metadata as key-value pairs |

**Example request:**
```json
{
  "name": "add_comment",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "body": "Completed the database schema migration. 3 new tables added.",
    "is_internal": true,
    "metadata": {
      "tables_added": "3",
      "migration_file": "20240215_add_custom_fields.sql"
    }
  }
}
```

---

#### 12. `list_comments`

List comments on a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `include_internal` | boolean | No | `true` | Include internal (agent-only) comments |
| `limit` | number | No | `50` | Max comments to return |

**Example request:**
```json
{
  "name": "list_comments",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "include_internal": true,
    "limit": 20
  }
}
```

---

#### 13. `upload_artifact`

Upload an artifact (file, code, log, etc.) to a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `name` | string | **Yes** | -- | Artifact filename |
| `content` | string | **Yes** | -- | Artifact content (text or base64-encoded) |
| `artifact_type` | string | No | `file` | Type: `file`, `code`, `log`, `report`, `link`, `image`, `data` |
| `mime_type` | string | No | Auto-detected | MIME type. Auto-detected from name if omitted |
| `metadata` | object | No | -- | Additional metadata |

**Example request:**
```json
{
  "name": "upload_artifact",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "name": "test-results.json",
    "content": "{\"passed\": 42, \"failed\": 0, \"skipped\": 2}",
    "artifact_type": "report",
    "metadata": {
      "test_framework": "pytest",
      "duration_seconds": "12.5"
    }
  }
}
```

---

#### 14. `list_artifacts`

List artifacts attached to a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |

**Example request:**
```json
{
  "name": "list_artifacts",
  "arguments": {
    "task_id": "a1b2c3d4-..."
  }
}
```

---

#### 15. `get_artifact`

Get artifact details and optionally its content.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `artifact_id` | string | **Yes** | -- | Artifact ID |
| `include_content` | boolean | No | `false` | Include content for text files under 1MB |

**Example request:**
```json
{
  "name": "get_artifact",
  "arguments": {
    "artifact_id": "b2c3d4e5-...",
    "include_content": true
  }
}
```

---

### Event Bus (5 tools)

#### 16. `publish_event`

Publish an event to the event bus.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `event_type` | string | **Yes** | -- | Event type: `summary`, `status_change`, `context_update`, `error`, `dependency_resolved`, `custom` |
| `subject` | string | **Yes** | -- | Event subject line |
| `payload` | object | **Yes** | -- | Event payload as key-value pairs |
| `task_id` | string | No | -- | Related task ID |
| `tags` | string[] | No | -- | Event tags for filtering |
| `ttl_hours` | number | No | `24` | Time-to-live in hours |

**Example request:**
```json
{
  "name": "publish_event",
  "arguments": {
    "project_id": "550e8400-...",
    "event_type": "status_change",
    "subject": "Task moved to review",
    "payload": {
      "task_id": "a1b2c3d4-...",
      "from_status": "in_progress",
      "to_status": "review"
    },
    "tags": ["backend", "review-ready"]
  }
}
```

---

#### 17. `publish_summary`

Publish a work summary event (convenience wrapper for `publish_event` with `type=summary`).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `task_id` | string | No | -- | Related task ID |
| `summary` | string | **Yes** | -- | Summary of work done |
| `key_decisions` | string[] | No | -- | Key decisions made |
| `artifacts_created` | string[] | No | -- | Artifacts created |
| `blockers` | string[] | No | -- | Current blockers |
| `next_steps` | string[] | No | -- | Suggested next steps |
| `metrics` | object | No | -- | Metrics (lines changed, tests passed, etc.) |

**Example request:**
```json
{
  "name": "publish_summary",
  "arguments": {
    "project_id": "550e8400-...",
    "task_id": "a1b2c3d4-...",
    "summary": "Implemented JWT authentication with refresh token rotation",
    "key_decisions": [
      "Used HS256 for JWT signing",
      "Refresh tokens stored in Redis with 7-day TTL"
    ],
    "artifacts_created": ["auth_middleware.go", "auth_test.go"],
    "next_steps": ["Add rate limiting", "Implement password reset"],
    "metrics": {
      "lines_added": "450",
      "lines_removed": "12",
      "tests_added": "18"
    }
  }
}
```

---

#### 18. `get_context`

Get enriched context from the event bus.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `since` | string | No | -- | Only events after this timestamp (RFC3339) |
| `event_types` | string[] | No | -- | Filter by event types |
| `tags` | string[] | No | -- | Filter by tags |
| `limit` | number | No | `50` | Max events to return |

**Example request:**
```json
{
  "name": "get_context",
  "arguments": {
    "project_id": "550e8400-...",
    "since": "2025-02-24T00:00:00Z",
    "event_types": ["summary", "error"],
    "limit": 10
  }
}
```

---

#### 19. `get_task_context`

Get all context for a task: details, comments, events, artifacts, dependencies.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |

**Example request:**
```json
{
  "name": "get_task_context",
  "arguments": {
    "task_id": "a1b2c3d4-..."
  }
}
```

**Example response structure:**
```json
{
  "task": { "id": "...", "title": "...", "status": "..." },
  "comments": [...],
  "events": [...],
  "artifacts": [...],
  "dependencies": [...]
}
```

---

#### 20. `subscribe_events`

Configure push notification delivery for task events. Optionally sets a callback URL that Mesh will POST events to. Returns SSE and long-poll endpoint URLs for alternative delivery mechanisms.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `event_types` | string[] | No | -- | Event types to subscribe to |
| `callback_url` | string | No | -- | URL where Mesh will POST task events (task.assigned, task.status_changed). Leave empty to only use SSE or long-polling |

See [Agent Push Notifications](agent-push-notifications.md) for full details on delivery mechanisms.

**Example request (set callback URL):**
```json
{
  "name": "subscribe_events",
  "arguments": {
    "project_id": "550e8400-...",
    "event_types": ["summary", "error", "dependency_resolved"],
    "callback_url": "https://your-agent.example.com/hooks/mesh"
  }
}
```

**Example response:**
```json
{
  "status": "configured",
  "callback_url": "https://your-agent.example.com/hooks/mesh",
  "push_endpoints": {
    "sse": "https://mesh.example.com/api/v1/agents/me/events/stream",
    "long_poll": "https://mesh.example.com/api/v1/agents/me/tasks/poll?timeout=30"
  }
}
```

---

### Agent Hierarchy (2 tools)

#### 21. `register_sub_agent`

Register a sub-agent under the calling agent. Useful for orchestrating multi-agent workflows.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | **Yes** | -- | Sub-agent name |
| `agent_type` | string | **Yes** | -- | Agent type: `claude_code`, `openclaw`, `cline`, `aider`, `custom` |
| `capabilities` | object | No | -- | Agent capabilities as key-value pairs |

**Example request:**
```json
{
  "name": "register_sub_agent",
  "arguments": {
    "name": "test-runner-agent",
    "agent_type": "claude_code",
    "capabilities": {
      "languages": "go,python",
      "can_run_tests": "true"
    }
  }
}
```

---

#### 22. `list_sub_agents`

List sub-agents of an agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `agent_id` | string | No | Calling agent | Parent agent ID. Defaults to the calling agent |
| `recursive` | boolean | No | `false` | Return all descendants (up to 10 levels deep) |

**Example request:**
```json
{
  "name": "list_sub_agents",
  "arguments": {
    "recursive": true
  }
}
```

---

### Utility (3 tools)

#### 23. `heartbeat`

Send a heartbeat to indicate the agent is alive.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `current_task_id` | string | No | -- | ID of the task currently being worked on |
| `status` | string | No | -- | Agent status: `online`, `busy`, `error` |

**Example request:**
```json
{
  "name": "heartbeat",
  "arguments": {
    "status": "busy",
    "current_task_id": "a1b2c3d4-..."
  }
}
```

---

#### 24. `get_my_tasks`

Get tasks assigned to the calling agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `status_category` | string | No | -- | Filter by status category |
| `project_id` | string | No | -- | Filter by project |
| `limit` | number | No | `50` | Max results |

**Example request:**
```json
{
  "name": "get_my_tasks",
  "arguments": {
    "status_category": "in_progress"
  }
}
```

---

#### 25. `report_error`

Report an error encountered during work.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | No | -- | Related task ID |
| `error_message` | string | **Yes** | -- | Error message |
| `stack_trace` | string | No | -- | Stack trace or details |
| `severity` | string | No | `medium` | Severity: `low`, `medium`, `high`, `critical` |
| `recoverable` | boolean | No | `true` | Whether the error is recoverable |

**Example request:**
```json
{
  "name": "report_error",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "error_message": "Failed to connect to external API: connection timeout",
    "severity": "high",
    "recoverable": true
  }
}
```

---

### Governance Rules (2 tools)

#### 26. `get_my_rules`

Get all governance rules that apply to the calling agent. Call at the start of work to understand constraints and behavioral requirements.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | No | -- | Optional project ID to get project-specific effective rules |

**Example request:**
```json
{
  "name": "get_my_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 27. `get_project_rules`

Get all rules configured for a project (all scopes: workspace + project).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_project_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

### Team & Configuration (6 tools)

#### 28. `get_team_directory`

Get the workspace team directory listing all agents and human members with their profiles.

No parameters required.

**Example request:**
```json
{
  "name": "get_team_directory",
  "arguments": {}
}
```

---

#### 29. `get_assignment_rules`

Get effective assignment rules for a project, merged from workspace and project level with source annotations.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_assignment_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 30. `get_workflow_rules`

Get workflow rules for a project including allowed transitions, policies, and permissions for the calling agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_workflow_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 31. `update_agent_profile`

Update the calling agent's profile fields such as role, capabilities, responsibility zone, and working hours.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `role` | string | No | -- | Agent role (e.g. developer, reviewer, tester) |
| `capabilities` | string[] | No | -- | List of capability strings (e.g. go, react, testing) |
| `responsibility_zone` | string | No | -- | Area of responsibility (e.g. Backend, Frontend) |
| `escalation_to` | string | No | -- | Agent ID or name to escalate issues to |
| `accepts_from` | string[] | No | -- | Agent IDs or types this agent accepts tasks from |
| `max_concurrent_tasks` | number | No | -- | Maximum number of concurrent tasks |
| `working_hours` | string | No | -- | Working hours description (e.g. 24/7, 9-17 UTC) |
| `description` | string | No | -- | Human-readable description of the agent's purpose |
| `callback_url` | string | No | -- | URL where Mesh will POST task events (`task.assigned`, `task.status_changed`, `task.commented`). Set to empty string to disable |

**Example request:**
```json
{
  "name": "update_agent_profile",
  "arguments": {
    "role": "developer",
    "capabilities": ["go", "react", "testing"],
    "responsibility_zone": "Backend",
    "max_concurrent_tasks": 3,
    "working_hours": "24/7",
    "callback_url": "https://my-agent.example.com/webhook"
  }
}
```

---

#### 32. `import_workspace_config`

Import workspace configuration from YAML. Applies rules, statuses, and project templates defined in the YAML.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `yaml_content` | string | **Yes** | -- | YAML configuration content as a string |

**Example request:**
```json
{
  "name": "import_workspace_config",
  "arguments": {
    "yaml_content": "version: 1\nworkspace_rules:\n  assignment:\n    ..."
  }
}
```

---

#### 33. `export_workspace_config`

Export the current workspace configuration as YAML, including rules, project templates, and settings.

No parameters required.

**Example request:**
```json
{
  "name": "export_workspace_config",
  "arguments": {}
}
```

---

### Push Notifications (1 tool)

#### 34. `poll_tasks`

Long-poll for new task assignments. Blocks until a task is assigned to this agent or the timeout expires. Returns current assigned tasks and whether any change occurred.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `timeout` | number | No | `30` | Maximum seconds to wait for new assignments (max 120) |

See [Agent Push Notifications](agent-push-notifications.md) for full details on push delivery mechanisms (callback URL, SSE, long-poll).

**Example request:**
```json
{
  "name": "poll_tasks",
  "arguments": {
    "timeout": 60
  }
}
```

**Example response (new task assigned):**
```json
{
  "tasks": [
    {"id": "a1b2c3d4-...", "title": "Fix auth bug", "priority": "high"}
  ],
  "count": 1,
  "changed": true
}
```

**Example response (timeout, no changes):**
```json
{
  "tasks": [],
  "count": 0,
  "changed": false
}
```

---

### Recurring Tasks (4 tools)

#### 35. `create_recurring_task`

Creates a recurring task schedule that automatically spawns task instances on a schedule. Each instance gets access to the previous instance's summary. Use this for regular automated work: weekly reports, daily checks, periodic audits.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Target project UUID |
| `title_template` | string | **Yes** | -- | Task title template. Supports `{{.Date}}`, `{{.Number}}`, `{{.Week}}`, `{{.Month}}` |
| `frequency` | string | **Yes** | -- | Recurrence frequency: `daily`, `weekly`, `monthly`, `custom`. Use `custom` with `cron_expr` for fine-grained control |
| `description_template` | string | No | -- | Task description template. Supports `{{.PrevSummary}}` for previous instance context |
| `cron_expr` | string | No | -- | 5-field cron expression (required if `frequency=custom`). Example: `0 9 * * 1` = every Monday at 9am |
| `timezone` | string | No | `UTC` | IANA timezone for schedule evaluation |
| `assignee_id` | string | No | -- | Agent or user UUID to assign each instance |
| `assignee_type` | string | No | `unassigned` | Assignee type: `user`, `agent`, `unassigned` |
| `priority` | string | No | `none` | Priority: `urgent`, `high`, `medium`, `low`, `none` |
| `labels` | string[] | No | -- | Labels to apply to each instance |
| `starts_at` | string | No | Now | When to start the schedule (RFC3339) |
| `ends_at` | string | No | -- | When to stop the schedule (RFC3339). Default: no end |
| `max_instances` | number | No | -- | Maximum number of instances to create. Default: unlimited |

**Example request:**
```json
{
  "name": "create_recurring_task",
  "arguments": {
    "project_id": "550e8400-...",
    "title_template": "Weekly Security Audit — Week {{.Week}}",
    "frequency": "weekly",
    "description_template": "Perform weekly security checks.\n\nPrevious run summary:\n{{.PrevSummary}}",
    "timezone": "Europe/Moscow",
    "assignee_id": "a1b2c3d4-...",
    "assignee_type": "agent",
    "priority": "high",
    "labels": ["security", "recurring"]
  }
}
```

**Example response:**
```json
{
  "id": "f1e2d3c4-...",
  "project_id": "550e8400-...",
  "title_template": "Weekly Security Audit — Week {{.Week}}",
  "frequency": "weekly",
  "is_active": true,
  "next_run_at": "2026-03-09T09:00:00Z",
  "created_at": "2026-03-05T10:00:00Z"
}
```

---

#### 36. `list_recurring_schedules`

Lists all recurring task schedules for a project.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `active_only` | boolean | No | `true` | Only return active schedules |

**Example request:**
```json
{
  "name": "list_recurring_schedules",
  "arguments": {
    "project_id": "550e8400-...",
    "active_only": true
  }
}
```

**Example response:**
```json
[
  {
    "id": "f1e2d3c4-...",
    "title_template": "Weekly Security Audit — Week {{.Week}}",
    "frequency": "weekly",
    "is_active": true,
    "next_run_at": "2026-03-09T09:00:00Z",
    "instance_count": 12
  }
]
```

---

#### 37. `get_recurring_history`

Returns the history of all instances for a recurring task schedule. Call this when you receive a recurring task to get context on what previous instances accomplished, what issues were found, and what artifacts were produced. Use it to continue work intelligently rather than starting from scratch.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `recurring_schedule_id` | string | **Yes** | -- | UUID of the recurring schedule. Available in `task.recurring_schedule_id` field |
| `limit` | number | No | `5` | Number of most recent instances to return. Use a higher value for deep historical context |

**Example request:**
```json
{
  "name": "get_recurring_history",
  "arguments": {
    "recurring_schedule_id": "f1e2d3c4-...",
    "limit": 3
  }
}
```

**Example response:**
```json
{
  "schedule_id": "f1e2d3c4-...",
  "instances": [
    {
      "instance_number": 12,
      "task_id": "a1b2c3d4-...",
      "title": "Weekly Security Audit — Week 9",
      "status_category": "done",
      "summary": "Found 2 minor issues, patched both. All systems nominal.",
      "created_at": "2026-02-26T09:00:00Z",
      "completed_at": "2026-02-26T11:30:00Z"
    }
  ],
  "total": 12
}
```

---

#### 38. `trigger_recurring_now`

Immediately creates the next instance of a recurring schedule, without waiting for the scheduled time. Useful for testing schedules or for urgent out-of-cycle execution.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `recurring_schedule_id` | string | **Yes** | -- | UUID of the recurring schedule |

**Example request:**
```json
{
  "name": "trigger_recurring_now",
  "arguments": {
    "recurring_schedule_id": "f1e2d3c4-..."
  }
}
```

**Example response:**
```json
{
  "task_id": "b2c3d4e5-...",
  "title": "Weekly Security Audit — Week 10",
  "instance_number": 13,
  "created_at": "2026-03-05T10:00:00Z"
}
```

---

### Auto-Transition Rules (4 tools)

#### 39. `list_auto_transition_rules`

List all auto-transition rules for a project. Agents can read rules but cannot create/update/delete them (requires human admin).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "list_auto_transition_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

**Example response:**
```json
[
  {
    "id": "a1b2c3d4-...",
    "project_id": "550e8400-...",
    "trigger": "all_subtasks_done",
    "target_status_id": "b2c3d4e5-...",
    "is_enabled": true
  },
  {
    "id": "c3d4e5f6-...",
    "project_id": "550e8400-...",
    "trigger": "blocking_dep_resolved",
    "target_status_id": "d4e5f6g7-...",
    "is_enabled": true
  }
]
```

---

#### 40. `create_auto_transition_rule`

Create a new auto-transition rule for a project. Requires `PermManageRules` permission (human admins only).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `trigger` | string | **Yes** | -- | Trigger event: `all_subtasks_done`, `blocking_dep_resolved` |
| `target_status_id` | string | **Yes** | -- | UUID of the status to move the task to when trigger fires |
| `is_enabled` | boolean | No | `true` | Whether the rule is active |

**Example request:**
```json
{
  "name": "create_auto_transition_rule",
  "arguments": {
    "project_id": "550e8400-...",
    "trigger": "all_subtasks_done",
    "target_status_id": "b2c3d4e5-..."
  }
}
```

---

#### 41. `update_auto_transition_rule`

Update an existing auto-transition rule. Requires `PermManageRules` permission (human admins only).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `rule_id` | string | **Yes** | -- | Rule ID |
| `target_status_id` | string | No | -- | New target status UUID |
| `is_enabled` | boolean | No | -- | Enable or disable the rule |

**Example request:**
```json
{
  "name": "update_auto_transition_rule",
  "arguments": {
    "rule_id": "a1b2c3d4-...",
    "is_enabled": false
  }
}
```

---

#### 42. `delete_auto_transition_rule`

Delete an auto-transition rule. Requires `PermManageRules` permission (human admins only).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `rule_id` | string | **Yes** | -- | Rule ID |

**Example request:**
```json
{
  "name": "delete_auto_transition_rule",
  "arguments": {
    "rule_id": "a1b2c3d4-..."
  }
}
```

---

### Task Checkout (3 tools)

#### 43. `checkout_task`

Acquire an exclusive lock on a task to prevent double-work. Only agents can checkout tasks. If the task is already checked out by another non-expired agent, the call fails with a conflict error including details about the current holder. Same-agent re-checkout is idempotent and returns a fresh token.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID to lock |
| `ttl_minutes` | number | No | `15` | Lock duration in minutes (1-240) |

**Example request:**
```json
{
  "name": "checkout_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "ttl_minutes": 30
  }
}
```

**Example response:**
```json
{
  "task_id": "a1b2c3d4-...",
  "checkout_token": "f1e2d3c4-...",
  "checked_out_by": "684bd684-...",
  "expires_at": "2026-03-10T12:30:00Z"
}
```

**Example conflict response (409):**
```json
{
  "code": 409,
  "message": "Task is already checked out",
  "details": {
    "checked_out_by": "other-agent-uuid",
    "expires_at": "2026-03-10T12:15:00Z"
  }
}
```

---

#### 44. `release_task`

Release an exclusive checkout. The `checkout_token` from the checkout response must be provided. Agents should release checkouts when done working on a task to allow others to pick it up immediately rather than waiting for TTL expiry.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `checkout_token` | string | **Yes** | -- | Token returned by `checkout_task` |

**Example request:**
```json
{
  "name": "release_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "checkout_token": "f1e2d3c4-..."
  }
}
```

**Example response:**
```json
{
  "status": "released"
}
```

---

#### 45. `extend_checkout`

Extend the TTL of an active checkout. Use this when a task takes longer than expected. The checkout must not have expired already.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `checkout_token` | string | **Yes** | -- | Token returned by `checkout_task` |
| `ttl_minutes` | number | No | `15` | New TTL in minutes from now (1-240) |

**Example request:**
```json
{
  "name": "extend_checkout",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "checkout_token": "f1e2d3c4-...",
    "ttl_minutes": 60
  }
}
```

**Example response:**
```json
{
  "task_id": "a1b2c3d4-...",
  "checkout_token": "f1e2d3c4-...",
  "checked_out_by": "684bd684-...",
  "expires_at": "2026-03-10T13:00:00Z"
}
```

---

## Error Handling

All tools return errors in a consistent format:

```json
{
  "isError": true,
  "content": [
    {
      "type": "text",
      "text": "error: invalid task_id: UUID must be a valid UUID"
    }
  ]
}
```

Common error conditions:
- **Invalid UUID** -- parameter is not a valid UUID format
- **Not found** -- referenced entity does not exist
- **Permission denied** -- agent lacks access to the workspace/project
- **Validation error** -- required field missing or invalid value

## Authentication

MCP tools authenticate using the `MESH_AGENT_KEY` environment variable (stdio mode) or
per-connection HTTP headers/query parameters (SSE mode). The key format is:

```
agk_{workspace_slug}_{random_string}
```

The agent key is generated when registering an agent through the REST API or the web UI.
It is shown only once at creation time -- store it securely.

To regenerate a lost key, use `POST /api/v1/agents/{agent_id}/regenerate-key` via the REST API.
