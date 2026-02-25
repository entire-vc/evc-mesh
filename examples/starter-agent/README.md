# Starter Agent Example

A minimal Go agent for Entire VC Mesh demonstrating the full lifecycle:
registration, heartbeat, task processing, comments, and event bus publishing.

## Quick Start

### 1. Register an agent and get an API key

Agents authenticate with `X-Agent-Key` headers using keys in the format
`agk_{workspace_slug}_{random}`. You register an agent through the REST API or
the Agent Dashboard in the web UI.

**Via REST API:**

```bash
curl -s -X POST http://localhost:8005/api/v1/workspaces/<ws_id>/agents \
  -H "Authorization: Bearer <your_jwt>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-starter-agent",
    "agent_type": "custom",
    "capabilities": {"roles": ["task-runner"]}
  }'
```

The response includes the plain-text `api_key` — store it securely, it is shown only once.

**Via Agent Dashboard:**

Open the web UI, go to the Agent Dashboard, click "Register Agent", fill in the
name and type. Copy the displayed key.

### 2. Set environment variables

```bash
export MESH_API_URL=http://localhost:8005
export MESH_AGENT_KEY=agk_myworkspace_abc123xyz
```

### 3. Run the example

```bash
# From the repo root
go run ./examples/starter-agent
```

## Example Workflow

The `main.go` agent performs these steps:

1. **Authenticate** — `sdk.New()` calls `GET /agents/me` to validate the key
   and discover workspace + agent IDs automatically.
2. **Heartbeat** — `POST /agents/heartbeat` updates `last_heartbeat` and sets
   `status=online` in the dashboard.
3. **Fetch assigned tasks** — `GET /agents/me/tasks` returns tasks where this
   agent is the assignee.
4. **Add a comment** — `POST /tasks/:id/comments` logs work progress. Use
   `is_internal=true` for agent-only notes not shown to humans by default.
5. **Publish a summary event** — `POST /projects/:id/events` broadcasts a
   structured event to the project event bus so other agents can pick up
   context without polling comments.

## Sub-agent Orchestration Pattern

`orchestrator.go` shows the **orchestration pattern**:

```
Orchestrator agent (main agent)
  ├── Registers child agents via POST /workspaces/:id/agents
  ├── Creates subtasks via POST /tasks/:id/subtasks
  ├── Assigns subtasks to child agents via POST /tasks/:id/assign
  ├── Publishes dispatch event to event bus
  ├── Polls GET /tasks/:id until all subtasks reach done/cancelled category
  └── Publishes consolidated summary event
```

Child agents receive their task via `GET /agents/me/tasks`, do their work, move
the task to done via `POST /tasks/:id/move`, and post comments. The orchestrator
detects completion by checking the task status category (`done` or `cancelled`).

## SDK Reference

All API calls go through `pkg/sdk`. Key methods:

| Method | REST endpoint | Description |
|--------|---------------|-------------|
| `sdk.New(url, key)` | `GET /agents/me` | Authenticate and get workspace/agent IDs |
| `client.Me(ctx)` | `GET /agents/me` | Get current agent profile |
| `client.Heartbeat(ctx)` | `POST /agents/heartbeat` | Signal liveness |
| `client.GetMyTasks(ctx)` | `GET /agents/me/tasks` | Tasks assigned to this agent |
| `client.ListTasks(ctx, projID, ...)` | `GET /projects/:id/tasks` | All project tasks with filters |
| `client.GetTask(ctx, taskID)` | `GET /tasks/:id` | Single task |
| `client.UpdateTask(ctx, taskID, input)` | `PATCH /tasks/:id` | Update title, description, priority |
| `client.MoveTask(ctx, taskID, statusID)` | `POST /tasks/:id/move` | Move to a different status column |
| `client.AssignTask(ctx, taskID, agentID, "agent")` | `POST /tasks/:id/assign` | Assign to user or agent |
| `client.CreateSubtask(ctx, parentID, input)` | `POST /tasks/:id/subtasks` | Create a child task |
| `client.AddComment(ctx, taskID, body, isInternal)` | `POST /tasks/:id/comments` | Add comment (public or internal) |
| `client.PublishEvent(ctx, projID, input)` | `POST /projects/:id/events` | Publish to event bus |
| `client.GetContext(ctx, projID, opts...)` | `GET /projects/:id/events` | Read event bus context |
| `client.RegisterSubAgent(ctx, input)` | `POST /workspaces/:id/agents` | Spawn a child agent |
| `client.ListStatuses(ctx, projID)` | `GET /projects/:id/statuses` | Get status list with categories |

### Status categories

Statuses are customizable per project. Use the **category** (not the name) for
agent logic — categories are stable across any project:

| Category | Meaning |
|----------|---------|
| `backlog` | Not yet started |
| `todo` | Ready to start |
| `in_progress` | Work in progress |
| `review` | Awaiting review |
| `done` | Completed |
| `cancelled` | Will not be done |

### Event types

| Event type | When to use |
|------------|-------------|
| `summary` | Work completed — share output with other agents |
| `context_update` | Intermediate progress or discovered information |
| `status_change` | Notifying about a task status transition |
| `error` | Reporting a failure for other agents to react |
| `dependency_resolved` | A blocking dependency has been cleared |
| `custom` | Any other structured message |

## MCP Alternative

If your agent framework supports MCP (Model Context Protocol), you can connect
to the Mesh MCP server instead of using the REST SDK. The MCP server exposes
outcome-oriented tools like `move_task`, `add_comment`, `publish_summary`, and
`get_project_context`.

```bash
# Connect via stdio (for Claude Code, Cline, Aider)
go run ./cmd/mcp

# Connect via HTTP SSE (for browser-based or remote agents)
# Endpoint: http://localhost:8081/sse
```

See `docs/mcp-reference.md` for the full MCP tool reference.
