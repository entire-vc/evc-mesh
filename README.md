# Entire VC Mesh

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)](https://react.dev)
[![MCP](https://img.shields.io/badge/MCP-45_tools-8B5CF6)](docs/mcp-reference.md)
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
- **MCP server** with 45 tools across 11 categories (stdio + HTTP SSE transports)
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
- RBAC with 15 permissions across 5 roles (owner, admin, member, viewer, agent)
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

## Quick Start

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
# Edit deploy/docker/mesh/.env — at minimum, change JWT_SECRET
# or manage the stack via: make docker-up
```

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

```bash
go run ./cmd/mcp --transport sse --port 8081
```

Open http://localhost:3000, register an account, and create your first workspace.

For detailed setup, see [Quick Start Guide](docs/quickstart.md) and [Self-Hosting Guide](docs/self-hosting.md).

## MCP Integration

Connect any MCP-compatible agent to Mesh. Example for Claude Code (`.mcp.json`):

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "go",
      "args": ["run", "./cmd/mcp"],
      "cwd": "/path/to/evc-mesh",
      "env": {
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

The MCP server exposes 45 tools for managing projects, tasks, comments, artifacts, events, rules, and more. See [MCP Reference](docs/mcp-reference.md) for the full tool catalog.

## Documentation

| Document | Description |
|----------|-------------|
| [Quick Start](docs/quickstart.md) | Get up and running in minutes |
| [Self-Hosting Guide](docs/self-hosting.md) | Production deployment with Docker Compose from `deploy/docker/mesh/` |
| [Architecture](docs/architecture.md) | System architecture and design decisions |
| [API Authentication](docs/api-authentication.md) | JWT, agent keys, and RBAC |
| [MCP Reference](docs/mcp-reference.md) | All 45 MCP tools with parameters and examples |
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
