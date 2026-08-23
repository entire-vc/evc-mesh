# Quickstart Guide

Get evc-mesh running in 5 minutes.

---

## Step 1: Start Infrastructure

```bash
cd /path/to/evc-mesh
cd deploy/docker/mesh
docker compose up -d
# or from the repo root: make docker-up
```

This starts PostgreSQL, Redis, NATS, and MinIO. Wait for all containers to be healthy:

```bash
cd /path/to/evc-mesh/deploy/docker/mesh
docker compose ps
```

All four services should show `healthy` status.

---

## Step 2: Start the API Server

```bash
go run ./cmd/api
```

You should see output like:
```
Connected to PostgreSQL
Database migrations applied
[eventbus] Connected to NATS at nats://localhost:4223
WebSocket hub started
Starting evc-mesh API server on 0.0.0.0:8005
```

Run it from the repo root — migrations are resolved relative to the working
directory, and elsewhere it exits with `migrations directory does not exist`.

Verify: `curl http://localhost:8005/health`

---

## Step 3: Start the Frontend

In a new terminal:

```bash
cd web
pnpm install
pnpm dev
```

The frontend starts at http://localhost:3000.

---

## Step 4: Create Your First Account

A fresh install has **no users and no default password** — the first account is the
one you create. There is nothing to log in with until you do.

1. Open http://localhost:3000
2. Click **Create an account** (or go straight to http://localhost:3000/register)
3. Register with your email and a password of 8+ characters containing an
   uppercase letter, a lowercase letter, and a digit
4. You are logged in, and a workspace has been created for you
5. Create a **project** (e.g. "Backend API")
6. Add some **tasks** via the kanban board

> **Trying to log in before registering returns `401 invalid email or password`.**
> That is expected on a fresh database: the account does not exist yet. Register
> first. If you would rather have the server create the admin for you (useful for
> scripted or headless installs), see
> [Seeding the first admin](self-hosting.md#seeding-the-first-admin).

---

## Step 5: Connect Claude Code via MCP

### 5.1 Register an Agent

In the web UI:
1. Open your workspace **Org Chart** (`/w/<workspace-slug>/org-chart`)
2. Click **Register Agent**
3. Choose a name (e.g. "claude-code") and type "claude_code"
4. **Copy the API key** -- it is shown only once!

The key looks like: `agk_my-team_a1b2c3d4e5f6...`, where the middle segment is
the workspace slug.

Creating one over the API instead, or connecting over SSE rather than stdio? See
[Agent Onboarding](agent-onboarding.md).

### 5.2 Add the Agent to Your Project

**Do not skip this.** Registering an agent puts it in the *workspace*; it does not
give it access to any *project*. Project membership is separate and is not granted
automatically, so a freshly registered agent sees an empty board:

```
list_projects  ->  {"items": [], "total_count": 0}
create_task    ->  Forbidden: agent is not a member of this project
```

That is the expected response for a non-member, not a broken key or a bad MCP
config — the agent is authenticating fine and is simply not on the project yet.

In the web UI: open the project, **Members** -> **Add agent**, pick the agent and a
role. Or over the API:

```bash
curl -X POST http://localhost:8080/api/v1/projects/<project_id>/members/agents \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "<agent_id>", "role": "member"}'
```

`role` must be one of `admin`, `member`, `viewer` — `member` is the usual choice
for an agent that creates and updates tasks. There is no `agent` role: "agent" is
an actor type (authenticated by API key), not a role you assign here.

Repeat for each project the agent should work in.

### 5.3 Configure MCP

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "evc-mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_API_URL": "http://localhost:8005",
        "MESH_AGENT_KEY": "agk_my-team_your-key-here"
      }
    }
  }
}
```

Or, if you built the binary:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "/path/to/mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_API_URL": "http://localhost:8005",
        "MESH_AGENT_KEY": "agk_my-team_your-key-here"
      }
    }
  }
}
```

### 5.4 Test the Connection

Ask Claude Code to:
- "List my projects" -- should return the project you created
- "Create a task titled 'Hello from Claude'" -- should create a task in the kanban board

---

## Step 6: Verify End-to-End

Test that the UI and MCP are in sync:

1. **UI to MCP:** Create a task via the web UI, then ask Claude `list_tasks` -- the task should appear.

2. **MCP to UI:** Ask Claude to `create_task` with a title, then refresh the kanban board -- the task should be visible.

3. **Real-time updates:** Move a task in the UI and watch the WebSocket deliver the update. Other connected clients (or a future SSE-based MCP) see changes instantly.

4. **Event bus:** Ask Claude to `publish_summary` after completing work. The summary appears in the project's event feed.

---

## What Can Claude Do?

With evc-mesh connected, Claude Code can:

| Action | MCP Tool | Example Prompt |
|--------|----------|----------------|
| See all projects | `list_projects` | "What projects do we have?" |
| Read task details | `get_task` | "Show me task #abc with comments" |
| Create tasks | `create_task` | "Create a task to add rate limiting" |
| Move tasks through workflow | `move_task` | "Mark this task as done" |
| Break work into subtasks | `create_subtask` | "Break this into smaller subtasks" |
| Leave progress notes | `add_comment` | "Add a comment with my progress" |
| Upload files and reports | `upload_artifact` | "Attach the test results" |
| Share context with other agents | `publish_summary` | "Publish a summary of what I did" |
| Read other agents' context | `get_context` | "What has happened in this project today?" |
| Self-assign work | `assign_task` | "Assign this task to me" |
| See own task queue | `get_my_tasks` | "What tasks are assigned to me?" |
| Report errors | `report_error` | "Report that the API endpoint is failing" |

---

## Next Steps

- Read [Agent Onboarding](agent-onboarding.md) for agent keys, the SSE transport, and running MCP behind a reverse proxy
- Read [Self-Hosting Guide](self-hosting.md) for production deployment, backup, and security hardening
- Read [MCP Tool Reference](mcp-reference.md) for detailed documentation on all 49 MCP tools
- Read the [OpenAPI spec](openapi.yaml) for the full REST API specification
- Set up multiple agents to explore multi-agent collaboration via the event bus
