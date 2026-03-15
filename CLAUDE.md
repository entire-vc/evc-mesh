# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Entire VC Mesh** — task management platform for coordinating humans and AI agents (Claude Code, OpenClaw, Cline, Aider) in a unified workspace. Kanban system with customizable fields/workflows, dual interface (Web UI for humans, MCP/REST API for agents), and event bus for inter-agent context sharing.

**Status:** Phase 1-12 OSS complete. Preparing for public release. Enterprise features in separate repo.

**What's built:** REST API (~125 routes) + React frontend (19 pages, 16 stores) + MCP server (45 tools, stdio + SSE) + Agent Dashboard + Org Chart + NATS JetStream event bus + WebSocket real-time + Timeline DAG + Custom Fields (12 types) + Board/List/Timeline/Calendar views + Webhooks + Analytics + Initiatives + Rules engine + Config import/export + Go SDK + Atomic Task Checkout + Auto-Transition Rules + Project-Level Membership.

**Next:** Pre-release hardening, documentation polish, OpenAPI completeness.

**Repo strategy:** `evc-mesh` (Apache 2.0, goes public at release) + `evc-mesh-enterprise` (Commercial, always private).

## Documentation

Two documentation directories with different purposes:

### `dev-docs/` — Internal development docs (gitignored, NOT in public repo)

| Document | Path | Purpose | Update Policy |
|----------|------|---------|---------------|
| PRD | `dev-docs/rnd/PRD_AgentDesk.md` | Full product spec: data model, API, MCP tools, event bus, auth, billing | When requirements change |
| Research Report | `dev-docs/rnd/deep-research-report.md` | Market analysis, licensing decisions, architecture rationale | Reference only |
| Requirements | `dev-docs/REQUIREMENTS.md` | Structured ТЗ (extracted from PRD) | When requirements change |
| Roadmap | `dev-docs/ROADMAP.md` | Work plan and task status | **After completing any task** |
| Feature Specs | `dev-docs/specs/` | Specifications per feature | Before implementing features |
| ADRs | `dev-docs/adrs/` | Architecture Decision Records | For significant technical decisions |
| Brand Assets | `dev-docs/assets/` | Logos and icons (SVG + PNG) | Reference only |

**Always read the PRD before implementing any feature** — it contains the complete data model (12 tables), REST API spec (~40 endpoints), MCP tool definitions (20+ tools), event bus design, and auth/billing integration details.

### `docs/` — Public documentation (in repo, for OSS community)

User-facing docs for the open-source (non-enterprise) version: installation guide, self-hosting, API reference, contributing guide, etc.

### Documentation Rules

1. **Before starting work**: Read dev-docs/REQUIREMENTS.md and dev-docs/ROADMAP.md
2. **After completing task**: Update dev-docs/ROADMAP.md status
3. **New feature**: Create spec in dev-docs/specs/ first
4. **Architecture decision**: Create ADR in dev-docs/adrs/
5. **Never put internal/enterprise docs in `docs/`** — that directory is public

## Architecture

Two-layer system:

1. **Work Management Plane** (human-facing) — React web UI with kanban boards, configurable statuses per project, custom fields (JSONB), task dependencies, comments, artifacts
2. **Agent Collaboration Plane** (agent-facing) — MCP Server (stdio + HTTP SSE), REST API (`/api/v1`), Event Bus (NATS JetStream) for inter-agent context sharing, WebSocket for real-time

All data is multi-tenant via `workspace_id` on every table. Row-level security in PostgreSQL as defense-in-depth.

```
Clients (Web UI / REST / MCP agents)
         ↓
API Gateway (Go + Echo) — Auth (Casdoor JWT / Agent API Key) + Rate Limiter + WebSocket Hub
         ↓
Core Services (Go) — Project, Task, Agent, EventBus, Comment, Artifact, Billing, Notification
         ↓
Data Layer — PostgreSQL 16 (JSONB) | Redis 7 (cache/pubsub) | NATS JetStream (event bus) | S3/MinIO (artifacts)
```

