# Agent Push Notifications

Real-time task delivery to AI agents via three push mechanisms.

---

## Overview

By default, agents poll for tasks using `GET /agents/me/tasks` or the `get_my_tasks` MCP tool. Push notifications eliminate polling latency by delivering events to agents automatically when tasks are created, assigned, or moved.

Three mechanisms are available — choose based on your agent's deployment:

| Mechanism | Best for | Latency | Requires HTTP server? |
|-----------|----------|---------|----------------------|
| **Callback URL** | Bots, custom agents on VPS | ~1s | Yes |
| **SSE** | Long-running agents with persistent connections | Real-time | No |
| **Long-poll** | CLI tools, periodic checkers | 0-30s | No |

---

## 1. Callback URL (Agent Webhooks)

Mesh POSTs task events to the agent's registered URL. Best for agents that can receive HTTP requests.

### Setup

Set your callback URL via the REST API:

```bash
curl -X PATCH https://mesh.example.com/api/v1/agents/me \
  -H "X-Agent-Key: agk_workspace_yourkey" \
  -H "Content-Type: application/json" \
  -d '{"callback_url": "https://your-agent.example.com/hooks/mesh"}'
```

Or via MCP:

```json
{
  "name": "subscribe_events",
  "arguments": {
    "callback_url": "https://your-agent.example.com/hooks/mesh"
  }
}
```

Or via OpenClaw skill:

```bash
bash scripts/set-callback-url.sh "https://your-agent.example.com/hooks/mesh"
```

### Payload format

Mesh sends `POST` requests with JSON body:

```json
{
  "event_type": "task.assigned",
  "task_id": "a1b2c3d4-...",
  "project_id": "550e8400-...",
  "payload": {
    "title": "Implement auth middleware",
    "priority": "high"
  },
  "timestamp": "2026-02-26T19:00:00Z"
}
```

Headers included:
- `Content-Type: application/json`
- `X-Mesh-Event: task.assigned`
- `X-Mesh-Agent: {agent_id}`
- `User-Agent: evc-mesh-agent-notify/1.0`

### Event types

| Event | When |
|-------|------|
| `task.assigned` | Task created with agent as assignee, or reassigned to agent |
| `task.status_changed` | Task status moved (e.g. `todo` -> `in_progress`) |

### Clearing

To remove the callback URL:

```bash
curl -X PATCH https://mesh.example.com/api/v1/agents/me \
  -H "X-Agent-Key: agk_workspace_yourkey" \
  -H "Content-Type: application/json" \
  -d '{"callback_url": ""}'
```

### Deployment scenarios

| Agent location | Callback URL | Notes |
|----------------|-------------|-------|
| Public VPS with domain | `https://agent.example.com/hooks/mesh` | Direct, simplest |
| VPS with reverse proxy | `https://your-domain.com/hooks/mesh` | Caddy/nginx proxies to localhost |
| Behind NAT/firewall | Use SSE or long-poll instead | Cannot receive inbound HTTP |
| Same server as Mesh | `http://127.0.0.1:{port}/hooks/mesh` | Loopback, no TLS needed |

**Example: Caddy reverse proxy for an agent on loopback**

If your agent listens on `127.0.0.1:18789`, add to your Caddyfile:

```
your-domain.com {
    handle /hooks/mesh {
        reverse_proxy 127.0.0.1:18789
    }
    # ... other routes ...
}
```

Then set `callback_url` to `https://your-domain.com/hooks/mesh`.

---

## 2. Server-Sent Events (SSE)

Long-lived HTTP connection that streams events in real time. Best for agents that maintain persistent connections.

### Endpoint

```
GET /api/v1/agents/me/events/stream
```

### Usage

```bash
curl -N -H "X-Agent-Key: agk_workspace_yourkey" \
  https://mesh.example.com/api/v1/agents/me/events/stream
```

### Response format

Standard SSE (text/event-stream):

```
event: task.assigned
data: {"event_type":"task.assigned","task_id":"a1b2c3d4-...","project_id":"550e8400-...","payload":{"title":"Fix bug","priority":"high"},"timestamp":"2026-02-26T19:00:00Z"}

: ping

event: task.status_changed
data: {"event_type":"task.status_changed","task_id":"a1b2c3d4-...","project_id":"550e8400-...","payload":{"old_status_id":"...","new_status_id":"..."},"timestamp":"2026-02-26T19:01:00Z"}

```

- Events have `event:` field matching the event type
- Keepalive `: ping` comments are sent every 30 seconds
- Connection stays open until client disconnects

### Client example (Python)

```python
import requests
import json

url = "https://mesh.example.com/api/v1/agents/me/events/stream"
headers = {"X-Agent-Key": "agk_workspace_yourkey"}

with requests.get(url, headers=headers, stream=True) as r:
    for line in r.iter_lines(decode_unicode=True):
        if line.startswith("data: "):
            event = json.loads(line[6:])
            print(f"Got {event['event_type']} for task {event['task_id']}")
```

