# Agent Onboarding

How to point an AI agent at a self-hosted Mesh instance: issue an agent key,
connect over stdio or SSE, and confirm the connection actually works.

This is the operator-facing path. For the tool catalogue see
[mcp-reference.md](mcp-reference.md); for API-level auth details see
[api-authentication.md](api-authentication.md).

**Prerequisites:** a running instance and an account you can log into. If you do
not have one yet, start at [self-hosting.md](self-hosting.md#seeding-the-first-admin) —
a fresh install has no users, and every login returns `401` until it does.

---

## 1. Issue an agent key

An agent is a member of exactly one workspace and authenticates with a key that
looks like this:

```
agk_ws-68fcb656_ea7838dcf000363031329c5571a47a24ac8553265014eae4
    └────┬─────┘ └──────────────────┬───────────────────────────┘
   workspace slug          192 bits of randomness
```

The middle segment is the workspace's slug — for the workspace created with your
first account that is `ws-<first 8 chars of your user id>`, which is why keys on
a fresh install start `agk_ws-`. Rename the workspace and new keys pick up the
new slug; existing keys keep working.

Only **owner** and **admin** can create one. Agents cannot mint agent keys, by
design.

### From the web UI

Open your workspace → **Org Chart** (`/w/<workspace-slug>/org-chart`) →
**Register Agent**. The key is displayed **once**, at creation. Copy it then;
only a bcrypt hash and an 8-character lookup prefix are stored, so nobody —
including you — can read it back afterwards. Lost it? Rotate (see
[Rotating and revoking](#5-rotating-and-revoking)).

### From the API

```bash
# Log in and keep the access token.
TOKEN=$(curl -s -X POST https://mesh.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<your-password>"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["tokens"]["access_token"])')

# Find the workspace id.
curl -s https://mesh.example.com/api/v1/workspaces \
  -H "Authorization: Bearer $TOKEN"

# Register the agent. The key is in the response and is never shown again.
curl -s -X POST https://mesh.example.com/api/v1/workspaces/<ws_id>/agents \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-agent","agent_type":"claude_code","capabilities":{}}'
```

Response:

```json
{
  "agent": { "id": "fe9e3a88-…", "name": "my-agent", "agent_type": "claude_code", "api_key_prefix": "ea7838dc", … },
  "api_key": "agk_ws-68fcb656_ea7838dc…"
}
```

Field names that catch people out: the request field is **`agent_type`**, not
`type`, and **`capabilities` is an object**, not an array. A JSON array is
rejected with `400`; a `type` key is silently ignored and you get an agent with
an empty type.

Check the key before wiring it into anything:

```bash
curl -s https://mesh.example.com/api/v1/agents/me -H "X-Agent-Key: agk_…"
```

That returns the agent's own record, or `401` if the key is wrong.

### 1.1 Add the agent to each project it works in

A valid key is not enough. Registering an agent places it in the **workspace**;
membership of a **project** is separate and is never granted automatically. Until
you add it, every tool call is correctly authenticated and still returns nothing
useful:

```
list_projects  ->  {"items": [], "total_count": 0}
create_task    ->  Forbidden: agent is not a member of this project
```

Web UI: open the project, **Members** -> **Add agent**. Over the API:

```bash
curl -X POST https://mesh.example.com/api/v1/projects/<project_id>/members/agents \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "<agent_id>", "role": "member"}'
```

`role` is one of `admin`, `member`, `viewer`; `member` suits an agent that creates
and updates tasks. **There is no `agent` role** — "agent" is an actor type,
authenticated by API key, with its own fixed permission set. Passing
`"role": "agent"` returns `400 role must be one of: admin, member, viewer`.

This endpoint needs a **user** JWT with permission to manage members; an agent key
cannot add itself.

---

## 2. Connect

Two transports. Pick **stdio** when the agent runs on a machine that can reach
the Mesh API directly; pick **SSE** when it cannot, or when several agents share
one MCP server.

### stdio — one agent, local process

The client spawns `mesh-mcp` and talks JSON-RPC over its stdin/stdout. The key
comes from the environment and is required — without `MESH_AGENT_KEY` the
process exits immediately.

Build the binary once:

```bash
go build -o ~/bin/mesh-mcp ./cmd/mcp
```

Minimal working MCP client config (`.mcp.json` for Claude Code, same shape for
Cline and other MCP clients):

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "/home/you/bin/mesh-mcp",
      "env": {
        "MESH_API_URL": "https://mesh.example.com",
        "MESH_AGENT_KEY": "agk_ws-68fcb656_…"
      }
    }
  }
}
```

`MESH_API_URL` is the **API** base URL, with no `/api/v1` suffix — the client
appends that itself. It defaults to `http://localhost:8005`, which is right for
a local dev instance and wrong for everything else.

