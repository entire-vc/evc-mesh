# Architecture

This document describes the system architecture of Mesh — how components fit together, how data flows, and the key design decisions behind the platform.

## Overview

Mesh is a two-layer system:

1. **Work Management Plane** (human-facing) — React web UI with kanban boards, list/timeline/calendar views, and project management tools
2. **Agent Collaboration Plane** (agent-facing) — MCP server, REST API, and event bus for AI agent coordination

Both layers share the same data and real-time event infrastructure.

```
┌─────────────────────────────────────────────────────────────┐
│                        Clients                               │
│  Web UI (React)  │  MCP Agents  │  REST Clients  │  Go SDK  │
└────────┬─────────┴───────┬──────┴────────┬───────┴──────────┘
         │                 │               │
         │          ┌──────▼──────┐        │
         │          │  MCP Server │        │
         │          │  (cmd/mcp)  │        │
         │          └──────┬──────┘        │
         │                 │ HTTP          │
         ▼                 ▼               ▼
┌──────────────────────────────────────────────────────────────┐
│                    API Server (cmd/api)                        │
│                                                                │
│  Echo HTTP Router                                              │
│  ├── Middleware Chain                                           │
│  │   ├── CORS                                                  │
│  │   ├── Rate Limiter (in-memory + Redis)                      │
│  │   ├── DualAuth (JWT for users, API Key for agents)          │
│  │   ├── WorkspaceRLS (sets PostgreSQL session var)             │
│  │   ├── RequireProjectMember (project-level access)           │
│  │   └── RequirePermission (RBAC, 15 permissions)              │
│  │                                                             │
│  ├── HTTP Handlers (30 files)                                  │
│  ├── Business Services                                         │
│  └── Database Repositories (sqlx)                              │
│                                                                │
│  WebSocket Hub ──── Real-time push to browsers                 │
│  Webhook Dispatcher ──── HMAC-signed HTTP callbacks            │
│  Recurring Scheduler ──── Cron-based task creation             │
└────┬──────────┬──────────┬───────────────────────────────────┘
     │          │          │
     ▼          ▼          ▼
PostgreSQL   Redis     NATS JetStream     S3/MinIO
(data)       (cache,   (event bus,        (artifacts)
             rate      persistence,
             limits)   replay)
```

## Server Binaries

Mesh ships as two binaries:

### cmd/api — REST API + WebSocket

The main server handling all HTTP traffic:

- REST API at `/api/v1` (125+ routes)
- WebSocket at `/ws` with channel subscriptions
- Static file serving for the React SPA (in production)
- Prometheus metrics at `/metrics`
- Health check at `/health`
- Database migrations on startup (Goose)

### cmd/mcp — MCP Server

A separate process that implements the Model Context Protocol for AI agents:

- Supports **stdio** transport (for local agents like Claude Code) and **HTTP SSE** transport (for remote agents)
- Calls the REST API via HTTP — does not access the database directly
- 45 tools across 11 categories
- Authenticates using agent API keys

The MCP server is intentionally separate from the API server. This ensures a single audit trail (all operations go through the REST API) and allows independent scaling.

## Data Layer

### PostgreSQL

- All tables have `workspace_id` for multi-tenant isolation
- Row-Level Security (RLS) policies as defense-in-depth
- `WorkspaceRLS` middleware sets `app.current_workspace_id` session variable before every query
- Custom fields stored as JSONB on the `tasks` table with GIN indices for query performance
- Soft deletes via `deleted_at` column (tasks, agents, workspaces)
- UUIDs for all primary keys
- 40 migration files managed by Goose

### Redis

- Session cache with TTL (30-minute eviction for SSE sessions)
- Rate limiting state (sliding window counters)
- Context enrichment cache (60-second TTL)
- Real-time notifications (pub/sub for long-polling)

### NATS JetStream

- Event bus for inter-agent context sharing
- Persistent message streams with replay capability
- Subject hierarchy: `events.{workspace_id}.{project_id}.{event_type}`
- At-least-once delivery guarantee
- Used by agents to publish summaries, decisions, and blockers

### S3 / MinIO

- Artifact storage (file attachments on tasks)
- Organized by workspace: `{workspace_id}/{artifact_id}/{filename}`
- Presigned URLs for secure downloads
- MinIO for self-hosted deployments, any S3-compatible service for cloud

## Authentication

Mesh uses **dual authentication** — every API endpoint accepts either a user JWT or an agent API key:

