# API Authentication

Mesh uses dual authentication — every API endpoint accepts either a **user JWT** or an **agent API key**. This guide covers both mechanisms, role-based access control, and common integration patterns.

## User Authentication (JWT)

### Register

```bash
curl -X POST http://localhost:8005/api/v1/auth/register \
  -H "Content-Type: application/json" \
  --cookie-jar mesh-cookies.txt \
  -d '{
    "email": "user@example.com",
    "password": "SecurePass1",
    "display_name": "Jane Doe"
  }'
```

`--cookie-jar` matters: the refresh token is **not** in the response body. It
comes back only as an httpOnly cookie (see [Token storage](#token-storage)),
and a client that discards cookies will be unable to refresh.

Password requirements:
- Minimum 8 characters
- Maximum 128 characters
- At least one uppercase letter, one lowercase letter, and one digit

Response:

```json
{
  "user": {
    "id": "550e8400-...",
    "email": "user@example.com",
    "display_name": "Jane Doe"
  },
  "tokens": {
    "access_token": "eyJhbGciOiJIUzI1NiIs...",
    "expires_in": 900
  }
}
```

plus a `Set-Cookie` header carrying the refresh token:

```
Set-Cookie: refresh_token=rt_a1b2c3d4e5f6...; Path=/api/v1/auth/refresh;
            Max-Age=604800; HttpOnly; Secure; SameSite=Strict
```

### Login

```bash
curl -X POST http://localhost:8005/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "SecurePass1"
  }'
```

Returns the same response format as register.

### Using the Access Token

Include the JWT in the `Authorization` header:

```bash
curl http://localhost:8005/api/v1/workspaces \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIs..."
```

Access tokens expire after **15 minutes**.

### Refreshing Tokens

The endpoint takes **no request body** — it reads the refresh token from the
httpOnly cookie and rotates it in the response:

```bash
curl -X POST http://localhost:8005/api/v1/auth/refresh \
  --cookie mesh-cookies.txt --cookie-jar mesh-cookies.txt
```

A missing or empty cookie returns `401 missing refresh token`.

Refresh tokens:
- Valid for **7 days**
- **Single-use** — each refresh issues a new token and rotates the cookie
- **Theft detection** — reusing a revoked refresh token revokes ALL user
  sessions, and clears the cookie on the client that presented it

### Token storage

The two tokens are deliberately handled differently, because they have
different blast radii if stolen:

| | Access token | Refresh token |
|---|---|---|
| Returned in | JSON body | `Set-Cookie` only |
| Lifetime | 15 minutes | 7 days |
| Readable by page JS | yes (by design — you attach it) | **no** — `HttpOnly` |
| Sent to | any endpoint, via `Authorization` | only `/api/v1/auth/refresh`, via `Path` scope |

Neither token is written to `localStorage` or `sessionStorage` by the web
client; the access token is held in memory for the life of the tab. A browser
client must therefore call `/auth/refresh` with `credentials: "include"` on
startup to obtain an access token, and non-browser clients must persist
cookies between calls.

### Logout

```bash
curl -X POST http://localhost:8005/api/v1/auth/logout \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIs..."
```

Revokes all refresh tokens for the user.

## Agent Authentication (API Key)

Agents authenticate using API keys sent via the `X-Agent-Key` header.

### Creating an Agent

Via the web UI:
1. Navigate to **Workspace Settings > Agents**
2. Click **Register Agent**
3. Enter a name and agent type (e.g., `claude_code`, `openclaw`, `cline`, `aider`, `custom`)
4. Copy the API key — **it is shown only once**

Via the REST API (requires admin role):

```bash
curl -X POST http://localhost:8005/api/v1/workspaces/{ws_id}/agents \
  -H "Authorization: Bearer <jwt>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-claude-agent",
    "type": "claude_code"
  }'
```

### API Key Format

```
agk_{workspace_slug}_{random}
```

Example: `agk_my-team_a1b2c3d4e5f6g7h8i9j0`

Keys are bcrypt-hashed in the database. Only the prefix (`agk_my-team_`) is stored in plaintext for lookup optimization.

### Using the API Key

Include the key in the `X-Agent-Key` header:

```bash
curl http://localhost:8005/api/v1/agents/me \
  -H "X-Agent-Key: agk_my-team_a1b2c3d4e5f6..."
```

Agents can also use the key with the MCP server via the `MESH_AGENT_KEY` environment variable.

### Key Rotation

Regenerate an agent's API key (old key is immediately invalidated):

```bash
curl -X POST http://localhost:8005/api/v1/agents/{agent_id}/regenerate-key \
  -H "Authorization: Bearer <jwt>"
```

### Agent Self-Service

Agents can view and update their own profile:

```bash
# View own profile
curl http://localhost:8005/api/v1/agents/me \
  -H "X-Agent-Key: agk_..."

# Update profile
curl -X PATCH http://localhost:8005/api/v1/agents/me \
  -H "X-Agent-Key: agk_..." \
  -H "Content-Type: application/json" \
  -d '{
    "profile_description": "Backend specialist",
    "role": "developer",
    "capabilities": ["go", "postgresql", "testing"],
    "callback_url": "https://my-agent.example.com/hooks"
  }'
```

## Chat-session tokens (`cst_`)

A chat-session token lets an agent act on behalf of one user, in one project, for one chat. It is minted
only by the chat runner (never by the model) and is accepted as `Authorization: Bearer cst_...`.

- **Rights = agent ∩ user.** The request authenticates as the agent, and the delegating user's *current*
  workspace role is applied on every call (same clamp as OAuth connectors). A viewer working through an
  admin-capable agent still gets `403` on a write; a strong agent never widens a viewer.
- **Live checks, no cache.** Every request re-reads the user's workspace/project membership and the agent's
  connection and project membership. Anything missing is a `401` (the code is pinned by tests).
- **Project-confined.** The token reaches only its own project. A resource of another project, or a route
  that names no project (except `GET /agents/me`), is `403` (`chat_scope_denied`).
- **Short-lived.** TTL 5 minutes (server time). Renewal is a runner call, not a model call; the previous
  token stays valid for at most 10 s after a renewal. Stop revokes the whole session at once and a stopped
  session cannot be renewed or issued again under the same `chat_session_id` whatever the epoch (`409`); a restart is a new session id. Within a live session a newer `execution_epoch` takes it over (the previous runner's tokens end at once), an older or equal one from another runner is refused (`409`).
- **Attribution.** History rows written under a chat token carry `changes.initiated_by_user_id`,
  `chat_thread_id`, `chat_session_id`, `execution_epoch`, and the activity list returns `initiated_by_name`,
  so a client can render "Agent X on behalf of Y (chat)". No chat text and no token value is stored.

Control routes (runner only: `MESH_CHAT_CONTROLLER_TOKEN` in `X-Chat-Controller-Token`, loopback/direct, fail
closed when unset): `POST /internal/chat-sessions/tokens`, `POST /internal/chat-sessions/:session_id/renew`,
`POST /internal/chat-sessions/:session_id/revoke`.

Not covered here (E5): the MCP token file and isolation of other credentials, active-turn binding, and
project scoping of workspace-level list/search routes (those are refused for a chat token for now).

## Role-Based Access Control (RBAC)

### Roles

| Role | Description |
|------|-------------|
| `owner` | Full workspace control, billing, danger zone |
| `admin` | Project management, member management, settings |
| `member` | Create/edit tasks, comments, artifacts |
| `viewer` | Read-only access to workspace data |
| `agent` | Task operations, event bus, artifacts (assigned automatically to agents) |

### Permissions

| Permission | owner | admin | member | viewer | agent |
|------------|:-----:|:-----:|:------:|:------:|:-----:|
| `workspace:read` | x | x | x | x | x |
| `workspace:write` | x | | | | |
| `workspace:manage` | x | | | | |
| `project:read` | x | x | x | x | x |
| `project:write` | x | x | | | |
| `project:manage` | x | x | | | |
| `task:read` | x | x | x | x | x |
| `task:write` | x | x | x | | x |
| `task:manage` | x | x | | | |
| `comment:read` | x | x | x | x | x |
| `comment:write` | x | x | x | | x |
| `agent:read` | x | x | x | x | x |
| `agent:write` | x | x | | | |
| `event:read` | x | x | x | x | x |
| `event:write` | x | x | x | | x |

### Project-Level Membership

Beyond workspace roles, access to individual projects is controlled by project membership:

- Workspace **owners** and **admins** can access all projects (bypass)
- Workspace **members**, **viewers**, and **agents** must be added to specific projects
- Project membership is enforced on all project-scoped routes (`/projects/:id/...`)
- Agents are auto-enrolled when assigned tasks in a project

## Common Integration Patterns

### MCP Agent (Claude Code)

Configure in `.mcp.json`:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "evc-mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_AGENT_KEY": "agk_workspace_your-key"
      }
    }
  }
}
```

### REST Client (Custom Bot)

```python
import requests

