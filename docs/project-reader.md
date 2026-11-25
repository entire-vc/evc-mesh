# Project reader (`POST /api/v1/workspaces/:ws_id/project-readers`)

A project reader is a service agent key bound to exactly one project. It reads
the allowlisted routes of that project and nothing else: no other project, no
workspace-level data, no memory, no writes. It is meant for a backend that
needs one narrow read (the first consumer: the ContentHub consent path, which
re-reads a canonical comment on its own task).

It is not the general viewer role. A plain viewer agent key stays exactly as it
was; this profile is a separate marker, `agents.reader_project_id`.

## Issue

`POST /api/v1/workspaces/:ws_id/project-readers`, as a human workspace admin
(JWT; `PermManageMembers`). An agent key, an OAuth connector or a non-admin
gets `401`/`403`.

```json
{"name": "ContentHub-Consent-Reader", "project_id": "<project uuid>"}
```

`201`, `Cache-Control: no-store`:

```json
{"agent": {"id": "...", "reader_project_id": "<project uuid>", "accepts_from": [], ...},
 "api_key": "agk_<workspace>_..."}
```

- The key is in this answer only; Mesh stores its bcrypt hash and digest.
  Never put it in argv, a comment, a log or an artifact: pass it via stdin or
  an env file the consumer reads.
- One transaction writes the agent (`reader_project_id`, `capabilities
  {"no_lane":true}`, `accepts_from []`, `mention_wakes false`), its home grant
  with role `viewer` and a `viewer` membership of that one project. The
  project must be live and in `:ws_id`, else `404` and nothing is written.
- `400` malformed body, missing `name` or `project_id`; `409` the handle is
  taken by a user or an agent of the workspace.
- Key lifetime: 365 days, like any agent key. Rotate with
  `POST /agents/:agent_id/regenerate-key` (admin); the marker is kept.

## What the key can do

The use is an ordinary agent key: `X-Agent-Key: <api_key>`.

| Request | Answer |
|---------|--------|
| `GET /api/v1/agents/me` | `200`, carries `reader_project_id` |
| `GET /api/v1/tasks/:task_id/comments` on a task of its project (UUID or short id) | `200` |
| the same on a task of any other project, an unknown or ambiguous id | `403` |
| anything else: every non-GET, every `/memories*` path (with or without `project_id`), `/projects/:proj_id/knowledge`, `/canonical_updates`, task, document and project reads, workspace lists, search, export, `/agents/me/events/stream` | `403` |
| WebSocket `/ws` with `X-Agent-Key: <api_key>` (or the `?agent_key=` fallback) | `403` before the upgrade. Probe with the header: a key in a URL can land in a proxy log (the API's own access log masks `agent_key` and `token`) |
| any request without the key | `401` |

Every refusal is the same `403` (`project reader: this key reads only the
allowlisted routes of its own project`), so it does not confirm that an id
exists elsewhere. The reader can never be an assignee (refused on the column,
whatever its profile says) and is never auto-assigned (`no_lane`).

The guard is `mw.ProjectReaderGuard` on the authenticated group, right after
`AgentReadOnlyGuard`. It is deny-by-default: the allowlist is
`projectReaderRoutes` in `internal/middleware/project_reader.go`. Adding an
entry is a security change: read the handler first (embedded objects of other
projects, workspace-tier rows, how it resolves its id) and pin it in
`cmd/api/project_reader_routes_test.go`.

## Revoke

`DELETE /api/v1/agents/:agent_id` (admin). The key answers `401` at once (the
auth cache is evicted); the agent disappears from
`GET /workspaces/:ws_id/agents`.

Inventory: `GET /api/v1/workspaces/:ws_id/agents` — readers carry
`reader_project_id`.

## Rollback

Order matters. A binary older than migration `20261122100` does not know the
column, so a live reader would authenticate as a plain viewer of its workspace.

1. `DELETE /api/v1/agents/:agent_id` for every live reader
   (`SELECT id FROM agents WHERE reader_project_id IS NOT NULL AND deleted_at IS NULL`).
2. Roll the binary back.
3. Optionally `goose down-to 20261121400`; the down migration refuses while a
   live reader exists.
