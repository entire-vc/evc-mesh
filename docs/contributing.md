# Contributing to Mesh

Thank you for your interest in contributing to Mesh! This guide covers how to set up a development environment, submit changes, and follow project conventions.

## Code of Conduct

Be respectful and constructive. We are building a tool for collaborative work — our community should reflect that.

## Getting Started

### Prerequisites

- Go 1.22+
- Node.js 20+ and pnpm
- Docker and Docker Compose
- PostgreSQL client tools (optional, for manual DB access)

### Development Setup

```bash
# Clone the repository
git clone https://github.com/entire-vc/evc-mesh && cd evc-mesh

# Start infrastructure
cd deploy/docker/mesh && docker compose up -d
# or from the repo root: make docker-up

# Edit deploy/docker/mesh/.env if you need to override local defaults

# Start the API server (runs migrations automatically)
go run ./cmd/api

# In another terminal — start the frontend
cd web && pnpm install && pnpm dev
```

The API runs on `:8005`, the frontend on `:3000`.

### Running Tests

```bash
# Backend tests (requires running PostgreSQL, Redis, NATS)
go test ./...

# Frontend lint and type check
cd web && pnpm lint && pnpm tsc --noEmit
```

## How to Contribute

### Reporting Issues

- Use GitHub Issues for bug reports and feature requests
- Include steps to reproduce for bugs
- Include your Go version, OS, and browser (for frontend issues)

### Submitting Pull Requests

1. **Fork** the repository and create a feature branch from `main`
2. **Make your changes** — keep PRs focused on a single concern
3. **Add tests** for new functionality
4. **Run tests** — ensure `go test ./...` passes
5. **Run lint** — ensure `cd web && pnpm lint` passes (for frontend changes)
6. **Submit a PR** against `main` with a clear description

### PR Guidelines

- Keep PRs small and focused (one feature or fix per PR)
- Write descriptive commit messages explaining "why", not just "what"
- Add tests for new backend features
- Update documentation if you change API behavior or add new features
- Don't modify unrelated code in the same PR

## Project Conventions

### Backend (Go)

- **Framework**: Echo v4
- **Database**: sqlx with raw SQL (no ORM)
- **Architecture**: handler → service → repository layers
- **Naming**: `snake_case` for SQL columns, `camelCase` for JSON, `PascalCase` for Go types
- **Error handling**: Return domain errors from services, handlers map them to HTTP codes
- **UUIDs**: All primary keys are UUIDs
- **Timestamps**: `TIMESTAMPTZ` in PostgreSQL, `time.Time` in Go
- **Soft deletes**: Use `deleted_at` column where applicable
- **Multi-tenancy**: Every table has `workspace_id`, filtered via RLS middleware

### Frontend (React/TypeScript)

- **State management**: Zustand stores in `web/src/stores/`
- **Styling**: Tailwind CSS 4
- **Components**: shadcn/ui pattern in `web/src/components/ui/`
- **API calls**: Via `web/src/lib/api.ts` helper
- **Pages**: In `web/src/pages/`, one file per route
- **Hooks rule**: Never place React hooks after early returns — this causes production errors

### Database Migrations

- Migrations use [Goose](https://github.com/pressly/goose) and live in `migrations/`
- File naming: `YYYYMMDDNNN_description.sql` (e.g., `20260305035_extend_project_members.sql`)
- Always include both `-- +goose Up` and `-- +goose Down` sections
- Run `go run ./cmd/api migrate up` to apply (also runs automatically on API startup)

### MCP Tools

- MCP server lives in `cmd/mcp/` and calls the REST API via HTTP
- Tool definitions follow outcome-oriented naming (e.g., `move_task` not `update_status`)
- Each tool should have a clear description and document all parameters

## Architecture Overview

```
┌──────────────────────────────────────────────┐
│  Clients (Web UI / MCP Agents / REST)        │
└────────┬─────────────────────────────────────┘
         │
┌────────▼─────────────────────────────────────┐
│  Echo HTTP Server                             │
│  Middleware: Auth → RLS → ProjectAccess → RBAC│
└────────┬─────────────────────────────────────┘
         │
┌────────▼─────────────────────────────────────┐
│  Handlers → Services → Repositories           │
└────────┬──────────┬──────────┬───────────────┘
         │          │          │
    PostgreSQL    Redis    NATS JetStream
```

- **Handlers** parse HTTP requests and return responses
- **Services** contain business logic and orchestrate operations
- **Repositories** handle database queries with sqlx

For a detailed architecture overview, see [docs/architecture.md](architecture.md).

## Areas Where Help is Wanted

- **Python SDK** — typed client for Python agents (matching the Go SDK in `pkg/sdk/`)
- **TypeScript SDK** — typed client for Node.js/Deno agents
- **Documentation** — improving guides, adding examples, fixing typos
- **Frontend** — accessibility improvements, mobile responsiveness
- **Testing** — increasing backend test coverage, adding integration tests
- **Translations** — the web UI currently supports English only

## Questions?

Open a GitHub Issue or start a Discussion. We are happy to help you get started.
