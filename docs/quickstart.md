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
Starting EVC Mesh API on 0.0.0.0:8005
Database connected
NATS JetStream connected
MinIO connected, bucket mesh-artifacts ready
WebSocket hub started
```

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

## Step 4: Register and Create Your Workspace

1. Open http://localhost:3000
2. Log in with the default admin account: `admin@localhost` / `Admin123` (or register a new one)
3. Create a **workspace** (e.g. "My Team")
4. Create a **project** (e.g. "Backend API")
5. Add some **tasks** via the kanban board

---

## Step 5: Connect Claude Code via MCP

### 5.1 Register an Agent

In the web UI:
1. Navigate to your workspace **Settings > Agents** tab
2. Click **Register Agent**
3. Choose a name (e.g. "claude-code") and type "claude_code"
4. **Copy the API key** -- it is shown only once!

The key looks like: `agk_my-team_a1b2c3d4e5f6...`

### 5.2 Configure MCP

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "go",
      "args": ["run", "./cmd/mcp"],
      "cwd": "/path/to/evc-mesh",
      "env": {
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
      "command": "/path/to/evc-mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_AGENT_KEY": "agk_my-team_your-key-here"
      }
    }
  }
}
```

### 5.3 Test the Connection

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

- Read [Self-Hosting Guide](self-hosting.md) for production deployment, backup, and security hardening
- Read [MCP Tool Reference](mcp-reference.md) for detailed documentation on all 45 MCP tools
- Read the [OpenAPI spec](openapi.yaml) or visit `http://localhost:8005/docs` for the full REST API reference
- Set up multiple agents to explore multi-agent collaboration via the event bus
