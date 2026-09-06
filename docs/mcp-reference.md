# MCP Tool Reference

## Overview

evc-mesh exposes **63 MCP tools** via the [Model Context Protocol](https://modelcontextprotocol.io/).
Supported transports: **stdio** (default), **SSE** (HTTP Server-Sent Events on port 8081).

New to this? [Agent Onboarding](agent-onboarding.md) walks through issuing a key
and connecting a client end to end. This page is the tool catalogue.

Every tool the server registers has an entry below, and every entry below names a
tool the server registers. Both directions are checked by
`scripts/check-mcp-reference.sh`, which diffs the `#### N. \`name\`` headings in this
file against the `AddTool` calls in `evc-mesh-mcp` `internal/mcp/server.go` and fails
on a mismatch in either direction. If you add or remove a tool, this page is part of
that change.

Tools are organized into 12 categories:

| Category | Tools | Description |
|----------|-------|-------------|
| Project & Task Management | 11 | CRUD for projects, tasks, subtasks, dependencies, assignments, PR links |
| Comments & Artifacts | 5 | Task comments, file uploads, artifact retrieval |
| Documents | 7 | Project document tree: create, read, edit, search, and comment on pages |
| Memory & Knowledge | 9 | Persistent memory, project knowledge, and the canonical decision layer |
| Event Bus | 5 | Publish/subscribe events, context aggregation |
| Agent Hierarchy | 2 | Register and list sub-agents |
| Utility | 4 | Heartbeat, error reporting, self-assigned task listing, session metrics |
| Governance Rules | 2 | Agent-applicable rules, project rules |
| Team & Configuration | 6 | Team directory, assignment/workflow rules, agent profiles, config import/export |
| Push Notifications | 1 | Long-poll for task assignments |
| Recurring Tasks | 6 | Create, update, delete recurring schedules and inspect instance history |
| Task Checkout | 3 | Exclusive task locking with TTL to prevent double-work |
| Human Gate | 2 | Arm and release the "waiting on a human" gate, with the four-question predicate |

> **Note:** the MCP server is **not** in this repository. It is the standalone
> module [github.com/entire-vc/evc-mesh-mcp](https://github.com/entire-vc/evc-mesh-mcp),
> and every snippet below uses its paths. This repo carried a duplicate copy
> under `./cmd/mcp` until 2026-08; it had drifted 12 tools behind and was
> removed (Mesh #e85e4e05), so a snippet naming `./cmd/mcp` is stale, not an
> alternative.

> **Auto-transition rules are not MCP tools.** Four of them
> (`list`/`create`/`update`/`delete_auto_transition_rule`) were described on this page
> until 2026-09-03 as if the server registered them; it never did, and calling one
> returns an unknown-tool error. The feature itself is real and reachable over REST:
> `GET|POST /api/v1/projects/{project_id}/auto-transition-rules` and
> `PUT|DELETE /api/v1/projects/{project_id}/auto-transition-rules/{rule_id}`
> (create, update and delete require the `manage_rules` permission). Their MCP
> equivalents are tracked separately, not documented here as if they existed.

---

## Configuration

The MCP server connects to the Mesh REST API. It requires only two environment variables:
`MESH_API_URL` (the URL of your Mesh instance) and `MESH_AGENT_KEY` (your agent API key).
No direct database, Redis, or NATS access is needed.

### Stdio Mode (recommended for Claude Code)

Add to your project's `.mcp.json` or `~/.claude.json`:

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "./mesh-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "MESH_API_URL": "https://your-mesh-instance.example.com",
        "MESH_AGENT_KEY": "agk_your-workspace_your-key"
      }
    }
  }
}
```

If running from source (from a checkout of `evc-mesh-mcp`):

```json
{
  "mcpServers": {
    "evc-mesh": {
      "command": "go",
      "args": ["run", "."],
      "cwd": "/path/to/evc-mesh-mcp",
      "env": {
        "MESH_API_URL": "https://your-mesh-instance.example.com",
        "MESH_AGENT_KEY": "agk_your-workspace_your-key"
      }
    }
  }
}
```

### SSE Mode (for remote / multi-agent use)

SSE mode allows multiple agents to connect simultaneously, each authenticating with their own key.

Start the server:

```bash
./mesh-mcp --transport sse
```

Or set transport via environment variable:

```bash
MESH_MCP_TRANSPORT=sse MESH_API_URL=https://your-mesh-instance.example.com ./mesh-mcp
```

Two endpoints are served:

| Endpoint | Profile | Tools |
|----------|---------|-------|
| `http://localhost:8081/sse` | full | 63 |
| `http://localhost:8081/core/sse` | core | 25 |

