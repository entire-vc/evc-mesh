# Extensions API

This document defines the extension API contract; availability depends on the deployed version.

This document describes the JSON contract for instance, workspace, and project
extension routes. Keep clients compatible with the documented response schemas
and stable error codes when these routes evolve.

Conventions:

- Base path `/api/v1`, JSON, bearer or agent-key auth like every other route.
- Ids in paths: `{ws}` workspace id, `{p}` project id, `{id}` extension id
  (the manifest `id`, e.g. `github`), not a uuid.
- Timestamps are RFC 3339 UTC.
- Errors use the standard envelope `{ "code": <http status>, "error_code": "<stable code>", "message": "<english>", "details"?: {...} }`.
  The interface translates by `error_code`.
- A secret setting (manifest `x-secret`) is never returned: the field is `"***"`
  when set and absent when not.

## Types

### Manifest

The manifest document as stored (`schema/manifest.v1.json`), returned verbatim.

### InstanceStatus

```json
{ "enabled": true, "configured": true }
```

`configured` — an instance-level setting exists; `enabled` — it is switched on.
Both `false` for an extension with no instance level.

### ExtensionSummary (instance list item)

```json
{
  "id": "github",
  "name": "GitHub",
  "version": "1.2.0",
  "kind": "integration",
  "runtime": "builtin",
  "publisher": "Entire VC",
  "description": "…",
  "category": "vcs",
  "levels": { "instance": true, "workspace": true, "project": false },
  "source": "builtin",
  "trust_level": "builtin",
  "blocked": false,
  "blocked_reason": "",
  "created_at": "2026-09-01T00:00:00Z",
  "updated_at": "2026-10-14T00:00:00Z",
  "manifest": { "…": "…" },
  "instance": { "enabled": true, "configured": true }
}
```

`source`: `builtin` | `catalog` | `repo`. `runtime` and `kind` as in the manifest.

### ExtensionStatus

What the runtime acts on for one extension in one workspace (one function,
`ResolveStatus`, behind the API and the runtime check).

```json
{
  "enabled": true,
  "source": "workspace",
  "version": "1.2.0",
  "health": { "ok": true, "message": "" },
  "health_checked_at": "2026-10-14T18:00:00Z",
  "last_error": "",
  "consented_capabilities": ["api:tasks.read", "events:task.updated"]
}
```

`source`: `workspace` (the workspace's own connection governs, active or switched
off) | `instance` (no workspace connection; instance level governs) | `none`.
`health` is cached for 60 s; `POST …/test` forces a fresh check.
`health`, `health_checked_at`, `last_error` are absent while `enabled` is false.

### Connection

```json
{
  "id": "6f1c…",
  "settings": { "…": "…" },
  "consented_capabilities": ["api:tasks.read"],
  "consented_at": "2026-10-14T17:00:00Z",
  "consented_by": "7a30…",
  "created_at": "…",
  "updated_at": "…"
}
```

`settings` are validated against the manifest `settings` schema for the
`workspace` level.

### WorkspaceExtension (workspace list item)

```json
{
  "id": "github",
  "name": "GitHub",
  "version": "1.2.0",
  "kind": "integration",
  "category": "vcs",
  "levels": { "instance": true, "workspace": true, "project": false },
  "manifest": { "…": "…" },
  "status": { "…ExtensionStatus…": "…" },
  "connection": { "…Connection…": "…" }
}
```

`connection` is `null` when the workspace has no row (the instance level may
still govern: see `status.source`).

### ProjectExtension

```json
{
  "extension_id": "team-relay",
  "enabled": true,
  "settings": { "…": "…" },
  "created_at": "…",
  "updated_at": "…"
}
```

`settings` are validated against the manifest `settings` schema for the
`project` level.

## Instance routes

| Method and path | Who | Request | Response |
|---|---|---|---|
| `GET /extensions` | any authenticated | – | `200 { "extensions": [ExtensionSummary] }` — installed and builtin, ordered by id |
| `GET /extensions/manifest-schema` | any authenticated | – | `200` JSON Schema (`application/schema+json`), the current manifest schema |
| `GET /extensions/{id}/instance-settings` | instance admin | – | `200` settings object; secret values are masked |
| `PUT /extensions/{id}/instance-settings` | instance admin | settings object | `200` settings object; a masked secret value preserves the stored secret |
| `POST /extensions/install` | instance admin | – | `501` `not_implemented` when installation is unavailable |
| `POST /extensions/{id}/update` | instance admin | – | `501` `not_implemented` when updates are unavailable |
| `POST /extensions/{id}/rollback` | instance admin | – | `501` `not_implemented` when rollback is unavailable |