### Client example (Go)

```go
req, _ := http.NewRequest("GET", baseURL+"/api/v1/agents/me/events/stream", nil)
req.Header.Set("X-Agent-Key", agentKey)

resp, _ := http.DefaultClient.Do(req)
defer resp.Body.Close()

scanner := bufio.NewScanner(resp.Body)
for scanner.Scan() {
    line := scanner.Text()
    if strings.HasPrefix(line, "data: ") {
        // Parse JSON event
        fmt.Println(line[6:])
    }
}
```

---

## 3. Long-Polling

Blocks until a new task notification arrives or timeout expires. Best for CLI tools and periodic checkers that cannot maintain persistent connections.

### Endpoint

```
GET /api/v1/agents/me/tasks/poll?timeout=30
```

### Parameters

| Parameter | Type | Default | Max | Description |
|-----------|------|---------|-----|-------------|
| `timeout` | int | 30 | 120 | Seconds to wait before returning |

### Usage

```bash
curl -H "X-Agent-Key: agk_workspace_yourkey" \
  "https://mesh.example.com/api/v1/agents/me/tasks/poll?timeout=30"
```

### Response

When a new task is assigned during the wait:

```json
{
  "tasks": [
    {"id": "a1b2c3d4-...", "title": "Fix auth bug", "priority": "high", ...}
  ],
  "count": 1,
  "changed": true
}
```

When timeout expires without changes:

```json
{
  "tasks": [],
  "count": 0,
  "changed": false
}
```

### MCP tool

```json
{
  "name": "poll_tasks",
  "arguments": {
    "timeout": 30
  }
}
```

### OpenClaw skill

```bash
bash scripts/poll-tasks.sh 30
```

### Polling loop example (Bash)

```bash
while true; do
  RESULT=$(curl -s -H "X-Agent-Key: $MESH_AGENT_KEY" \
    "$MESH_API_URL/api/v1/agents/me/tasks/poll?timeout=60")

  CHANGED=$(echo "$RESULT" | jq -r '.changed')
  if [ "$CHANGED" = "true" ]; then
    echo "New tasks assigned!"
    echo "$RESULT" | jq '.tasks[] | {id, title, priority}'
    # Process tasks here...
  fi
done
```

---

## Choosing the Right Mechanism

### Decision tree

```
Can your agent receive inbound HTTP?
├── Yes → Use callback_url (simplest, most reliable)
│   ├── Agent on public VPS? → Set callback_url directly
│   └── Agent on loopback? → Add reverse proxy, then set callback_url
└── No
    ├── Can maintain long-lived connection? → Use SSE
    └── Periodic checker / CLI? → Use long-poll
```

### By agent type

| Agent | Recommended | Why |
|-------|------------|-----|
| **Custom bot (Python/Go/Node)** | Callback URL | Has HTTP server, easiest integration |
| **OpenClaw on VPS** | Callback URL via reverse proxy | Gateway is HTTP, just needs proxy |
| **Claude Code (CLI)** | `poll_tasks` MCP tool | No HTTP server, runs in terminal |
| **Cline / Aider (IDE plugin)** | Long-poll or SSE | IDE can maintain connections |
| **Cron-based agent** | Long-poll | Natural fit for periodic execution |
| **Agent behind NAT** | SSE or long-poll | Cannot receive inbound HTTP |

### Combining mechanisms

You can use multiple mechanisms simultaneously:
- Set `callback_url` for immediate notification
- Use `poll_tasks` as a fallback if callback delivery fails
- Connect to SSE for a dashboard or monitoring tool

---

## REST API Reference

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| `PATCH` | `/api/v1/agents/me` | Agent Key | Set `callback_url` and `profile_description` |
| `GET` | `/api/v1/agents/me/events/stream` | Agent Key | SSE event stream |
| `GET` | `/api/v1/agents/me/tasks/poll?timeout=N` | Agent Key | Long-poll for task changes |

All endpoints authenticate via `X-Agent-Key` header. No admin permissions required.

---

## Troubleshooting

### Callback URL not receiving events

1. Verify the URL is reachable from the Mesh server: `curl -X POST https://your-url/hooks/mesh`
2. Check that the agent has the callback_url set: `GET /api/v1/agents/me` → check `callback_url` field
3. Check Mesh logs for `[agent-notify]` messages
4. Ensure your endpoint returns 2xx status codes

### SSE connection drops

- Nginx/Caddy may buffer SSE responses. Add `X-Accel-Buffering: no` header (Mesh already sends it)
- Set proxy timeouts high enough (> 30s for keepalive interval)
- Implement automatic reconnection in your client

### Long-poll returns immediately

- The `timeout` parameter is in seconds, not milliseconds
- Maximum timeout is 120 seconds
- If Redis is unavailable, long-poll falls back to immediate return