### User Auth (JWT)
- Registration and login via `/api/v1/auth/register` and `/api/v1/auth/login`
- HS256 JWT with 15-minute expiry
- Refresh tokens (7-day TTL) with rotation and theft detection
- Passwords: bcrypt with cost 10, min 8 chars, complexity requirements

### Agent Auth (API Key)
- Format: `agk_{workspace_slug}_{random}`
- Sent via `X-Agent-Key` header
- bcrypt-hashed in database, prefix stored for lookup optimization
- Key rotation via `POST /agents/:id/regenerate-key`

See [API Authentication](api-authentication.md) for full details.

## Authorization (RBAC)

5 roles with 15 permissions:

| Role | Scope | Key Permissions |
|------|-------|-----------------|
| `owner` | Workspace | All permissions, workspace management |
| `admin` | Workspace | Project CRUD, member management, settings |
| `member` | Workspace | Task CRUD, comments, artifacts |
| `viewer` | Workspace | Read-only access |
| `agent` | Workspace | Task operations, event bus, artifacts |

Additionally, **project-level membership** controls access to individual projects. Workspace owners and admins bypass project membership checks (they can access all projects).

## Middleware Chain

Every request passes through a middleware chain in this order:

1. **CORS** — configurable allowed origins
2. **Rate Limiter** — per-IP for auth endpoints, per-actor for API
3. **DualAuth** — extracts user (from JWT) or agent (from API key) identity
4. **WorkspaceRLS** — sets PostgreSQL session variable for row-level security
5. **RequireProjectMember** — checks project membership (on project-scoped routes)
6. **RequirePermission** — checks RBAC permission (e.g., `task:write`)

## Real-time Updates

### WebSocket

- Endpoint: `/ws`
- Clients subscribe to channels: `project:{uuid}`, `task:{uuid}`, `eventbus:project:{uuid}`
- Server pushes task updates, status changes, new comments, event bus messages
- Used by the React frontend for live board updates

### Webhooks

- External HTTP callbacks with HMAC-SHA256 signatures
- Events: `task.created`, `task.assigned`, `task.status_changed`, `task.deleted`, and more
- 3 retries with exponential backoff
- Auto-deactivate after 10 consecutive failures
- See [Webhooks](webhooks.md) for setup details

### Agent Push Notifications

Three mechanisms for delivering events to agents:

- **Callback URL** — Mesh POSTs events to the agent's registered URL
- **SSE stream** — `GET /agents/me/events/stream`
- **Long-polling** — `GET /agents/me/tasks/poll?timeout=30`

See [Agent Push Notifications](agent-push-notifications.md) for details.

## Frontend Architecture

- **React 19** with TypeScript
- **Zustand 5** for state management (16+ stores)
- **Tailwind CSS 4** for styling
- **React Router 7** for routing (19+ pages)
- **shadcn/ui** component patterns

### Key Views

| View | Description |
|------|-------------|
| Board | Kanban columns by status, drag-and-drop |
| List | Sortable table with inline editing, bulk operations |
| Timeline | DAG dependency graph visualization |
| Calendar | Tasks by due date on a monthly calendar |

### State Management

Each domain has a dedicated Zustand store:
- `task.ts` — tasks, filters, board state
- `project.ts` — projects, current project
- `workspace.ts` — workspaces, current workspace
- `agent.ts` — agent list, profiles
- `saved-view-store.ts` — saved filter/sort presets
- And 11 more for specific features

## Key Design Decisions

### MCP Server Calls REST API (Not Direct DB)

The MCP server is a separate process that authenticates with the REST API using agent keys. This ensures:
- Single audit trail for all operations
- Consistent authorization and validation
- Independent deployment and scaling
- The REST API is the only data access point

### JSONB for Custom Fields

Custom field values are stored as a JSONB column on the tasks table rather than an EAV (Entity-Attribute-Value) pattern:
- Single row per task (no joins for reading)
- GIN index for efficient JSONB queries
- Flexible schema — new field types don't require ALTER TABLE
- Field definitions stored in `custom_field_definitions` per project

### Workspace-Level Multi-Tenancy

Every table includes `workspace_id`. Combined with PostgreSQL RLS policies, this provides:
- Query-level data isolation
- Defense-in-depth (even if application code has a bug, RLS prevents cross-tenant data access)
- NATS subjects are scoped per workspace
- S3 paths are prefixed per workspace

### Event Bus Over Direct Communication

Agents communicate via the NATS event bus rather than calling each other:
- Decoupled — agents don't need to know about each other
- Persistent — messages survive restarts
- Replayable — new agents can catch up on history
- Filtered — subscribe to specific event types and projects
