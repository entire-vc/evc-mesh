# Entire VC Mesh

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)](https://react.dev)
[![MCP](https://img.shields.io/badge/MCP-23_tools-8B5CF6)](docs/mcp-reference.md)

Task management platform for coordinating humans and AI agents in a unified workspace. Designed for teams that work alongside AI coding agents such as Claude Code, OpenClaw, Cline, and Aider.

Mesh provides a dual interface: a web UI with kanban boards for humans and an MCP/REST API for agents, connected by a real-time event bus so both sides share context.

## Features

**Work Management**
- Kanban boards with drag-and-drop, customizable statuses per project
- Custom fields (12 types: text, number, date, select, multiselect, URL, email, checkbox, user/agent references, JSON)
- Task dependencies visualized as a DAG timeline
- List view with sortable columns and custom field support
- Comments, subtasks, and artifact attachments (S3/MinIO)

**Agent Integration**
- MCP server with 23 tools across 4 categories (stdio + HTTP SSE transports)
- REST API with 46+ routes at `/api/v1`
- Agent authentication via API keys (`X-Agent-Key`)
- Agent dashboard for registration and monitoring

**Real-time Collaboration**
- NATS JetStream event bus for inter-agent context sharing
- WebSocket push with per-channel subscriptions
- Event feed with filtering by type and project
- Context enrichment: summaries, decisions, blockers grouping

**Architecture**
- Multi-tenant with workspace isolation on every table
- Built-in JWT auth (HS256) for users, API keys for agents
- Two server binaries: `cmd/api` (REST + WebSocket) and `cmd/mcp` (MCP server)
- PostgreSQL JSONB for flexible custom field storage with GIN indices

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend | Go 1.22+, Echo v4 |
| Database | PostgreSQL 16 |
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
docker compose up -d
```

This starts PostgreSQL, Redis, NATS, and MinIO with the following local ports:

| Service | Port |
|---------|------|
| PostgreSQL | 5437 |
| Redis | 6383 |
| NATS | 4223 |
| MinIO Console | 9002 |

### 2. Configure environment

```bash
cp .env.example .env
# Edit .env with your settings (database URL, JWT secret, etc.)
```

### 3. Run migrations

```bash
go run ./cmd/api migrate up
```

### 4. Start the API server

```bash
go run ./cmd/api
# Listening on :8005
```

### 5. Start the frontend

```bash
cd web
pnpm install
pnpm dev
# Listening on :3000
```

### 6. Start the MCP server (optional)

```bash
go run ./cmd/mcp --transport sse --port 8081
```

For detailed setup instructions, see [docs/quickstart.md](docs/quickstart.md) and [docs/self-hosting.md](docs/self-hosting.md).

## MCP Integration

Connect any MCP-compatible agent to Mesh. Example configuration for Claude Code:

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

The MCP server exposes 23 tools for managing projects, tasks, comments, artifacts, and the event bus. See [docs/mcp-reference.md](docs/mcp-reference.md) for the full tool catalog.

## Documentation

| Document | Description |
|----------|-------------|
| [Quick Start](docs/quickstart.md) | Get up and running in minutes |
| [Self-Hosting Guide](docs/self-hosting.md) | Production deployment with Docker Compose |
| [MCP Reference](docs/mcp-reference.md) | All 23 MCP tools with parameters and examples |
| [OpenAPI Spec](docs/openapi.yaml) | REST API specification (OpenAPI 3.0.3) |
| [Security Audit](docs/security-audit.md) | Security model and audit notes |

## Project Structure

```
evc-mesh/
├── cmd/
│   ├── api/          # REST API + WebSocket server
│   └── mcp/          # MCP server (stdio + SSE)
├── internal/         # Core business logic
│   ├── handler/      # HTTP handlers
│   ├── service/      # Business services
│   ├── repo/         # Database repositories (sqlx)
│   ├── eventbus/     # NATS JetStream event bus
│   └── ws/           # WebSocket hub
├── migrations/       # SQL migrations (goose)
├── web/              # React frontend
│   └── src/
│       ├── pages/    # Route pages
│       ├── components/
│       └── stores/   # Zustand stores
├── docs/             # Public documentation
└── docker-compose.yml
```

## Contributing

Contributions are welcome. Please open an issue to discuss your proposed changes before submitting a pull request.

## License

This project is licensed under the [MIT License](LICENSE).

Copyright (c) 2026 Entire VC