Confirm it works before involving the agent:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
| MESH_API_URL=https://mesh.example.com MESH_AGENT_KEY=agk_… mesh-mcp
```

On success stderr shows `Authenticated as agent: my-agent (…)` and stdout
carries a `tools/list` result with 49 tools.

### SSE — remote agents, several at once

One `mesh-mcp` process serves many agents; each connection authenticates on its
own. This is the `mcp` service in `docker-compose.prod.yml`, listening on
`${MCP_PORT:-8081}`.

| Endpoint | Profile | Tools |
|----------|---------|-------|
| `/sse` | full | 49 |
| `/core/sse` | core | 21 |

Three accepted ways to present the key, first match wins:

```
Authorization: Bearer agk_…      ← preferred
X-Agent-Key: agk_…
?agent_key=agk_…                 ← avoid: puts a year-lived credential in
                                   URLs, proxy access logs and Referer headers
```

Client config:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "type": "sse",
      "url": "https://mesh.example.com/mcp/sse",
      "headers": { "Authorization": "Bearer agk_ws-68fcb656_…" }
    }
  }
}
```

Missing key → `401`. Key present but invalid → `403`.

---

## 3. Verify the connection

Watch the handshake directly. This is the quickest way to tell "the port is
closed" apart from "the port is open and the server told the client to go
somewhere useless" — the second is what a misconfigured proxy looks like.

```bash
curl -N https://mesh.example.com/mcp/sse -H "Authorization: Bearer agk_…"
```

Expected first frame:

```
event: endpoint
data: /message?sessionId=720bad23-…
```

The client resolves that against the URL it connected to and POSTs every
subsequent message there. A relative path is normal and correct.

**If `data:` contains an absolute URL, check it is one your client can reach.**
An endpoint of `http://0.0.0.0:8081/message` means the server is advertising its
own listen address; `0.0.0.0` is a wildcard bind, not a destination, and the
client has nowhere to send messages. Versions before this one did exactly that,
which made a correctly published MCP port look dead from outside while working
fine from inside the host. The fix is to upgrade; the workaround is
`MESH_MCP_PUBLIC_URL` below.

Full round trip, if you want to see a tool actually run:

```bash
SSE=https://mesh.example.com/mcp/sse
KEY=agk_…
# 1. Read the endpoint from the SSE stream (first frame, above).
# 2. POST to it — the reply comes back on the SSE stream, not in the POST body.
curl -s -o /dev/null -w '%{http_code}\n' \
  -X POST "https://mesh.example.com/mcp/message?sessionId=<from-step-1>" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_my_tasks","arguments":{}}}'
```

`202` means accepted; the result arrives on the open SSE connection.

---

## 4. Behind a reverse proxy

The bundled nginx (`deploy/docker/mesh/nginx.conf`) proxies `/api/`, `/ws` and
`/health` only — **it does not proxy MCP**. The Integrations page in the web UI
suggests `https://<your-host>/mcp/sse`; on a stock install that path hits the
SPA fallback and returns HTML. Either connect straight to `${MCP_PORT:-8081}`,
or add the route to whichever proxy you actually run.