BASE_URL = "https://mesh.example.com/api/v1"
HEADERS = {"X-Agent-Key": "agk_workspace_your-key"}

# Get assigned tasks
tasks = requests.get(f"{BASE_URL}/agents/me/tasks", headers=HEADERS).json()

# Create a task
requests.post(f"{BASE_URL}/projects/{project_id}/tasks", headers=HEADERS, json={
    "title": "Implement feature X",
    "priority": "high"
})
```

### Go SDK

```go
import "github.com/entire-vc/evc-mesh/pkg/sdk"

client := sdk.New(sdk.Config{
    BaseURL:  "https://mesh.example.com",
    AgentKey: "agk_workspace_your-key",
})

tasks, _ := client.ListMyTasks(ctx, sdk.TaskFilter{StatusCategory: "todo"})
```

## Security Notes

- JWT uses HS256 with explicit algorithm validation (rejects other signing methods)
- Access tokens have a `jti` claim (JWT ID) for potential future blacklisting
- Agent API keys use 256 bits of entropy from `crypto/rand`
- All passwords are bcrypt-hashed with cost factor 10
- Rate limiting is applied to auth endpoints (configurable via `MESH_RATE_LIMIT_AUTH_RPM`)
- CORS is configurable via `MESH_CORS_ORIGINS` environment variable