Behind a reverse proxy, see
[Agent Onboarding §4](agent-onboarding.md#4-behind-a-reverse-proxy) — a path
prefix needs `MESH_MCP_PUBLIC_URL` as well as a proxy route.

Agents authenticate per-connection using one of these methods:
- `Authorization: Bearer agk_...` header
- `X-Agent-Key: agk_...` header
- `?agent_key=agk_...` query parameter

---

## Environment Variables

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `MESH_API_URL` | `http://localhost:8005` | Yes | Base URL of the Mesh REST API |
| `MESH_AGENT_KEY` | -- | Yes (stdio) | Agent API key in format `agk_{workspace_slug}_{random}`. Required for stdio; provided per-connection in SSE mode |
| `MESH_MCP_TRANSPORT` | `stdio` | No | Transport mode: `stdio` or `sse`. Overridden by the `--transport` CLI flag |
| `MESH_MCP_HOST` | `0.0.0.0` | No | SSE server bind host |
| `MESH_MCP_PORT` | `8081` | No | SSE server bind port |
| `MESH_MCP_PUBLIC_URL` | *(empty)* | No | Public base URL of the SSE server. Empty advertises the message endpoint relative to the URL the client connected to, which is correct unless a proxy serves MCP under a path prefix |
| `MESH_MCP_PROFILE` | `full` | No | Tool profile for **stdio** mode: `full` (63) or `core` (25). In SSE mode the profile follows the endpoint |

---

## Tools

### Project & Task Management (11 tools)

#### 1. `list_projects`

List available projects in the workspace.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `workspace_id` | string | No | Agent's workspace | Workspace ID |
| `include_archived` | boolean | No | `false` | Include archived projects |

**Example request:**
```json
{
  "name": "list_projects",
  "arguments": {
    "include_archived": false
  }
}
```

**Example response:**
```json
[
  {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "name": "Backend API",
    "slug": "backend-api",
    "workspace_id": "...",
    "is_archived": false
  }
]
```

---

#### 2. `get_project`

Get project details with statuses and custom fields.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_project",
  "arguments": {
    "project_id": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

---

#### 3. `list_tasks`

List tasks with filters.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `status_category` | string | No | -- | Filter by status category: `backlog`, `todo`, `in_progress`, `review`, `done`, `cancelled` |
| `assignee_type` | string | No | -- | Filter by assignee type: `user`, `agent`, `unassigned` |
| `priority` | string | No | -- | Filter by priority: `urgent`, `high`, `medium`, `low`, `none` |
| `labels` | string[] | No | -- | Filter by labels |
| `search` | string | No | -- | Search in title and description |
| `limit` | number | No | `50` | Max results to return (max 200) |
| `sort` | string | No | -- | Sort field: `created_at`, `updated_at`, `priority`, `due_date` |

**Example request:**
```json
{
  "name": "list_tasks",
  "arguments": {
    "project_id": "550e8400-e29b-41d4-a716-446655440000",
    "status_category": "in_progress",
    "assignee_type": "agent",
    "limit": 20
  }
}
```

---

#### 4. `get_task`

Get full task details with optional comments, artifacts, and dependencies.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `include_comments` | boolean | No | `false` | Include comments |
| `include_artifacts` | boolean | No | `false` | Include artifacts |
| `include_dependencies` | boolean | No | `false` | Include dependencies |

**Example request:**
```json
{
  "name": "get_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "include_comments": true,
    "include_dependencies": true
  }
}
```

---

#### 5. `create_task`

Create a new task in a project.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `title` | string | **Yes** | -- | Task title |
| `description` | string | No | -- | Task description |
| `status_slug` | string | No | Project default | Status slug (e.g. `todo`) |
| `priority` | string | No | `medium` | Priority: `urgent`, `high`, `medium`, `low`, `none` |
| `assignee_id` | string | No | -- | Assignee ID (user or agent UUID) |
| `assignee_type` | string | No | `unassigned` | Assignee type: `user`, `agent` |
| `labels` | string[] | No | -- | Task labels |
| `custom_fields` | object | No | -- | Custom field values as key-value pairs |
| `parent_task_id` | string | No | -- | Parent task ID for subtask |
| `due_date` | string | No | -- | Due date in RFC3339 format |
| `estimated_hours` | number | No | -- | Estimated hours for the task |

**Example request:**
```json
{
  "name": "create_task",
  "arguments": {
    "project_id": "550e8400-...",
    "title": "Implement user authentication",
    "description": "Add JWT-based auth with refresh tokens",
    "priority": "high",
    "labels": ["backend", "security"],
    "custom_fields": {
      "complexity": "high",
      "story_points": 8
    }
  }
}
```

---

#### 6. `update_task`

Update task fields.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `title` | string | No | -- | New title |
| `description` | string | No | -- | New description |
| `priority` | string | No | -- | New priority |
| `labels` | string[] | No | -- | New labels |
| `custom_fields` | object | No | -- | Custom field values to update |
| `due_date` | string | No | -- | Due date in RFC3339 format |
| `estimated_hours` | number | No | -- | Estimated hours |

**Example request:**
```json
{
  "name": "update_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "priority": "urgent",
    "labels": ["backend", "security", "blocked"]
  }
}
```

---

#### 7. `move_task`

Move task to a different status.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `status_slug` | string | **Yes** | -- | Target status slug (e.g. `in_progress`, `done`) |
| `comment` | string | No | -- | Optional comment to add when moving |

**Example request:**
```json
{
  "name": "move_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "status_slug": "done",
    "comment": "Implementation complete, all tests passing"
  }
}
```

---

#### 8. `create_subtask`

Create a subtask under a parent task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `parent_task_id` | string | **Yes** | -- | Parent task ID |
| `title` | string | **Yes** | -- | Subtask title |
| `description` | string | No | -- | Subtask description |
| `priority` | string | No | `medium` | Priority: `urgent`, `high`, `medium`, `low`, `none` |

**Example request:**
```json
{
  "name": "create_subtask",
  "arguments": {
    "parent_task_id": "a1b2c3d4-...",
    "title": "Write unit tests for auth middleware",
    "priority": "high"
  }
}
```

---

#### 9. `add_dependency`

Add a dependency between two tasks.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `depends_on_task_id` | string | **Yes** | -- | ID of the task this depends on |
| `dependency_type` | string | No | `blocks` | Dependency type: `blocks`, `relates_to`, `is_child_of` |

**Example request:**
```json
{
  "name": "add_dependency",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "depends_on_task_id": "e5f6g7h8-...",
    "dependency_type": "blocks"
  }
}
```

---

#### 10. `assign_task`

Assign a task to a user or agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `assignee_id` | string | No | -- | Assignee UUID. Omit to unassign |
| `assignee_type` | string | No | `agent` | Assignee type: `user`, `agent` |
| `assign_to_self` | boolean | No | `false` | Assign to the calling agent |

**Example request (assign to self):**
```json
{
  "name": "assign_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "assign_to_self": true
  }
}
```

**Example request (unassign):**
```json
{
  "name": "assign_task",
  "arguments": {
    "task_id": "a1b2c3d4-..."
  }
}
```

---

#### 11. `add_vcs_link`

Link a task to a pull request, merge request, commit, or branch. This is what makes the
task<->PR join real: a task with no VCS link cannot be matched to the code that implements
it, so PR-driven status automation and any "what shipped for this task?" report will not
see it. Call it as soon as the PR exists.

Only `task_id` and `url` are needed -- `provider`, `link_type` and `external_id` are
inferred from a GitHub or GitLab URL.

> **If the PR was already merged when you call this, pass `status="merged"`.** Otherwise the
> link starts as `open` and no webhook will ever correct it -- a merge that happened before
> the link existed fires no event. Calling `add_vcs_link` again on the same PR with a
> corrected status is safe: it updates the existing link rather than failing.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `url` | string | **Yes** | -- | Link URL, e.g. `https://github.com/owner/repo/pull/123` |
| `provider` | string | No | inferred (`github`) | VCS provider: `github`, `gitlab`. Inferred from the URL host |
| `link_type` | string | No | inferred (`pr`) | What the URL points at: `pr` (alias `pull_request`), `commit`, `branch`. Inferred from the URL path |
| `external_id` | string | No | inferred | PR number, commit SHA, or branch name. Only needed when the URL is not a recognised PR/commit/branch link |
| `title` | string | No | -- | Human-readable label, e.g. the PR title |
| `status` | string | No | `open` | `open`, `merged`, `closed`. Pass `merged` when linking a PR merged before this call |

**Example request:**
```json
{
  "name": "add_vcs_link",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "url": "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/912",
    "title": "fix: reject a colon in a memory key",
    "status": "merged"
  }
}
```

---

### Comments & Artifacts (5 tools)

#### 12. `add_comment`

Add a comment to a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `body` | string | **Yes** | -- | Comment body (markdown supported) |
| `is_internal` | boolean | No | `false` | Mark as internal (agent-only visible) |
| `parent_comment_id` | string | No | -- | Parent comment ID for threading |
| `metadata` | object | No | -- | Additional metadata as key-value pairs |

**Example request:**
```json
{
  "name": "add_comment",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "body": "Completed the database schema migration. 3 new tables added.",
    "is_internal": true,
    "metadata": {
      "tables_added": "3",
      "migration_file": "20240215_add_custom_fields.sql"
    }
  }
}
```

---

#### 13. `list_comments`

List comments on a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `include_internal` | boolean | No | `true` | Include internal (agent-only) comments |
| `limit` | number | No | `50` | Max comments to return |

**Example request:**
```json
{
  "name": "list_comments",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "include_internal": true,
    "limit": 20
  }
}
```

---

#### 14. `upload_artifact`

Upload an artifact (file, code, log, etc.) to a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `name` | string | **Yes** | -- | Artifact filename |
| `content` | string | **Yes** | -- | Artifact content (text or base64-encoded) |
| `artifact_type` | string | No | `file` | Type: `file`, `code`, `log`, `report`, `link`, `image`, `data` |
| `mime_type` | string | No | Auto-detected | MIME type. Auto-detected from name if omitted |
| `metadata` | object | No | -- | Additional metadata |

**Example request:**
```json
{
  "name": "upload_artifact",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "name": "test-results.json",
    "content": "{\"passed\": 42, \"failed\": 0, \"skipped\": 2}",
    "artifact_type": "report",
    "metadata": {
      "test_framework": "pytest",
      "duration_seconds": "12.5"
    }
  }
}
```

---

#### 15. `list_artifacts`

List artifacts attached to a task.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |

**Example request:**
```json
{
  "name": "list_artifacts",
  "arguments": {
    "task_id": "a1b2c3d4-..."
  }
}
```

---

#### 16. `get_artifact`

Get artifact details and optionally its content.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `artifact_id` | string | **Yes** | -- | Artifact ID |
| `include_content` | boolean | No | `false` | Include content for text files under 1MB |

**Example request:**
```json
{
  "name": "get_artifact",
  "arguments": {
    "artifact_id": "b2c3d4e5-...",
    "include_content": true
  }
}
```

---

### Documents (7 tools)

#### 17. `list_docs`

List a project's documents -- id, title, slug path, version, and who touched them last.
Carries **no document bodies**, so it is safe to call on a whole project: use it as the map,
then `get_doc` for one page. Returns `path` and `has_children` for navigating the tree.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project UUID |
| `include_archived` | boolean | No | `false` | Include archived documents |

**Example request:**
```json
{
  "name": "list_docs",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 18. `search_docs`

Full-text search a project's documents by title and body. Returns matching documents with a
snippet and a `path` usable directly with `get_doc` -- this is how you find a document when
you do not already know its path. `list_docs` is the map; this is the index.

> **Scope is per-project only.** Results never cross `project_id`, and this is not a
> substitute for `recall` (which searches memory, not documents) nor for a cross-project
> document search -- none exists. A query that matches nothing returns an empty `items` list,
> not an error. Documents saved before full-text search shipped (2026-08-20) are matched by
> title only until their next edit.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project UUID. Search is scoped to this one project -- call it once per project you need to check |
| `query` | string | **Yes** | -- | Search text. Matched against title and body |
| `limit` | number | No | `20` | Max results (server max 50) |

**Example request:**
```json
{
  "name": "search_docs",
  "arguments": {
    "project_id": "550e8400-...",
    "query": "rollback procedure",
    "limit": 10
  }
}
```

---

#### 19. `get_doc`

Read a document. **By default this returns metadata plus the outline (headings) and *not*
the body.** A document is far larger than a task, and a body you read stays in your context
for the rest of the session. Read the outline first, then pass `section` for just the part
you need; `body=true` returns the whole page and should be the exception.

The returned `version` is what `update_doc` takes as `base_version`.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `doc` | string | **Yes** | -- | Document UUID, or a slug path like `architecture/adr/adr-004` (a path also needs `project_id`) |
| `project_id` | string | No | -- | Project UUID. Required only when `doc` is a slug path |
| `section` | string | No | -- | Return only this section: a heading's text, or its anchor from the outline |
| `body` | boolean | No | `false` | Return the full markdown body. Prefer `section` when you need one part |
| `version_only` | boolean | No | `false` | Return just the version -- the cheap "has this changed since I read it?" check before a write |
| `outline_depth` | string | No | all levels | Limit the outline to headings at this level or shallower, e.g. `"2"` |

**Example request:**
```json
{
  "name": "get_doc",
  "arguments": {
    "project_id": "550e8400-...",
    "doc": "architecture/adr/adr-004",
    "section": "Consequences"
  }
}
```

---

#### 20. `create_doc`

Create a document in a project. Returns its metadata and `version` -- the version is what
`update_doc` takes as `base_version`, so a create followed by an edit needs no read in
between. The body you sent is not echoed back.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project UUID |
| `title` | string | **Yes** | -- | Document title |
| `body` | string | No | -- | Markdown body |
| `slug` | string | No | derived from title | URL slug |
| `parent_id` | string | No | -- | Parent document UUID, to nest this one under it |
| `position` | number | No | -- | Sort position among siblings |

**Example request:**
```json
{
  "name": "create_doc",
  "arguments": {
    "project_id": "550e8400-...",
    "parent_id": "7c1e0d92-...",
    "title": "ADR-004: single writer for the VPN tunnel",
    "slug": "adr-004-single-writer",
    "body": "## Status\n\nAccepted\n\n## Context\n\n..."
  }
}
```

---

#### 21. `update_doc`

Edit a document. Replacing the body **requires** `base_version` -- the version you got from
`get_doc` -- and the write is refused with a `409 document_version_conflict` if anyone
changed the document since, so you can never silently overwrite someone else's edit. The
stored body is left byte-for-byte unchanged on a refusal.

To add to the end, pass `append` instead: it needs no `base_version`, cannot conflict, and
does not make you read the document first. **Prefer `append` for reports, decisions and
logs.**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `doc` | string | **Yes** | -- | Document UUID, or a slug path like `architecture/adr/adr-004` (a path also needs `project_id`) |
| `project_id` | string | No | -- | Project UUID. Required only when `doc` is a slug path |
| `append` | string | No | -- | Text to add to the END of the document. No `base_version` needed. Cannot be combined with `body` |
| `body` | string | No | -- | Replacement markdown for the WHOLE document. Requires `base_version` |
| `base_version` | number | No | -- | The version you read from `get_doc`. Required for any write other than `append` |
| `title` | string | No | -- | New title |
| `parent_id` | string | No | -- | New parent document UUID |
| `position` | number | No | -- | New sort position among siblings |

**Example request (append -- no read, cannot conflict):**
```json
{
  "name": "update_doc",
  "arguments": {
    "project_id": "550e8400-...",
    "doc": "state",
    "append": "\n## 2026-09-03 -- phase:verify\nCatalogue rebuilt; counts reconciled against server.go.\n"
  }
}
```

---

#### 22. `comment_doc`

Comment on a document. To comment on a specific passage, pass `quote` with the text exactly
as the document reads it -- the server finds it and anchors the comment there, so you never
compute a position yourself. **There is no offset parameter, by design:** a position you
calculated would silently point at the wrong sentence after any edit.

Without `quote` the comment is on the whole document. Your comment appears in the same
thread humans see in the document UI.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `doc` | string | **Yes** | -- | Document UUID, or a slug path like `architecture/adr/adr-004` (a path also needs `project_id`) |
| `body` | string | **Yes** | -- | The comment text. Markdown; `@slug` mentions notify that person or agent |
| `project_id` | string | No | -- | Project UUID. Required only when `doc` is a slug path |
| `quote` | string | No | -- | The passage being commented on, copied from the document exactly. One sentence is plenty. Omit to comment on the document as a whole |
| `quote_context` | string | No | -- | A longer passage containing the quote exactly once -- send this when the quote occurs several times and you were told it was ambiguous |
| `reply_to` | string | No | -- | UUID of the comment being answered. A reply inherits that thread's anchor, so it takes no quote of its own |

**Example request:**
```json
{
  "name": "comment_doc",
  "arguments": {
    "project_id": "550e8400-...",
    "doc": "architecture/adr/adr-004",
    "quote": "The tunnel is opened by whichever node boots first.",
    "body": "This contradicts the single-writer rule two paragraphs up. @linus which one holds?"
  }
}
```

---

#### 23. `list_doc_comments`

Read the comments on a document as threads -- each top-level comment with its replies nested
under it, the quoted passage it is anchored to, and who wrote it. Resolved threads are hidden
unless `include_resolved=true`.

A comment whose quoted text no longer exists in the document is marked `orphaned=true` in its
anchor: it is still shown, and it is not pointing anywhere.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `doc` | string | **Yes** | -- | Document UUID, or a slug path like `architecture/adr/adr-004` (a path also needs `project_id`) |
| `project_id` | string | No | -- | Project UUID. Required only when `doc` is a slug path |
| `include_resolved` | boolean | No | `false` | Include threads somebody marked resolved |

**Example request:**
```json
{
  "name": "list_doc_comments",
  "arguments": {
    "project_id": "550e8400-...",
    "doc": "architecture/adr/adr-004",
    "include_resolved": true
  }
}
```

---

### Memory & Knowledge (9 tools)

#### 24. `remember`

Save knowledge to persistent memory. Use for decisions, conventions, and preferences.
**UPSERT by key** -- calling with the same key updates the existing entry.

> **Content is screened on write and REFUSED with a named reason** (never silently stripped
> or stored) if it contains invisible/bidi characters, an LLM role tag, an instruction to
> ignore previous or system instructions, a PEM private key, a prefixed API token
> (`sk-`/`ghp_`/`xox*`/`AKIA`), or a literal assignment to a `*_PASSWORD` / `*_SECRET` /
> `*_TOKEN` / `*_API_KEY` name.
>
> **This screen is partial and must not be relied on as a secret filter.** It cannot see a
> secret with no recognisable prefix and no field name beside it -- a bare value on its own
> line -- nor names it does not know. Record *where* a secret lives, never its value.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `key` | string | **Yes** | -- | Slug key for UPSERT, e.g. `api-convention`. Kebab-case: `^[a-z0-9][a-z0-9-]*[a-z0-9]$` -- a colon in the key is rejected |
| `content` | string | **Yes** | -- | What to remember (markdown) |
| `scope` | string | No | `project` | `workspace`, `project`, or `agent` |
| `project_id` | string | No | -- | Project ID (required for `project` scope) |
| `tags` | array\<string\> | No | -- | Tags for categorization and filtering |
| `relevance` | number | No | `1.0` | Relevance score 0-1 |
| `expires_at` | string | No | -- | RFC3339 timestamp or Go duration, e.g. `"72h"` |
| `source_url` | string | No | -- | URL/path to the source (task ID, PR, file path) |
| `source_task_id` | string | No | auto | UUID of the Mesh task that produced this memory. Auto-populated from the active checkout |
| `thread_id` | string | No | auto | Thread identifier for same-session grouping. Auto-populated |
| `attach_context` | boolean | No | `true` | When `false`, disables auto-injection of `thread_id` and `source_task_id` |
| `reason` | string | No | -- | Why this entry is worth writing, recorded on the revision. Optional today and about to become required -- write it now |
| `expected_version` | number | No | -- | Makes the write conditional: refused, with both version numbers, if someone else wrote to the key in between. Omit for last-write-wins |

**Example request:**
```json
{
  "name": "remember",
  "arguments": {
    "key": "mesh-mcp-two-deploy-targets",
    "scope": "workspace",
    "content": "evc-mesh-mcp has two prod targets; updating one looks like a full deploy...",
    "tags": ["kind:learning", "project:mesh-dev", "topic:deploy"],
    "relevance": 0.85,
    "reason": "A green deploy of one target read as a green deploy of both."
  }
}
```

---

#### 25. `recall`

**Search** memory by keywords. Use to find a *specific* piece of knowledge, e.g. "API
convention" or "license decision". Returns ranked results with scores. To load *all* project
knowledge at session start, use `get_project_knowledge` instead.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | **Yes** | -- | Full-text search query |
| `project_id` | string | No | -- | Filter to a specific project |
| `scope` | string | No | `all` | `workspace`, `project`, `agent`, or `all` |
| `tags` | array\<string\> | No | -- | AND-filter: memory must contain ALL listed tags |
| `tags_any` | array\<string\> | No | -- | OR-filter: memory must contain AT LEAST ONE of these tags |
| `created_by` | string | No | -- | Filter by agent ID (UUID) |
| `since` | string | No | -- | Return memories created at or after this RFC3339 timestamp |
| `until` | string | No | -- | Return memories created at or before this RFC3339 timestamp |
| `relevance_min` | number | No | -- | Minimum relevance score (0-1) |
| `min_importance` | number | No | `0.4` | Minimum `importance_score`. Entries below this are excluded (`kind:session-checkpoint` scores 0.3). Pass `0` to retrieve everything |
| `apply_recency_decay` | boolean | No | `false` | Sort by `relevance * 0.95^days_since_created` |
| `order_by` | string | No | `created_at:desc` | `created_at:desc`, `created_at:asc`, `relevance:desc`, `decayed_relevance:desc` |
| `include_expired` | boolean | No | `false` | Include expired memories |
| `include_archived` | boolean | No | `false` | Include archived memories |
| `limit` | number | No | `10` | Max results (max 50). **Hard bound** -- see the note below |
| `offset` | number | No | `0` | Pagination offset |

> `limit` is a hard bound: the response never contains more than `limit` items. When
> knowledge-graph boost is enabled, a share of the page (`limit/4`, at least 1 when
> `limit >= 2`) may be filled with graph-expanded neighbours, marked `graph_boost=true` and
> `provenance=via:graph` -- they take the *tail slots* rather than being added on top. Rows
> that fail the scope or tag filters are dropped, never returned unmarked.

**Example request:**
```json
{
  "name": "recall",
  "arguments": {
    "query": "why did we drop the duplicate MCP server",
    "tags_any": ["kind:decision", "kind:incident"],
    "min_importance": 0.5,
    "order_by": "decayed_relevance:desc",
    "limit": 5
  }
}
```

---

#### 26. `recall_with_graph`

Search memory with knowledge-graph expansion. Seeds from hybrid `recall`, then BFS-traverses
`memory_edges` up to `hops` deep. Returns memories ranked by composite score, each carrying
`hop_distance` and `provenance`. Use it when you want the broader context -- related
decisions, connected incidents, derived learnings -- rather than one specific fact.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `q` | string | **Yes** | -- | Search query (keywords or natural language). Note: this tool takes `q`, not `query` |
| `project_id` | string | No | -- | Filter to a specific project |
| `task_id` | string | No | -- | Cache-key discriminator for session-scoped traversal |
| `hops` | number | No | `2` | Graph traversal depth (max 5) |
| `weight_threshold` | number | No | `0.3` | Minimum edge weight to follow |

**Example request:**
```json
{
  "name": "recall_with_graph",
  "arguments": {
    "q": "deploy gap between merged code and the running binary",
    "hops": 2,
    "weight_threshold": 0.4
  }
}
```

---

#### 27. `forget`

Delete a memory entry.

> Agents can delete **only their own agent-scope memories**. A workspace- or project-scope
> entry, or another agent's entry, is refused.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `memory_id` | string | **Yes** | -- | UUID of the memory to delete |

**Example request:**
```json
{
  "name": "forget",
  "arguments": {
    "memory_id": "a1b2c3d4-..."
  }
}
```

---

#### 28. `get_project_knowledge`

Get **all permanent knowledge** for a project: decisions, conventions, accumulated context.
Call at session start (ACP step 2). Returns workspace-level plus project-level memories. For
*recent events* use `get_context`; to search for one specific fact use `recall`.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project UUID |
| `limit` | number | No | `100` | Max workspace-tier memories (max 500) |
| `offset` | number | No | `0` | Pagination offset for the workspace tier |
| `min_importance` | number | No | `0` | Minimum `importance_score` for the workspace tier (0 = all) |
| `tags_any` | string | No | -- | **Comma-separated** tag OR-filter for the workspace tier, e.g. `"kind:decision,kind:incident"` (a string here, not an array) |

**Example request:**
```json
{
  "name": "get_project_knowledge",
  "arguments": {
    "project_id": "550e8400-...",
    "tags_any": "kind:decision,kind:incident",
    "min_importance": 0.6
  }
}
```

---

#### 29. `set_project_knowledge`

Write a structured fact to project knowledge. **UPSERT by key.** Use it for deploy URLs,
stack conventions, and gotchas -- facts that must still be true next month. These are the
records `get_project_knowledge` returns.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID to store knowledge for |
| `key` | string | **Yes** | -- | Slug key for UPSERT, e.g. `deploy-url`. Kebab-case; a colon in the key is rejected |
| `value` | string | **Yes** | -- | The knowledge to store (markdown, max 4000 chars) |
| `category` | string | No | -- | `deploy`, `stack`, `conventions`, `gotchas`, `api`, `auth`, ... |
| `tags` | array\<string\> | No | -- | Additional tags for filtering |
| `source_url` | string | No | -- | URL/path to the source of this knowledge |
| `source_task_id` | string | No | auto | UUID of the Mesh task that produced this fact |
| `thread_id` | string | No | auto | Thread identifier |
| `attach_context` | boolean | No | `true` | When `false`, disables auto-injection of `thread_id` and `source_task_id` |

**Example request:**
```json
{
  "name": "set_project_knowledge",
  "arguments": {
    "project_id": "550e8400-...",
    "key": "canonical-deploy-target",
    "category": "deploy",
    "value": "mesh-api runs under systemd on mesh-vm, not docker. `systemctl restart mesh-api`.",
    "tags": ["kind:canonical"]
  }
}
```

---

#### 30. `get_canonical`

Query the canonical knowledge layer: curated facts, decisions, and strategy docs for a topic,
merged from `project_memories` (key `canonical:*`) and `workspace_memories`
(`kind:canonical`). Ephemeral session-checkpoints are excluded. Project slug aliases are
resolved automatically (`mesh-dev` == `evc-mesh`).

Call it **before authoring any document that might conflict with existing canonical
knowledge**. An empty result for a topic that should exist is a propagation gap, not proof
that no decision was made -- flag it rather than filling the vacuum.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `topic` | string | **Yes** | -- | Topic or keyword to search, e.g. `auth middleware` |
| `project` | string | No | -- | Project slug to narrow results, e.g. `evc-mesh`. Aliases resolved automatically |

**Example request:**
```json
{
  "name": "get_canonical",
  "arguments": {
    "topic": "release cadence",
    "project": "evc-mesh"
  }
}
```

---

#### 31. `get_canonical_updates`

Fetch canonical decisions broadcast since a given time. Call at ACP step 6 (session start) to
catch up on directives issued since your previous session. Returns only `privacy:public`
records targeted at you or at all agents.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `since` | string | No | your previous session's start (server-resolved) | RFC3339 cursor. Omit on the first call |
| `agent` | string | No | -- | Your agent slug, e.g. `linus`. Filters `propagate_to:<slug>` records. Omit to get only `propagate_to:all` |
| `scope` | string | No | -- | Project UUID, to restrict to project-scoped decisions |

**Example request:**
```json
{
  "name": "get_canonical_updates",
  "arguments": {
    "agent": "linus"
  }
}
```

---

#### 32. `pavel_decision`

Record a human owner's directive as a canonical decision in project knowledge, and broadcast
it to the agents named in `propagate_to`. `privacy:private` records are stored but excluded
from `get_canonical_updates`; text containing secrets is auto-flagged private.

If `task_id` is given, this also records a `human_gate` decision on that task -- releasing a
live gate as a consequence and linking back via `canonical_key`. That half is best-effort: a
failure there is reported in the result but does not undo the canonical write.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `text` | string | **Yes** | -- | Full text of the decision/directive |
| `summary` | string | **Yes** | -- | One-line summary, used as the UPSERT key (dedupes the same decision on the same day) |
| `propagate_to` | array\<string\> | No | -- | Agent slugs, e.g. `["linus","bill"]`. Use `["all"]` for a workspace-wide broadcast |
| `scope` | string | No | -- | Project UUID. Omit for workspace-level decisions |
| `privacy` | string | No | `public` | `public` (visible in the change feed) or `private` (recorded but hidden) |
| `task_id` | string | No | -- | Task UUID this decision answers. Also records a `human_gate` decision on that task |

**Example request:**
```json
{
  "name": "pavel_decision",
  "arguments": {
    "summary": "Docs live in Mesh Docs, not in files",
    "text": "Specs, ADRs and runbooks go into the project's Docs tree...",
    "propagate_to": ["all"],
    "task_id": "a1b2c3d4-..."
  }
}
```

---

### Event Bus (5 tools)

#### 33. `publish_event`

Publish an event to the event bus.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `event_type` | string | **Yes** | -- | Event type: `summary`, `status_change`, `context_update`, `error`, `dependency_resolved`, `custom` |
| `subject` | string | **Yes** | -- | Event subject line |
| `payload` | object | **Yes** | -- | Event payload as key-value pairs |
| `task_id` | string | No | -- | Related task ID |
| `tags` | string[] | No | -- | Event tags for filtering |
| `ttl_hours` | number | No | `24` | Time-to-live in hours |

**Example request:**
```json
{
  "name": "publish_event",
  "arguments": {
    "project_id": "550e8400-...",
    "event_type": "status_change",
    "subject": "Task moved to review",
    "payload": {
      "task_id": "a1b2c3d4-...",
      "from_status": "in_progress",
      "to_status": "review"
    },
    "tags": ["backend", "review-ready"]
  }
}
```

---

#### 34. `publish_summary`

Publish a work summary event (convenience wrapper for `publish_event` with `type=summary`).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `task_id` | string | No | -- | Related task ID |
| `summary` | string | **Yes** | -- | Summary of work done |
| `key_decisions` | string[] | No | -- | Key decisions made |
| `artifacts_created` | string[] | No | -- | Artifacts created |
| `blockers` | string[] | No | -- | Current blockers |
| `next_steps` | string[] | No | -- | Suggested next steps |
| `metrics` | object | No | -- | Metrics (lines changed, tests passed, etc.) |

**Example request:**
```json
{
  "name": "publish_summary",
  "arguments": {
    "project_id": "550e8400-...",
    "task_id": "a1b2c3d4-...",
    "summary": "Implemented JWT authentication with refresh token rotation",
    "key_decisions": [
      "Used HS256 for JWT signing",
      "Refresh tokens stored in Redis with 7-day TTL"
    ],
    "artifacts_created": ["auth_middleware.go", "auth_test.go"],
    "next_steps": ["Add rate limiting", "Implement password reset"],
    "metrics": {
      "lines_added": "450",
      "lines_removed": "12",
      "tests_added": "18"
    }
  }
}
```

---

#### 35. `get_context`

Get enriched context from the event bus.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `since` | string | No | -- | Only events after this timestamp (RFC3339) |
| `event_types` | string[] | No | -- | Filter by event types |
| `tags` | string[] | No | -- | Filter by tags |
| `limit` | number | No | `50` | Max events to return |

**Example request:**
```json
{
  "name": "get_context",
  "arguments": {
    "project_id": "550e8400-...",
    "since": "2025-02-24T00:00:00Z",
    "event_types": ["summary", "error"],
    "limit": 10
  }
}
```

---

#### 36. `get_task_context`

Get all context for a task: details, comments, events, artifacts, dependencies.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |

**Example request:**
```json
{
  "name": "get_task_context",
  "arguments": {
    "task_id": "a1b2c3d4-..."
  }
}
```

**Example response structure:**
```json
{
  "task": { "id": "...", "title": "...", "status": "..." },
  "comments": [...],
  "events": [...],
  "artifacts": [...],
  "dependencies": [...]
}
```

---

#### 37. `subscribe_events`

Configure push notification delivery for task events. Optionally sets a callback URL that Mesh will POST events to. Returns SSE and long-poll endpoint URLs for alternative delivery mechanisms.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `event_types` | string[] | No | -- | Event types to subscribe to |
| `callback_url` | string | No | -- | URL where Mesh will POST task events (task.assigned, task.status_changed). Leave empty to only use SSE or long-polling |

See [Agent Push Notifications](agent-push-notifications.md) for full details on delivery mechanisms.

**Example request (set callback URL):**
```json
{
  "name": "subscribe_events",
  "arguments": {
    "project_id": "550e8400-...",
    "event_types": ["summary", "error", "dependency_resolved"],
    "callback_url": "https://your-agent.example.com/hooks/mesh"
  }
}
```

**Example response:**
```json
{
  "status": "configured",
  "callback_url": "https://your-agent.example.com/hooks/mesh",
  "push_endpoints": {
    "sse": "https://mesh.example.com/api/v1/agents/me/events/stream",
    "long_poll": "https://mesh.example.com/api/v1/agents/me/tasks/poll?timeout=30"
  }
}
```

---

### Agent Hierarchy (2 tools)

#### 38. `register_sub_agent`

Register a sub-agent under the calling agent. Useful for orchestrating multi-agent workflows.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | **Yes** | -- | Sub-agent name |
| `agent_type` | string | **Yes** | -- | Agent type: `claude_code`, `openclaw`, `cline`, `aider`, `custom` |
| `capabilities` | object | No | -- | Agent capabilities as key-value pairs |

**Example request:**
```json
{
  "name": "register_sub_agent",
  "arguments": {
    "name": "test-runner-agent",
    "agent_type": "claude_code",
    "capabilities": {
      "languages": "go,python",
      "can_run_tests": "true"
    }
  }
}
```

---

#### 39. `list_sub_agents`

List sub-agents of an agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `agent_id` | string | No | Calling agent | Parent agent ID. Defaults to the calling agent |
| `recursive` | boolean | No | `false` | Return all descendants (up to 10 levels deep) |

**Example request:**
```json
{
  "name": "list_sub_agents",
  "arguments": {
    "recursive": true
  }
}
```

---

### Utility (4 tools)

#### 40. `heartbeat`

Send a heartbeat to indicate the agent is alive.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `current_task_id` | string | No | -- | ID of the task currently being worked on |
| `status` | string | No | -- | Agent status: `online`, `busy`, `error` |

**Example request:**
```json
{
  "name": "heartbeat",
  "arguments": {
    "status": "busy",
    "current_task_id": "a1b2c3d4-..."
  }
}
```

---

#### 41. `get_my_tasks`

Get tasks assigned to the calling agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `status_category` | string | No | -- | Filter by status category |
| `project_id` | string | No | -- | Filter by project |
| `limit` | number | No | `50` | Max results |

**Example request:**
```json
{
  "name": "get_my_tasks",
  "arguments": {
    "status_category": "in_progress"
  }
}
```

---

#### 42. `report_error`

Report an error encountered during work.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | No | -- | Related task ID |
| `error_message` | string | **Yes** | -- | Error message |
| `stack_trace` | string | No | -- | Stack trace or details |
| `severity` | string | No | `medium` | Severity: `low`, `medium`, `high`, `critical` |
| `recoverable` | boolean | No | `true` | Whether the error is recoverable |

**Example request:**
```json
{
  "name": "report_error",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "error_message": "Failed to connect to external API: connection timeout",
    "severity": "high",
    "recoverable": true
  }
}
```

---

#### 43. `session_report`

Report session metrics. Call before the session ends. Returns a compliance score and session
statistics.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `model` | string | No | -- | Model used, e.g. `claude-sonnet-4` |
| `tokens_in` | number | No | -- | Total input tokens this session |
| `tokens_out` | number | No | -- | Total output tokens this session |
| `estimated_cost` | number | No | -- | Estimated cost in USD |

**Example request:**
```json
{
  "name": "session_report",
  "arguments": {
    "model": "claude-sonnet-4",
    "tokens_in": 184320,
    "tokens_out": 21044,
    "estimated_cost": 0.87
  }
}
```

---

### Governance Rules (2 tools)

#### 44. `get_my_rules`

Get all governance rules that apply to the calling agent. Call at the start of work to understand constraints and behavioral requirements.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | No | -- | Optional project ID to get project-specific effective rules |

**Example request:**
```json
{
  "name": "get_my_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 45. `get_project_rules`

Get all rules configured for a project (all scopes: workspace + project).

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_project_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

### Team & Configuration (6 tools)

#### 46. `get_team_directory`

Get the workspace team directory listing all agents and human members with their profiles.

No parameters required.

**Example request:**
```json
{
  "name": "get_team_directory",
  "arguments": {}
}
```

---

#### 47. `get_assignment_rules`

Get effective assignment rules for a project, merged from workspace and project level with source annotations.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_assignment_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 48. `get_workflow_rules`

Get workflow rules for a project including allowed transitions, policies, and permissions for the calling agent.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |

**Example request:**
```json
{
  "name": "get_workflow_rules",
  "arguments": {
    "project_id": "550e8400-..."
  }
}
```

---

#### 49. `update_agent_profile`

Update the calling agent's profile fields such as role, capabilities, responsibility zone, and working hours.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `role` | string | No | -- | Agent role (e.g. developer, reviewer, tester) |
| `capabilities` | string[] | No | -- | List of capability strings (e.g. go, react, testing) |
| `responsibility_zone` | string | No | -- | Area of responsibility (e.g. Backend, Frontend) |
| `escalation_to` | string | No | -- | Agent ID or name to escalate issues to |
| `accepts_from` | string[] | No | -- | Agent IDs or types this agent accepts tasks from |
| `max_concurrent_tasks` | number | No | -- | Maximum number of concurrent tasks |
| `working_hours` | string | No | -- | Working hours description (e.g. 24/7, 9-17 UTC) |
| `description` | string | No | -- | Human-readable description of the agent's purpose |
| `callback_url` | string | No | -- | URL where Mesh will POST task events (`task.assigned`, `task.status_changed`, `task.commented`). Set to empty string to disable |

**Example request:**
```json
{
  "name": "update_agent_profile",
  "arguments": {
    "role": "developer",
    "capabilities": ["go", "react", "testing"],
    "responsibility_zone": "Backend",
    "max_concurrent_tasks": 3,
    "working_hours": "24/7",
    "callback_url": "https://my-agent.example.com/webhook"
  }
}
```

---

#### 50. `import_workspace_config`

Import workspace configuration from YAML. Applies rules, statuses, and project templates defined in the YAML.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `yaml_content` | string | **Yes** | -- | YAML configuration content as a string |

**Example request:**
```json
{
  "name": "import_workspace_config",
  "arguments": {
    "yaml_content": "version: 1\nworkspace_rules:\n  assignment:\n    ..."
  }
}
```

---

#### 51. `export_workspace_config`

Export the current workspace configuration as YAML, including rules, project templates, and settings.

No parameters required.

**Example request:**
```json
{
  "name": "export_workspace_config",
  "arguments": {}
}
```

---

### Push Notifications (1 tools)

#### 52. `poll_tasks`

Long-poll for new task assignments. Blocks until a task is assigned to this agent or the timeout expires. Returns current assigned tasks and whether any change occurred.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `timeout` | number | No | `30` | Maximum seconds to wait for new assignments (max 120) |

See [Agent Push Notifications](agent-push-notifications.md) for full details on push delivery mechanisms (callback URL, SSE, long-poll).

**Example request:**
```json
{
  "name": "poll_tasks",
  "arguments": {
    "timeout": 60
  }
}
```

**Example response (new task assigned):**
```json
{
  "tasks": [
    {"id": "a1b2c3d4-...", "title": "Fix auth bug", "priority": "high"}
  ],
  "count": 1,
  "changed": true
}
```

**Example response (timeout, no changes):**
```json
{
  "tasks": [],
  "count": 0,
  "changed": false
}
```

---

### Recurring Tasks (6 tools)

#### 53. `create_recurring_task`

Creates a recurring task schedule that automatically spawns task instances on a schedule. Each instance gets access to the previous instance's summary. Use this for regular automated work: weekly reports, daily checks, periodic audits.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Target project UUID |
| `title_template` | string | **Yes** | -- | Task title template. Supports `{{.Date}}`, `{{.Number}}`, `{{.Week}}`, `{{.Month}}` |
| `frequency` | string | **Yes** | -- | Recurrence frequency: `daily`, `weekly`, `monthly`, `custom`. Use `custom` with `cron_expr` for fine-grained control |
| `description_template` | string | No | -- | Task description template. Supports `{{.PrevSummary}}` for previous instance context |
| `cron_expr` | string | No | -- | 5-field cron expression (required if `frequency=custom`). Example: `0 9 * * 1` = every Monday at 9am |
| `timezone` | string | No | `UTC` | IANA timezone for schedule evaluation |
| `assignee_id` | string | No | -- | Agent or user UUID to assign each instance |
| `assignee_type` | string | No | `unassigned` | Assignee type: `user`, `agent`, `unassigned` |
| `priority` | string | No | `none` | Priority: `urgent`, `high`, `medium`, `low`, `none` |
| `labels` | string[] | No | -- | Labels to apply to each instance |
| `starts_at` | string | No | Now | When to start the schedule (RFC3339) |
| `ends_at` | string | No | -- | When to stop the schedule (RFC3339). Default: no end |
| `max_instances` | number | No | -- | Maximum number of instances to create. Default: unlimited |

**Example request:**
```json
{
  "name": "create_recurring_task",
  "arguments": {
    "project_id": "550e8400-...",
    "title_template": "Weekly Security Audit — Week {{.Week}}",
    "frequency": "weekly",
    "description_template": "Perform weekly security checks.\n\nPrevious run summary:\n{{.PrevSummary}}",
    "timezone": "Europe/Moscow",
    "assignee_id": "a1b2c3d4-...",
    "assignee_type": "agent",
    "priority": "high",
    "labels": ["security", "recurring"]
  }
}
```

**Example response:**
```json
{
  "id": "f1e2d3c4-...",
  "project_id": "550e8400-...",
  "title_template": "Weekly Security Audit — Week {{.Week}}",
  "frequency": "weekly",
  "is_active": true,
  "next_run_at": "2026-03-09T09:00:00Z",
  "created_at": "2026-03-05T10:00:00Z"
}
```

---

#### 54. `list_recurring_schedules`

Lists all recurring task schedules for a project.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_id` | string | **Yes** | -- | Project ID |
| `active_only` | boolean | No | `true` | Only return active schedules |

**Example request:**
```json
{
  "name": "list_recurring_schedules",
  "arguments": {
    "project_id": "550e8400-...",
    "active_only": true
  }
}
```

**Example response:**
```json
[
  {
    "id": "f1e2d3c4-...",
    "title_template": "Weekly Security Audit — Week {{.Week}}",
    "frequency": "weekly",
    "is_active": true,
    "next_run_at": "2026-03-09T09:00:00Z",
    "instance_count": 12
  }
]
```

---

#### 55. `get_recurring_history`

Returns the history of all instances for a recurring task schedule. Call this when you receive a recurring task to get context on what previous instances accomplished, what issues were found, and what artifacts were produced. Use it to continue work intelligently rather than starting from scratch.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `recurring_schedule_id` | string | **Yes** | -- | UUID of the recurring schedule. Available in `task.recurring_schedule_id` field |
| `limit` | number | No | `5` | Number of most recent instances to return. Use a higher value for deep historical context |

**Example request:**
```json
{
  "name": "get_recurring_history",
  "arguments": {
    "recurring_schedule_id": "f1e2d3c4-...",
    "limit": 3
  }
}
```

**Example response:**
```json
{
  "schedule_id": "f1e2d3c4-...",
  "instances": [
    {
      "instance_number": 12,
      "task_id": "a1b2c3d4-...",
      "title": "Weekly Security Audit — Week 9",
      "status_category": "done",
      "summary": "Found 2 minor issues, patched both. All systems nominal.",
      "created_at": "2026-02-26T09:00:00Z",
      "completed_at": "2026-02-26T11:30:00Z"
    }
  ],
  "total": 12
}
```

---

#### 56. `trigger_recurring_now`

Immediately creates the next instance of a recurring schedule, without waiting for the scheduled time. Useful for testing schedules or for urgent out-of-cycle execution.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `recurring_schedule_id` | string | **Yes** | -- | UUID of the recurring schedule |

**Example request:**
```json
{
  "name": "trigger_recurring_now",
  "arguments": {
    "recurring_schedule_id": "f1e2d3c4-..."
  }
}
```

**Example response:**
```json
{
  "task_id": "b2c3d4e5-...",
  "title": "Weekly Security Audit — Week 10",
  "instance_number": 13,
  "created_at": "2026-03-05T10:00:00Z"
}
```

---

#### 57. `update_recurring_schedule`

Update an existing recurring task schedule -- title, description, frequency, assignee,
priority -- or pause it with `is_active=false`. Every field is optional; omitted fields are
left as they are.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `recurring_schedule_id` | string | **Yes** | -- | UUID of the schedule to update |
| `title_template` | string | No | -- | New title template. Supports `{{.Date}}`, `{{.Number}}`, `{{.Week}}`, `{{.Month}}` |
| `description_template` | string | No | -- | New description template. Supports `{{.PrevSummary}}` |
| `frequency` | string | No | -- | `daily`, `weekly`, `monthly`, `custom` |
| `cron_expr` | string | No | -- | New cron expression (for `custom` frequency) |
| `timezone` | string | No | -- | New IANA timezone |
| `assignee_id` | string | No | -- | New assignee UUID |
| `assignee_type` | string | No | -- | `user`, `agent`, `unassigned` |
| `priority` | string | No | -- | `urgent`, `high`, `medium`, `low`, `none` |
| `is_active` | boolean | No | -- | Set to `false` to pause the schedule |

**Example request:**
```json
{
  "name": "update_recurring_schedule",
  "arguments": {
    "recurring_schedule_id": "a1b2c3d4-...",
    "frequency": "weekly",
    "timezone": "Europe/Moscow",
    "is_active": false
  }
}
```

---

#### 58. `delete_recurring_schedule`

Delete a recurring task schedule. **Task instances already created from it are not
affected** -- they stay where they are; only future generation stops. To stop generation
reversibly, prefer `update_recurring_schedule` with `is_active=false`.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `recurring_schedule_id` | string | **Yes** | -- | UUID of the schedule to delete |

**Example request:**
```json
{
  "name": "delete_recurring_schedule",
  "arguments": {
    "recurring_schedule_id": "a1b2c3d4-..."
  }
}
```

---

### Task Checkout (3 tools)

#### 59. `checkout_task`

Acquire an exclusive lock on a task to prevent double-work. Only agents can checkout tasks. If the task is already checked out by another non-expired agent, the call fails with a conflict error including details about the current holder. Same-agent re-checkout is idempotent and returns a fresh token.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID to lock |
| `ttl_minutes` | number | No | `15` | Lock duration in minutes (1-240) |

**Example request:**
```json
{
  "name": "checkout_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "ttl_minutes": 30
  }
}
```

**Example response:**
```json
{
  "task_id": "a1b2c3d4-...",
  "checkout_token": "f1e2d3c4-...",
  "checked_out_by": "684bd684-...",
  "expires_at": "2026-03-10T12:30:00Z"
}
```

**Example conflict response (409):**
```json
{
  "code": 409,
  "message": "Task is already checked out",
  "details": {
    "checked_out_by": "other-agent-uuid",
    "expires_at": "2026-03-10T12:15:00Z"
  }
}
```

---

#### 60. `release_task`

Release an exclusive checkout. The `checkout_token` from the checkout response must be provided. Agents should release checkouts when done working on a task to allow others to pick it up immediately rather than waiting for TTL expiry.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `checkout_token` | string | **Yes** | -- | Token returned by `checkout_task` |

**Example request:**
```json
{
  "name": "release_task",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "checkout_token": "f1e2d3c4-..."
  }
}
```

**Example response:**
```json
{
  "status": "released"
}
```

---

#### 61. `extend_checkout`

Extend the TTL of an active checkout. Use this when a task takes longer than expected. The checkout must not have expired already.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task ID |
| `checkout_token` | string | **Yes** | -- | Token returned by `checkout_task` |
| `ttl_minutes` | number | No | `15` | New TTL in minutes from now (1-240) |

**Example request:**
```json
{
  "name": "extend_checkout",
  "arguments": {
    "task_id": "a1b2c3d4-...",
    "checkout_token": "f1e2d3c4-...",
    "ttl_minutes": 60
  }
}
```

**Example response:**
```json
{
  "task_id": "a1b2c3d4-...",
  "checkout_token": "f1e2d3c4-...",
  "checked_out_by": "684bd684-...",
  "expires_at": "2026-03-10T13:00:00Z"
}
```

---

### Human Gate (2 tools)

#### 62. `set_human_gate`

Arm the human gate on a task: freeze it and record **who** is waiting, **what** was asked, and **what happens if nobody answers**. Use this instead of hand-writing a `❓ Blocking @pavel` comment — the marker still works, but this path records the whole ask on the task itself, so no reader has to re-derive it from comment text.

`gate_author` is deliberately **not** a parameter: the server takes it from the authenticated identity, so the answer to "who is waiting" cannot be attributed to somebody else.

The four predicate answers are required, each with one line of justification. The server **refuses the arm** when your own answers say nobody needs to be asked — if you hold the credential, the action is reversible, and nothing a customer sees or pays changes right now, capture a rollback anchor and just do it. If the blocker is another card, the server tells you to use `add_dependency` instead.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task to gate |
| `reason` | string | **Yes** | -- | The question itself, in your own words |
| `recommended_default` | string | **Yes** | -- | What you will do if nobody answers. An ask with no default can never time out |
| `class` | string | No | `hard` | `hard` (never auto-released) or `soft` (released by timeout; the release does **not** answer the question) |
| `deadline` | string | No | -- | RFC3339 timestamp when `recommended_default` applies |
| `credential_exists` | boolean | **Yes** | -- | Do you already hold the credential this needs? |
| `credential_reason` | string | **Yes** | -- | Which credential, and where you checked |
| `reversible` | boolean | **Yes** | -- | Is there a rollback anchor? If you can *manufacture* one, this is `true` |
| `reversible_reason` | string | **Yes** | -- | The exact rollback path, or why none exists |
| `blocked_by_other_task` | boolean | **Yes** | -- | Is the blocker actually another card? |
| `blocked_reason` | string | **Yes** | -- | Which card, or why none |
| `customer_visible_now` | boolean | **Yes** | -- | Does this change what a customer sees or pays **right now**? |
| `customer_reason` | string | **Yes** | -- | What the customer would see, or why nothing changes for them now |

**Example response (refused):**
```json
{
  "error": "Unprocessable Entity",
  "field": "predicate",
  "message": "cannot arm human_gate: predicate the predicate says nobody needs to be asked: you hold the credential, the action is reversible, and nothing a customer sees or pays changes right now..."
}
```

---

#### 63. `clear_human_gate`

Release a human gate. Server-enforced **user-only**: an agent key receives a 403 whose message names the exits an agent *can* reach — withdraw your own marker with a short negator comment if you raised it, or record the human's answer as a human-gate decision. Read `human_gate_info.clearable_by_owner` on `get_task` first.

Releasing also drops the ask metadata (author, reason, recommended default, deadline) and resets the class to `hard`: those fields describe a *live* question, and one left on a settled task is a default something would eventually apply.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `task_id` | string | **Yes** | -- | Task whose gate to clear |

---

## Error Handling

All tools return errors in a consistent format:

```json
{
  "isError": true,
  "content": [
    {
      "type": "text",
      "text": "error: invalid task_id: UUID must be a valid UUID"
    }
  ]
}
```

Common error conditions:
- **Invalid UUID** -- parameter is not a valid UUID format
- **Not found** -- referenced entity does not exist
- **Permission denied** -- agent lacks access to the workspace/project
- **Validation error** -- required field missing or invalid value

## Authentication

MCP tools authenticate using the `MESH_AGENT_KEY` environment variable (stdio mode) or
per-connection HTTP headers/query parameters (SSE mode). The key format is:

```
agk_{workspace_slug}_{random_string}
```

The agent key is generated when registering an agent through the REST API or the web UI.
It is shown only once at creation time -- store it securely.

To regenerate a lost key, use `POST /api/v1/agents/{agent_id}/regenerate-key` via the REST API.