`GET /extensions/events` returns the extension event catalog.

## Workspace routes

Read: any workspace member. Write: `manage_webhooks` permission (same as the
old integrations routes).

| Method and path | Request | Response |
|---|---|---|
| `GET /workspaces/{ws}/extensions` | – | `200 { "extensions": [WorkspaceExtension] }` — every registered extension, with `status` |
| `POST /workspaces/{ws}/extensions/{id}/connect` | `{ "settings": {…}, "consent": { "capabilities": ["…"] } }` | `201 WorkspaceExtension`. `consent.capabilities` must cover everything the manifest declares, else `400 validation_failed` with `details.consent`. Written to `consented_capabilities/at/by`. A second connect on an existing connection: `409 already_connected` |
| `PATCH /workspaces/{ws}/extensions/{id}/settings` | `{ "settings": {…} }` (merged over the current ones) | `200 WorkspaceExtension` |
| `POST /workspaces/{ws}/extensions/{id}/enable` | – | `200 WorkspaceExtension` |
| `POST /workspaces/{ws}/extensions/{id}/disable` | `{ "confirm": true }` | `200 WorkspaceExtension`. When `status.source` is `instance` the extension is on for the whole instance and `confirm` must be `true`; without it `409 confirmation_required` and nothing changes |
| `POST /workspaces/{ws}/extensions/{id}/test` | – | `200 { "ok": true, "message": "", "status": ExtensionStatus }` — fresh health check |
| `DELETE /workspaces/{ws}/extensions/{id}?data=keep\|delete` | – | `204`. `keep` (default) removes the connection and keeps the extension's data; `delete` removes the data too |

Errors common to all: `404 not_found` (unknown extension id or no connection
where one is required), `403 forbidden`, `400 validation_failed` (settings do
not match the manifest schema; `details` carries the field messages).

## Project routes

Only for extensions with `levels.project = true`. Today that is `team-relay`
alone, so the routes are registered as `/projects/{p}/extensions/team-relay`;
any other id answers `404 not_found` from the router. A project-level extension
added later registers its own path next to it. If the Team Relay manifest ever
loses `levels.project`, the handler answers `404` as well. Read: project access. Write: `manage_webhooks`.
A secret setting (`agent_key`) reads back as `"***"`; on `PUT`, leaving it out or
sending `"***"` keeps the stored value. A key outside the `project` settings
schema is refused with `400 validation_failed`.

| Method and path | Request | Response |
|---|---|---|
| `GET /projects/{p}/extensions/{id}` | – | `200 ProjectExtension`; `404` when never set |
| `PUT /projects/{p}/extensions/{id}` | `{ "enabled": true, "settings": {…} }` | `200 ProjectExtension` (created or replaced) |
| `DELETE /projects/{p}/extensions/{id}` | – | `204` |

## Deprecated aliases

The legacy routes remain available during migration and answer with
`Deprecation: true` and `Link: <new path>; rel="successor-version"`. Clients
should move to the corresponding extension routes.

| Old | New |
|---|---|
| `GET /workspaces/{ws}/integrations` | `GET /workspaces/{ws}/extensions` |
| `POST /workspaces/{ws}/integrations` | `POST /workspaces/{ws}/extensions/{id}/connect` |
| `PATCH /integrations/{int_id}` | `PATCH /workspaces/{ws}/extensions/{id}/settings` (and `enable` / `disable`) |
| `DELETE /integrations/{int_id}` | `DELETE /workspaces/{ws}/extensions/{id}` |
| `GET\|PATCH\|DELETE /projects/{p}/integrations/team-relay` | `GET\|PUT\|DELETE /projects/{p}/extensions/team-relay` |

Alias response bodies are unchanged from today.

## Instance settings

- `GET/PUT /api/v1/extensions/{id}/instance-settings`: human instance administrator only; secrets are masked on GET, a masked value on PUT keeps the stored secret.

Instance administrator = a human user (never an agent key) with the owner/admin role in the oldest non-deleted workspace of the instance. There is no separate instance-level role.
