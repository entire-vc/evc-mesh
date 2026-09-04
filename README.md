# Entire VC Mesh

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)](https://react.dev)
[![MCP](https://img.shields.io/badge/MCP-61_tools-8B5CF6)](docs/mcp-reference.md)
[![Status](https://img.shields.io/badge/Status-Alpha-orange)](https://github.com/entire-vc/evc-mesh/releases)

> **Alpha Release** — Mesh is under active development. APIs may change between versions. We welcome early adopters and feedback.

Task management platform for coordinating humans and AI agents in a unified workspace. Designed for teams that work alongside AI coding agents such as Claude Code, OpenClaw, Cline, and Aider.

Mesh provides a **dual interface**: a web UI with kanban boards for humans and an MCP/REST API for agents, connected by a real-time event bus so both sides share context.

## Why Mesh?

Traditional project management tools treat AI agents as an afterthought. Mesh is built from the ground up for human-agent collaboration:

- **Agents are first-class citizens** — they authenticate, receive tasks, report progress, and share context with other agents
- **Real-time coordination** — NATS JetStream event bus enables inter-agent context sharing without polling
- **One source of truth** — both humans (web UI) and agents (MCP/REST) operate on the same task board
- **Self-hosted** — your data stays on your infrastructure

## Features

### Work Management
- Kanban boards with drag-and-drop, customizable statuses per project
- List, Timeline (DAG), and Calendar views with saved view presets
- Custom fields (12 types: text, number, date, select, multiselect, URL, email, checkbox, user/agent references, JSON)
- Task dependencies visualized as a DAG timeline
- Subtasks, comments, labels, and artifact attachments (S3/MinIO)
- Recurring tasks with cron scheduling
- Initiatives and objectives for cross-project tracking
- Bulk operations and inline editing in list view

### Agent Integration
- **MCP server** with 61 tools (stdio + HTTP SSE transports)
- **REST API** with 125+ routes at `/api/v1`
- **Go SDK** (`pkg/sdk/`) for building custom integrations
- Agent authentication via API keys (`X-Agent-Key`)
- Agent dashboard with profiles, capabilities, and team directory
- Push notifications: callback URL, SSE stream, or long-polling
- Atomic task checkout with TTL-based exclusive locks
- Auto-transition rules for automated workflow progression

### Real-time Collaboration
- NATS JetStream event bus for inter-agent context sharing
- WebSocket push with per-channel subscriptions
- Webhooks with HMAC-SHA256 signatures
- Context enrichment: summaries, decisions, blockers grouping

### Platform
- Multi-tenant with workspace isolation on every table
- Project-level membership enforcement (users and agents)
- RBAC with 16 permissions across 4 workspace roles (owner, admin, member, viewer);
  agents authenticate by API key and hold their own fixed permission set
- Built-in JWT auth (HS256) for users, API keys for agents
- Rate limiting (per-IP for auth, per-actor for API)
- Config import/export (YAML) with workflow templates
- Prometheus metrics and Grafana dashboards
- Visual org chart with team directory

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend | Go 1.22+, Echo v4 |
| Database | PostgreSQL 16 (JSONB for custom fields) |
| Cache / PubSub | Redis 7 |
| Event Bus | NATS JetStream |
| Frontend | React 19, TypeScript, Tailwind CSS 4, Zustand 5 |
| MCP Server | mcp-go SDK |
| File Storage | S3-compatible (MinIO for self-hosted) |
| Migrations | Goose |

## Run it with Docker

Docker is the only prerequisite. This builds and starts everything — API, web
UI, MCP server, Postgres, Redis, NATS, MinIO, nginx — and leaves you with a
login page. If you want to *develop* Mesh rather than run it, skip to
[Development setup](#development-setup) instead.

```bash
git clone https://github.com/entire-vc/evc-mesh && cd evc-mesh/deploy/docker/mesh

# The file must be called .env, in this directory. Compose auto-loads only
# that name, and a misnamed copy fails as "POSTGRES_PASSWORD is missing a
# value" — which reads like a missing variable rather than an unread file.
cp .env.prod.example .env

# Fill in the required secrets, in place. Appending them instead would leave
# two lines per key — Compose reads the last one, so editing the empty one at
# the top later would silently do nothing.
for v in POSTGRES_PASSWORD REDIS_PASSWORD JWT_SECRET MINIO_SECRET_KEY \
         GRAFANA_PASSWORD MESH_INTEGRATION_ENCRYPTION_KEY; do
  s=$(openssl rand -base64 32)
  sed "s|^$v=.*|$v=$s|" .env > .env.tmp && mv .env.tmp .env
done
sed 's|^MINIO_ACCESS_KEY=.*|MINIO_ACCESS_KEY=meshadmin|' .env > .env.tmp && mv .env.tmp .env

docker compose -f docker-compose.prod.yml --env-file .env up -d --build
```

The first build compiles the Go binaries and the frontend from source: expect
**5–10 minutes** (measured: ~8 on an M-series Mac, including image pulls).
Later starts take seconds.

```bash
docker compose -f docker-compose.prod.yml --env-file .env ps
# every service should read "healthy" or "running"
```

### Get in

`.env.prod.example` ships `MESH_SEED_ADMIN=true`, so the API creates the first
account on an empty database and prints the password **once**:

```bash
docker compose -f docker-compose.prod.yml --env-file .env logs api | grep bootstrap
```

```
[bootstrap] ────────────────────────────────────────────────────────
[bootstrap] First admin created: admin@localhost
[bootstrap] Generated password:  <24 random characters>
[bootstrap] This password is shown ONCE and is not stored anywhere.
```

Open `http://localhost:${HTTP_PORT}` (80 by default) and log in with it. To
choose the password yourself, set `MESH_ADMIN_PASSWORD` **before** the first
boot — the seed runs only while the database has zero users.

Self-registration ships closed (`MESH_ALLOW_REGISTRATION=false`), so this admin
account is the only way in until you invite someone. Adding people, SMTP, TLS,
every environment variable, and the security checklist to run through before
putting this on a public address: [Self-Hosting Guide](docs/self-hosting.md).

## Development setup

Running from source, with the compose file providing only the backing services.

### Prerequisites

- Go 1.22+
- Node.js 20+ and pnpm
- Docker and Docker Compose (for infrastructure services)

### 1. Start infrastructure

```bash
git clone https://github.com/entire-vc/evc-mesh && cd evc-mesh
cd deploy/docker/mesh && docker compose up -d
# or: make docker-up
```

This starts PostgreSQL, Redis, NATS, and MinIO.

### 2. Configure environment

```bash
export JWT_SECRET=$(openssl rand -base64 32)
```

The API server has no dotenv loader — it reads the process environment only, so
a `.env` file is not picked up by `go run ./cmd/api`. `.env.example` is the
reference list of names and defaults; Docker Compose is what actually reads an
env file (`deploy/docker/mesh/.env`).

Defaults in `.env.example` already match the ports the compose file above
publishes, so local development works without further edits.

### 3. Start the API server

```bash
go run ./cmd/api
# Listening on :8005, migrations applied automatically
```

### 4. Start the frontend

```bash
cd web && pnpm install && pnpm dev
# Listening on :3000
```

### 5. Start the MCP server (optional)

The MCP server is a separate module —
[`evc-mesh-mcp`](https://github.com/entire-vc/evc-mesh-mcp) — not part of this
repository:

```bash
go install github.com/entire-vc/evc-mesh-mcp@latest
MESH_API_URL=http://localhost:8005 MESH_MCP_PORT=8081 \
  evc-mesh-mcp --transport sse
```

`--transport` is the only flag; the port is set via `MESH_MCP_PORT`. See
[Agent Onboarding](docs/agent-onboarding.md) to connect a client.

### 6. Create the first account

A fresh install ships with **no users and no default password**. Open
http://localhost:3000/register and create an account — the first one you
register is yours, and it gets its own workspace.

Trying to log in before registering returns `401 invalid email or password`.
That is expected: the account does not exist yet.

If you would rather have the server create the admin for you — useful for
scripted, headless, or container-only installs — start the API once with:

```bash
MESH_SEED_ADMIN=true \
MESH_ADMIN_EMAIL=you@example.com \
MESH_ADMIN_PASSWORD='<strong-password>' \
go run ./cmd/api
```

The seed runs **only when the database has zero users**, and the API logs what
it did on every boot (`[bootstrap] ...`) — including why it skipped. Omit
`MESH_ADMIN_PASSWORD` and a strong password is generated and printed once.

See [Seeding the first admin](docs/self-hosting.md#seeding-the-first-admin) for
the full reference.

For detailed setup, see [Quick Start Guide](docs/quickstart.md) and [Self-Hosting Guide](docs/self-hosting.md).

## MCP Integration

Connect any MCP-compatible agent to Mesh. Example for Claude Code (`.mcp.json`):

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "evc-mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_API_URL": "http://localhost:8005",
        "MESH_AGENT_KEY": "agk_workspace_your-key-here"
      }
    }
  }
}
```

Or connect via SSE for remote agents:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "url": "http://localhost:8081/sse",
      "headers": {
        "Authorization": "Bearer <agent-api-key>"
      }
    }
  }
}
```

The MCP server exposes 61 tools for managing projects, tasks, comments, artifacts, events, rules, memory, and more. See [MCP Reference](docs/mcp-reference.md) for the full tool catalog, and [Agent Onboarding](docs/agent-onboarding.md) for connecting an agent to a self-hosted instance.

**There is one implementation, and it is not in this repository.** Until
2026-08 this repo carried a second copy of the same server under `./cmd/mcp`
(+ `internal/mcp`). Go's `internal/` visibility rules mean neither module can
import the other, so the two copies were maintained by hand and drifted — the
copy here was 12 tools behind when it was removed. Only
[`evc-mesh-mcp`](https://github.com/entire-vc/evc-mesh-mcp) remains.

### Running a fleet, not one agent

The section above connects a single agent. Running several — each with its own
lane, workspace and identity, fed tasks from Mesh — is a separate concern, and
there is a starter kit for it:
[`evc-mesh-fleet-starter`](https://github.com/entire-vc/evc-mesh-fleet-starter).

It ships the two drivers we run ourselves (a feeder that keeps persistent agent
sessions, and an SSE dispatcher that spawns one per task), the registry that
renders each agent's `.mcp.json`, example agent instructions, and launchd units.
It works against any Mesh instance, including one behind an HTTP basic-auth
proxy.

## Documentation

| Document | Description |
|----------|-------------|
| [Quick Start](docs/quickstart.md) | Get up and running in minutes |
| [Self-Hosting Guide](docs/self-hosting.md) | Production deployment with Docker Compose from `deploy/docker/mesh/` |
| [Architecture](docs/architecture.md) | System architecture and design decisions |
| [API Authentication](docs/api-authentication.md) | JWT, agent keys, and RBAC |
| [Agent Onboarding](docs/agent-onboarding.md) | Issue agent keys, connect over stdio or SSE, run MCP behind a proxy |
| [MCP Reference](docs/mcp-reference.md) | Tool catalogue with parameters and examples — all 61 tools |
| [Custom Fields](docs/custom-fields.md) | Guide for 12 custom field types |
| [Webhooks](docs/webhooks.md) | Webhook setup with HMAC-SHA256 validation |
| [Agent Push Notifications](docs/agent-push-notifications.md) | Callback URL, SSE, and long-polling |
| [OpenAPI Spec](docs/openapi.yaml) | REST API specification (OpenAPI 3.0.3) |
| [Security Audit](docs/security-audit.md) | Security model and audit findings |
| [Contributing](docs/contributing.md) | How to contribute |

## Project Structure

```
evc-mesh/
├── cmd/
│   ├── api/             # REST API + WebSocket server
│   └── mcp/             # MCP server (stdio + SSE)
├── internal/            # Core business logic
│   ├── handler/         # HTTP handlers (30 files)
│   ├── service/         # Business services
│   ├── repository/      # Database repositories (sqlx)
│   ├── middleware/       # Auth, RBAC, RLS, rate limiting
│   ├── eventbus/        # NATS JetStream event bus
│   └── ws/              # WebSocket hub
├── pkg/sdk/             # Go SDK for external integrations
├── migrations/          # SQL migrations (40 files, goose)
├── web/                 # React frontend
│   └── src/
│       ├── pages/       # Route pages (19+)
│       ├── components/  # UI components
│       └── stores/      # Zustand stores (16+)
├── docs/                # Public documentation
├── deploy/
│   └── docker/
│       └── mesh/        # Docker Compose stack, env files, bind-mounted volumes
└── Makefile             # Helpers including docker-up/docker-down
```

## Alpha Status

This is an **alpha release**. What this means:

- **Core features are stable** — task management, agent integration, MCP, event bus, and the web UI are all functional and used in production
- **APIs may change** — REST and MCP tool signatures may evolve based on feedback
- **Missing features** — Python/TypeScript SDKs, SwaggerUI, and some planned features are not yet implemented
- **Bug reports welcome** — please open issues for any problems you encounter

We follow [semantic versioning](https://semver.org/). Breaking changes will be documented in release notes.

## Contributing

We welcome contributions! Please see [CONTRIBUTING](docs/contributing.md) for guidelines.

Quick overview:
1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes with tests
4. Submit a pull request

## License

This project is licensed under the [Apache License 2.0](LICENSE).

Copyright (c) 2026 Entire VC
