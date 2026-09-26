# Agent Onboarding

How to point an AI agent at a self-hosted Mesh instance: issue an agent key,
connect over stdio or SSE (or let an MCP client sign in with OAuth over
Streamable HTTP), and confirm the connection actually works.

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

Pick **stdio** when the agent runs on a machine that can reach the Mesh API
directly; pick **SSE** when it cannot, or when several agents share one MCP
server. MCP clients that expect to be given only a URL and to sign in by
themselves use **Streamable HTTP with OAuth** — no agent key to copy at all.

### stdio — one agent, local process

The client spawns `mesh-mcp` and talks JSON-RPC over its stdin/stdout. The key
comes from the environment and is required — without `MESH_AGENT_KEY` the
process exits immediately.

Install the binary once. It comes from its own module, not from this
repository — pin a commit instead of `@latest` if you want a reproducible
install:

```bash
GOBIN=~/bin go install github.com/entire-vc/evc-mesh-mcp@latest
# installs ~/bin/evc-mesh-mcp
```

Minimal working MCP client config (`.mcp.json` for Claude Code, same shape for
Cline and other MCP clients):

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "/home/you/bin/evc-mesh-mcp",
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
| MESH_API_URL=https://mesh.example.com MESH_AGENT_KEY=agk_… evc-mesh-mcp
```

On success stderr shows `Authenticated as agent: my-agent (…)` and stdout
carries a `tools/list` result with 63 tools (25 on `MESH_MCP_PROFILE=core`).

### SSE — remote agents, several at once

One `mesh-mcp` process serves many agents; each connection authenticates on its
own. This is the `mcp` service in `docker-compose.prod.yml`, listening on
`${MCP_PORT:-8081}`, published on loopback by default (`MCP_BIND`) — remote
agents connect through the bundled nginx at `/mcp/` (below), not to that port.

| Endpoint | Profile | Tools |
|----------|---------|-------|
| `/sse` | full | 63 |
| `/core/sse` | core | 25 |

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

### Streamable HTTP with OAuth — clients that sign in on their own

The same `mcp` service also serves the MCP Streamable HTTP transport, and it
accepts an OAuth access token (`mot_…`) in place of an agent key. You give the
client one URL; it discovers the rest, opens a browser for the user to approve
the connection, and gets a token of its own.

| Public URL (bundled nginx) | Profile |
|----------------------------|---------|
| `https://<your-host>/mcp` | full |
| `https://<your-host>/mcp/core` | core |

What the client does with that URL, and what has to be reachable for it to work:

1. `POST /mcp` without a credential → `401` with
   `WWW-Authenticate: Bearer resource_metadata="https://<your-host>/.well-known/oauth-protected-resource/mcp", scope="mesh"`.
2. `GET /.well-known/oauth-protected-resource/mcp` (served by `mcp`) → JSON
   whose `authorization_servers` is the instance origin.
3. `GET /.well-known/oauth-authorization-server` (served by `api`) → RFC 8414
   metadata; `issuer` is `MESH_BASE_URL`.
4. `POST /oauth/register` (dynamic client registration) — or the client uses
   an `https://` URL as its `client_id` (Client ID Metadata Document) and skips
   this step.
5. `GET /oauth/authorize` → `302` to the web UI's consent screen
   `/connect/consent`, where the signed-in user picks a workspace and approves.
6. `POST /oauth/token` → `mot_…` access token plus a refresh token.
7. `POST /mcp` with `Authorization: Bearer mot_…` → normal MCP traffic.

An agent key still works on the same endpoints (`Authorization: Bearer agk_…`
or `X-Agent-Key`, headers only — never the query string here). A rejected
agent key is `403`; a missing credential or a rejected OAuth token is `401`
with the `resource_metadata` challenge above, which is what tells an OAuth
client to start (or restart) the flow.

Every URL in steps 1–7 is on the instance origin, and none of them is under
`/mcp/` or `/api/`. The bundled nginx routes all of them; on your own proxy
you have to add them yourself (§4) — otherwise the client gets the web UI's
`index.html` with `200` and reports "not JSON" or "no OAuth support".

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