## Planned Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend | Go 1.22+, Echo framework |
| Database | PostgreSQL 16 (JSONB for custom fields, GIN indices) |
| Cache/PubSub | Redis 7 |
| Event Bus | NATS JetStream (persistence, replay, at-least-once) |
| Frontend | React 19 + TypeScript + Tailwind CSS + Zustand |
| WebSocket | coder/websocket (fork of nhooyr) |
| MCP Server | mcp-go SDK |
| Auth/IAM | Casdoor (OIDC/OAuth 2.1) — https://login.entire.vc |
| Billing | evc-billing (enterprise only) |
| File Storage | S3-compatible (MinIO for self-hosted) |
| UI Kit | evc-brandkit (https://github.com/entire-vc/evc-brandkit) — тема Mesh |
| Agent Catalog | Spark (интеграция, не встроенный marketplace) |
| Migrations | goose |
| Deployment | Docker + docker-compose (K8s отложен до реальной необходимости) |

## Key Data Model Conventions

- **PKs:** UUID everywhere (not integer — designed for distributed systems)
- **Timestamps:** TIMESTAMPTZ (no timezone ambiguity)
- **Custom fields:** JSONB column on `tasks` table, definitions in `custom_field_definitions` per project
- **Soft deletes:** via `deleted_at` column, repos filter `deleted_at IS NULL`
- **Task statuses:** customizable per project with semantic categories (`backlog`, `todo`, `in_progress`, `review`, `done`, `cancelled`) — agents use categories, not status names
- **Auth:** Bearer JWT for users, `X-Agent-Key: agk_{workspace_slug}_{random}` for agents (bcrypt hashed)
- **Multi-tenancy:** `workspace_id` on every table, NATS subjects per workspace, S3 prefixes per workspace

## API Design Principles

- REST API at `/api/v1` with nested resources: `/workspaces/:ws_id/projects`, `/projects/:proj_id/tasks`
- MCP tools are outcome-oriented (e.g., `move_task` not `update_field`), designed for LLM safety
- Event bus subjects: `events.{workspace_id}.{project_id}.{event_type}`
- WebSocket at `/ws` with channel subscriptions (`project:uuid`, `task:uuid`, `eventbus:project:uuid`)
- Webhooks with HMAC-SHA256 signatures for external integrations

## Known Spec vs Implementation Gaps

Key differences between specs (dev-docs/specs/) and actual code:

- **Custom field routes**: Spec has nested `/projects/:id/custom-fields/:field_id`, impl uses flat `/custom-fields/:field_id`
- **Custom field options**: Spec has `{value, label, color}[]`, impl supports both formats (auto-detect)
- **Timeline**: Spec describes Gantt-like view; implemented as DAG dependency graph
- **Auth**: DualAuth middleware accepts JWT OR Agent Key on all routes. Casdoor OIDC deferred to Phase 5

Resolved gaps (Phase 4.1):
- ~~Artifact S3 stub~~ → real S3 client wired
- ~~Agent CRUD~~ → PATCH/DELETE/regenerate-key + GET /agents/me implemented
- ~~Auth logout~~ → POST /auth/logout revokes refresh tokens
- ~~CF filtering~~ → server-side JSONB queries with slug regex validation
- ~~Soft delete~~ → implemented for agents, repos filter deleted_at IS NULL
- ~~Activity logging~~ → task CRUD writes to activity_log (task.created/updated/deleted/moved/assigned)

Always check actual code, not just specs, when implementing features.

## Licensing & Repository Structure

**Dual-license:** Apache 2.0 (core) + Commercial (enterprise).

| Repo | Visibility | License | Content |
|------|-----------|---------|---------|
| `@entire-vc/evc-mesh` | Private -> Public at release | Apache 2.0 | Core: backend + frontend |
| `@entire-vc/evc-mesh-enterprise` | Always private | Commercial | Enterprise extensions (Casdoor SSO, billing, advanced RBAC) |
| `@entire-vc/evc-mesh-mcp` | Public | MIT | MCP server (separate package) |
| `@entire-vc/evc-mesh-openclawskill` | Public | MIT | OpenClaw skill |

- `docs/` — public OSS documentation (goes into public repo)
- `dev-docs/` — internal dev docs (gitignored, never public)

## Language

- Documentation and user-facing text: Russian
- Code, comments, variable names, API fields: English

## Agent Context Protocol (MCP)

At the **start of every session**, follow the ACP:

```
1. mcp__evc-mesh__get_me()                              # Who am I?
2. mcp__evc-mesh__get_project_knowledge(project_id)     # Accumulated decisions & conventions
3. mcp__evc-mesh__get_my_rules()                        # Workflow constraints
4. mcp__evc-mesh__get_context(project_id, since="24h")  # Recent activity
5. mcp__evc-mesh__get_my_tasks(status_category: "todo") # My work queue
   mcp__evc-mesh__get_my_tasks(status_category: "in_progress")
```

If there are tasks — report them to the user before proceeding.
After fixing a task: `mcp__evc-mesh__move_task` → "review", `mcp__evc-mesh__add_comment` with summary.
**Never close tasks yourself** — move to "review" and reassign to the creator.

### Memory Usage

- When making a decision: `mcp__evc-mesh__remember(key="decision-xxx", content="...", scope="project", tags=["decision"])`
- When finding a convention: `mcp__evc-mesh__remember(key="convention-xxx", content="...", scope="project")`
- When learning a user preference: `mcp__evc-mesh__remember(key="pref-xxx", content="...", scope="agent")`
- To search knowledge: `mcp__evc-mesh__recall(query="API convention")`
- At session end: `mcp__evc-mesh__publish_event(type="summary", memory={persist: true})`

## Agents

| Agent | When to Use |
|-------|-------------|
| `developer` | Write/fix/refactor code (Go backend, React frontend, MCP server, migrations) |

## Skills (Slash Commands)

| Command | Description |
|---------|-------------|
| `/ship` | **REQUIRED before every push.** Rebase, full tests, self-review, push branch, create PR, update Mesh task |
| `/review` | Self-review checklist — Go backend + React frontend. Run before /ship |
| `/check` | Quick quality check (go build + test + lint + tsc + frontend build) |
| `/deploy [component]` | Deploy to tw-mesh (backend/frontend/all) |
| `/db-migrate [action]` | Database migrations via goose (up/create/status) |
| `/status` | Project status: git, Mesh tasks, roadmap, services health |

## Task Workflow (Autonomous Agents)

### Phase 1: UNDERSTAND
- Read task description + comments: `mcp__evc-mesh__get_task`, `mcp__evc-mesh__list_comments`
- Read relevant code and tests
- Post plan as task comment: `mcp__evc-mesh__add_comment`

### Phase 2: CODE
- Branch: `daedalus/<short-description>` (NEVER work on main)
- TDD: write failing test → implement → verify
- Commit style: conventional commits (`feat:`, `fix:`, `refactor:`, `test:`)

### Phase 3: SELF-REVIEW
Run `/review`. Check every item. Fix issues before proceeding.

### Phase 4: SHIP
Run `/ship`. This handles: rebase → tests → push → PR → Mesh update.

### Rules
- **NEVER push to main** — branches only
- **NEVER deploy to production** — only via /deploy with explicit approval
- **NEVER skip tests before push** — /ship enforces this
- If stuck > 20 min on same error → add comment, move to "blocked", pick next task
- Max 3 fix attempts per task → escalate to lead (add comment, assign to creator)

## Subagent Guidelines

When implementing features:
1. Use `pm-spec` agent for requirement analysis
2. Use `architect` agent for design decisions
3. Use `developer` agent for coding
4. Use `tester` agent for verification

## Autonomous Execution Mode

При фразах "делай по шагам, не спрашивай", "вернись с результатом", "работай автономно":

1. **Не спрашивай** — бери из REQUIREMENTS.md, ROADMAP.md, PRD, существующего кода
2. **Решай сам** — выбирай по паттернам проекта, логируй решения
3. **Batch-режим** — объединяй операции (lint+build+test)
4. **Возвращайся** — только с результатом или критической ошибкой

**Исключения**: деструктивные операции, production, нет scope задачи