Serving MCP under a path prefix needs both halves:

1. A proxy route that strips the prefix:

   ```nginx
   location /mcp/ {
       proxy_pass http://mcp:8081/;   # trailing slash strips the /mcp prefix
       proxy_http_version 1.1;
       proxy_set_header Connection "";
       proxy_buffering off;           # required: SSE must not be buffered
       proxy_read_timeout 3600s;
   }
   ```

   ```
   # Caddy
   handle_path /mcp/* {
       reverse_proxy mcp:8081
   }
   ```

2. `MESH_MCP_PUBLIC_URL` so the advertised endpoint includes the prefix:

   ```bash
   # deploy/docker/mesh/.env
   MESH_MCP_PUBLIC_URL=https://mesh.example.com/mcp
   ```

   Without it the server advertises `/message`, the client resolves that to
   `https://mesh.example.com/message`, and the proxy — which only knows about
   `/mcp/` — has never heard of it. With it, the advertised endpoint is
   `https://mesh.example.com/mcp/message`, which the proxy strips back to
   `/message` upstream.

Serving MCP on its own hostname or port instead? Leave `MESH_MCP_PUBLIC_URL`
empty. Relative endpoints resolve correctly on their own; the variable exists
for path prefixes and for clients that refuse relative endpoints.

`proxy_buffering off` is not optional. A buffering proxy holds the SSE stream
until it has enough bytes to flush, and the connection looks like it hangs at
the handshake.

---

## 5. Rotating and revoking

```bash
curl -X POST https://mesh.example.com/api/v1/agents/<agent_id>/regenerate-key \
  -H "Authorization: Bearer $TOKEN"
```

Returns a new `api_key` and invalidates the old one. Owner/admin only. In the UI
it is on the agent's detail dialog in the Org Chart.

Two caveats:

- Keys expire **365 days** after issue. Nothing warns you beforehand; the agent
  simply starts getting `401`.
- The SSE server caches authenticated sessions in memory for **30 minutes of
  idle time**. A rotated or deleted key keeps working on an established SSE
  connection until its cache entry ages out. Restart the `mcp` service if you
  need a revocation to take effect immediately.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `MESH_AGENT_KEY environment variable is required for stdio mode` | stdio mode without a key | Set `MESH_AGENT_KEY` in the client's `env` block |
| `Agent authentication failed` at stdio startup | Wrong key, wrong `MESH_API_URL`, or API unreachable | `curl $MESH_API_URL/api/v1/agents/me -H "X-Agent-Key: agk_…"` |
| `401` on `/sse` | No key on the request | Send `Authorization: Bearer agk_…` |
| `403` on `/sse` | Key present but rejected | Key rotated, deleted, or expired (365 days) — issue a new one |
| SSE connects, then nothing happens | Proxy buffering the stream | `proxy_buffering off` |
| Client posts to `0.0.0.0` or to a 404 | Advertised endpoint does not match the client's route | Upgrade; set `MESH_MCP_PUBLIC_URL` if MCP sits under a path prefix |
| `https://<host>/mcp/sse` returns HTML | Bundled nginx does not proxy MCP | Connect to port `8081`, or add the route (§4) |
| `list_projects` returns `[]` and `create_task` says `agent is not a member of this project` | The key works; the agent is in the workspace but not on the project | Add it to each project it must work in (§1.1) |
| Fewer tools than expected | Connected to the core profile | Use `/sse`, or unset `MESH_MCP_PROFILE` for stdio |
| `Invalid transport "…"` | Typo in `MESH_MCP_TRANSPORT` | Only `stdio` and `sse` are valid |

---

## Related

- [self-hosting.md](self-hosting.md) — environment variable reference, first
  admin, security checklist
- [mcp-reference.md](mcp-reference.md) — the tool catalogue
- [api-authentication.md](api-authentication.md) — JWT and agent-key auth at the
  REST level