The bundled nginx (`deploy/docker/mesh/nginx.conf`) proxies `/mcp/`, exact
`/mcp` and the OAuth routes (§2, Streamable HTTP) under the same origin as the
web UI, alongside `/api/`, `/ws` and `/health` — no extra configuration needed
on a stock `docker compose` install. `MESH_MCP_PUBLIC_URL`
defaults to `${MESH_BASE_URL}/mcp` in `docker-compose.prod.yml`, so setting
`MESH_BASE_URL` (which you already do for invite links) is enough to make
`https://<your-host>/mcp/sse` — the address the Integrations page in the web UI
shows you — actually work.

Running your own reverse proxy instead of the bundled one, or serving MCP under
a path prefix on infrastructure you built by hand? Both halves below are what
the bundled setup does for you automatically; do them yourself:

1. A proxy route that strips the prefix:

   ```nginx
   location /mcp/ {
       # A literal upstream (proxy_pass http://mcp:8081/;) is simpler and
       # fine if MCP is a hard dependency of your setup — nginx resolves it
       # once at config load and refuses to start if it's down. The bundled
       # nginx uses a resolver + variable instead, because it treats mcp as
       # optional and must not take the web UI down with it (see
       # deploy/docker/mesh/nginx.conf for the full form). Use whichever
       # matches how you actually run mcp.
       proxy_pass http://mcp:8081/;   # trailing slash strips the /mcp prefix
       proxy_http_version 1.1;
       proxy_set_header Host $host;
       proxy_set_header Connection "";
       proxy_buffering off;           # required: SSE must not be buffered
       proxy_read_timeout 3600s;
   }
   ```

   ```
   # Caddy
   handle_path /mcp/* {
       reverse_proxy mcp:8081 {
           header_up Host localhost   # see the DNS-rebinding note below
       }
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

3. The Streamable HTTP endpoint and the OAuth routes, if remote clients should
   be able to sign in (§2). None of these may strip a prefix: `mcp` and `api`
   match on the full path.

   | Path | Match | Upstream |
   |------|-------|----------|
   | `/mcp` | exact | `mcp` (full-profile Streamable HTTP; `/mcp/core` is already covered by the `/mcp/` route above) |
   | `/.well-known/oauth-protected-resource` | prefix | `mcp` |
   | `/.well-known/oauth-authorization-server` | prefix | `api` |
   | `/oauth/` | prefix | `api` (`register`, `authorize`, `token`, `revoke`) |

   The consent screen `/connect/consent` is a web UI route — leave it on the
   SPA. The prefix match on the authorization-server metadata also catches the
   RFC 8414 path-suffixed form some clients probe, so they get the API's JSON
   `404` rather than an HTML page.

   ```nginx
   # X-Forwarded-For is overwritten with the connecting address, never appended
   # to: an appended header keeps whatever the client sent, and the services key
   # their rate limits on it (see docs/self-hosting.md, client IP).
   # Same buffering rules as /mcp/ — Streamable HTTP can answer with an SSE stream.
   location = /mcp {
       proxy_pass http://mcp:8081;    # no URI part: the path is passed unchanged
       proxy_http_version 1.1;
       proxy_set_header Host $host;
       proxy_set_header X-Real-IP $remote_addr;
       proxy_set_header X-Forwarded-For $remote_addr;
       proxy_set_header X-Forwarded-Proto $scheme;
       proxy_set_header Connection "";
       proxy_buffering off;
       proxy_read_timeout 3600s;
   }
   location ^~ /.well-known/oauth-protected-resource {
       proxy_pass http://mcp:8081;
       proxy_set_header Host $host;
       proxy_set_header X-Real-IP $remote_addr;
       proxy_set_header X-Forwarded-For $remote_addr;
       proxy_set_header X-Forwarded-Proto $scheme;
   }
   location ^~ /.well-known/oauth-authorization-server {
       proxy_pass http://api:8005;
       proxy_set_header Host $host;
       proxy_set_header X-Real-IP $remote_addr;
       proxy_set_header X-Forwarded-For $remote_addr;
       proxy_set_header X-Forwarded-Proto $scheme;
   }
   location ^~ /oauth/ {
       proxy_pass http://api:8005;
       proxy_set_header Host $host;
       proxy_set_header X-Real-IP $remote_addr;
       proxy_set_header X-Forwarded-For $remote_addr;
       proxy_set_header X-Forwarded-Proto $scheme;
   }
   ```

   ```
   # Caddy — before the SPA catch-all
   handle /mcp {
       reverse_proxy mcp:8081 {
           header_up Host localhost
       }
   }
   handle /.well-known/oauth-protected-resource* {
       reverse_proxy mcp:8081 {
           header_up Host localhost
       }
   }
   handle /.well-known/oauth-authorization-server* {
       reverse_proxy api:8005
   }
   handle /oauth/* {
       reverse_proxy api:8005
   }
   ```

   The URLs the client is sent to come from configuration, not from the proxy:
   the resource and its metadata from `MESH_MCP_PUBLIC_URL`, the authorization
   server (`issuer`, every `/oauth/*` endpoint, the consent redirect) from
   `MESH_BASE_URL`. Both must be the public origin the client actually uses.
   If MCP is served from a different origin than the Mesh web UI, set
   `MESH_MCP_OAUTH_ISSUER` on `mcp` to `MESH_BASE_URL` — by default `mcp`
   names its own origin as the authorization server.

   Check it end to end — each line must print JSON, not HTML:

   ```bash
   H=https://mesh.example.com
   curl -si -X POST $H/mcp | grep -i '^www-authenticate'       # resource_metadata="…"
   curl -s $H/.well-known/oauth-protected-resource/mcp          # "authorization_servers"
   curl -s $H/.well-known/oauth-authorization-server            # "issuer"
   curl -s -X POST $H/oauth/token -d grant_type=x               # JSON error, not index.html
   ```

Serving MCP on its own hostname or port instead? Set `MCP_BIND=0.0.0.0` so the
port is reachable from outside the host (it is loopback-only by default; see
[MCP port binding](self-hosting.md#mcp-port-binding) for the trade-off), and
leave `MESH_MCP_PUBLIC_URL`
empty (or unset it — the bundled compose file's default only fires when the
variable is entirely absent). Relative endpoints resolve correctly on their
own; the variable exists for path prefixes and for clients that refuse
relative endpoints.

`proxy_buffering off` is not optional. A buffering proxy holds the SSE stream
until it has enough bytes to flush, and the connection looks like it hangs at
the handshake.

### DNS-rebinding guard and loopback proxies

`mcp-go` ≥ v0.57.0 rejects a request with `403 Forbidden: invalid Host header`
when the **local address of the accepted connection** is loopback and the
`Host` header doesn't say so. It checks the connection's own local address, not
the address MCP is listening on — binding to `0.0.0.0` does not avoid this.

This never fires inside the bundled `docker compose` network: nginx dials `mcp`
by its container IP, which is never loopback. It fires the moment your reverse
proxy runs on the **same host** as `mesh-mcp` and reaches it over `127.0.0.1`
or `localhost` — a manual (non-Docker) install, or a second proxy layer in
front of the bundle. Fix: make the proxy send `Host: localhost` on that route,
same as the Caddy example above (`header_up Host localhost`) — the app never
uses the Host header for anything except this check, so overriding it here is
safe.

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
- OAuth connections (§2) are revoked by the user from the web UI or with
  `POST /oauth/revoke`. The API instance that handled the revoke stops
  accepting the token at once (other API replicas within 15 seconds); `mcp`
  re-checks an accepted OAuth token every `MESH_MCP_OAUTH_CACHE_TTL_SEC`
  (default **60 seconds**), so a revoked token can keep working there for up
  to that long. The two caches stack: on a multi-replica API the worst case is
  about 75 seconds (15 + 60); on a single-replica install it is the 60.

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
| `https://<host>/mcp/sse` returns HTML | Running a custom reverse proxy without the `/mcp/` route, or `MESH_BASE_URL`/`MESH_MCP_PUBLIC_URL` pointing somewhere the proxy doesn't route from | On the bundled nginx, set `MESH_BASE_URL`; on your own proxy, add the route (§4) |
| OAuth client says the server returned HTML / "not JSON", or that it has no OAuth support | `/.well-known/oauth-*`, `/oauth/*` or exact `/mcp` fall through to the web UI on your proxy | Add the routes in §4 item 3, then re-run the `curl` check there |
| OAuth sign-in goes to the wrong host, or `http://` behind TLS | `MESH_BASE_URL` (authorization server) or `MESH_MCP_PUBLIC_URL` (resource) is not the public origin | Set both to the URL clients actually use (§4 item 3) |
| `403 Forbidden: invalid Host header` on `/mcp/sse` | DNS-rebinding guard — proxy reaches `mesh-mcp` over loopback with a non-`localhost` Host | Set `Host: localhost` on that route (§4) |
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
