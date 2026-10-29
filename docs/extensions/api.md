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
  "key_expires_at": "2027-01-10T00:00:00Z",
  "key_expiry_source": "source",
  "key_expiring_soon": false,
  "last_sync_checked_at": "2026-10-16T12:00:00Z",
  "last_sync_status": "ok",
  "last_sync_error": null,
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
| `GET /extensions/catalog` | any authenticated | – | `200 { "extensions": [ExtensionSummary], "catalog": { available, fetched_at?, error? } }` — the signed Spark catalog joined with what is installed, one row per id. A catalog record with `runtime: "builtin"` is the code shipped with Mesh under the same id: it never adds a second row, and a record this build does not carry is left out. An installed row carries `page_url` of its catalog record; a repo install whose id the catalog lists carries `id_conflict: "catalog"` |
| `GET /extensions/manifest-schema` | any authenticated | – | `200` JSON Schema (`application/schema+json`), the current manifest schema |
| `GET /extensions/{id}/instance-settings` | instance admin | – | `200` settings object; secret values are masked |
| `PUT /extensions/{id}/instance-settings` | instance admin | settings object | `200` settings object; a masked secret value preserves the stored secret |
| `POST /extensions/install` | instance admin | `{ "source": "repo", "repo_url", "ref", "auth_token"? }` | `201 { extension_id, version, source, trust_level: "custom", repo_url, repo_ref, commit_sha }`. `400` `extension_builtin_runtime` / `extension_incompatible` / `extension_manifest_invalid`, `409` `extension_id_conflict` (the id is installed, listed in the Spark catalog or shipped with Mesh: one extension, one id), `403`/`404`/`502` `extension_repo_unavailable`. The token is stored encrypted and never returned |
| `POST /extensions/repo/refs` | instance admin | `{ repo_url, auth_token? }` | `200 { tags: [{name, sha}], branches: [{name, sha}] }` |
| `POST /extensions/repo/preview` | instance admin | as install | `200` manifest, `capabilities`, `compatible`, `commit_sha`, `trust_level: "custom"`, `warning`; installs nothing |
| `GET /extensions/updates` | instance admin | – | `200 { updates: [{extension_id, current_*, latest_*, update_available, new_capabilities, needs_consent, pending, can_rollback, error?}] }` |
| `POST /extensions/{id}/update` | instance admin | `{ consent_commit_sha? }` | `200 { outcome: updated \| up_to_date \| pending_consent, ... }`. An update that adds permissions stays `pending_consent` until `consent_commit_sha` names that commit |
| `POST /extensions/update-all` | instance admin | – | `200 { results: [...] }`, same shape per extension |
| `POST /extensions/{id}/rollback` | instance admin | – | `200` the version before the last update; `409` when there is none |

For a tag `ref` the updates check also offers a newer semver tag; for a branch it
compares the head with the installed `commit_sha`. With the instance auto-update
flag on, a background pass every 30 minutes applies updates that add no
permissions; the others wait for consent.

`GET /extensions/events` returns the extension event catalog.

## Workspace routes

Read: any workspace member. Write: `manage_webhooks` permission (same as the
old integrations routes).

Secret settings (`x-secret`) sent in plaintext are stored encrypted (same
`MESH_INTEGRATION_ENCRYPTION_KEY` as the old routes). Telegram additionally
checks the token with `getMe` (fills `bot_username`, `400` on a bad token) and
restarts the bot poller on connect, settings, enable/disable and delete.
Each `WorkspaceExtension` carries `can_purge_data` (`data=delete` is supported)
and `settings_managed_elsewhere` (generic connect/settings/remove refuse; Runner).

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

For every extension with `levels.project = true` (today `team-relay`, `gitlab`,
`github`). An id without a project level, or not registered, answers `404
not_found`; so does an extension whose manifest loses `levels.project`. Read:
project access. Write: `manage_webhooks`.

Settings are validated against the manifest `project` schema: a key the schema
does not declare, or one marked `readOnly`, is refused with `400 validation_failed`
(`details` names the key), then the provider's own rules run (`gitlab`:
`repo_path` is `group/project`). A refused save changes nothing.

A secret setting (`agent_key`, the one secret a binding can hold) reads back as
`"***"`; on `PUT`, leaving it out or sending `"***"` keeps the stored value, and
the value is never stored in `settings`. A manifest that declares any other
`x-secret` at the project level is refused at `PUT` until it has a place to be
stored. A `GET` answer is accepted back by `PUT` unchanged.

| Method and path | Request | Response |
|---|---|---|
| `GET /projects/{p}/extensions` | – | `200 { "extensions": [ProjectExtension] }` — the bindings this project has, each with its `last_sync_*` |
| `GET /projects/{p}/extensions/{id}` | – | `200 ProjectExtension`; `404` when never set |
| `PUT /projects/{p}/extensions/{id}` | `{ "enabled": true, "settings": {…} }` | `200 ProjectExtension` (created or replaced) |
| `DELETE /projects/{p}/extensions/{id}` | – | `204` |

`ProjectExtension` also carries, next to `settings`: `key_expires_at` and
`key_expiry_source` (`"source"`: read from the system itself, `"manual"`; absent
while unknown), `key_expiring_soon` (within 14 days, `false` while unknown),
`last_sync_checked_at`, `last_sync_status` (`ok`, `error`, `key_expired`, …),
`last_sync_error`. They are read-only and are not part of `settings`.

### Team Relay project settings

`share_id`, `share_slug`, `subfolder`, `agent_key` (secret), and the mount
settings: `include_project_slug` (bool), `docs_mount_path` (slug path in the
project Docs, empty = project top level), `excluded_paths` (top-level share
folders not mounted; absent = `["artifacts"]`, `[]` = mount everything),
`sync_interval_minutes` (`0` = scheduled sync off, otherwise ≥ 5; absent = 15).
`key_expires_at` is read-only: it is asked of the relay, never set by a client.

The share follows from the agent key: a key belongs to exactly one share, so a
save with a new `agent_key` asks the relay (`GET /v1/web/agent-key`) which one
and stores its `share_id`, `share_slug` and the read-only `share_name` the form
shows. A `share_id` that is not the key's is a `400` on `agent_key`; a key the
relay rejects is the same. A save without a new key keeps the bound share;
naming another share without that share's key is a `400` on `agent_key`. When
the relay cannot answer (unreachable, or a relay without that route), the
`share_id`/`share_slug` the client sent are stored as before.
Saving settings does not interrupt sync: the documents mounted from the share
stay as they are and the next scheduled or manual mount refreshes them.

### Actions

What an extension can do for a project beyond its settings is a provider action,
so the interface knows one shape and no system-specific path.

| Method and path | Who | Response |
|---|---|---|
| `GET /projects/{p}/extensions/{id}/actions` | project access | `200 { "actions": [{ "name", "method", "title" }] }` |
| `POST /projects/{p}/extensions/{id}/actions/check` | `manage_webhooks` | `200 { "ok", "message", "enabled", "source", "checked_at" }` — health of the connection this binding relies on (the workspace or instance level of the same extension). Writes nothing. `404` when the project has no binding |
| `GET /projects/{p}/extensions/{id}/actions/search?q=&limit=` | project access | `team-relay` only: search the documents of the bound share (same answer as `GET /projects/{p}/tr/search`) |
| `POST /projects/{p}/extensions/{id}/actions/mount` | `manage_webhooks` | `team-relay` only: materialize the share into the project Docs now (same answer as `POST /projects/{p}/tr/mount`) |

### Repositories (`gitlab`, `github`)

`GET /projects/{p}/extensions/{id}/repos` (project access; `404` for any other
extension) is read-only: it joins the repositories the project's VCS links point
into with the one its binding names (`repo_path` for GitLab, `repo` for GitHub;
bind and unbind stay `PUT`/`DELETE /projects/{p}/extensions/{id}`).

```json
{ "repos": [{ "repo": "entire-vc/evc-mesh", "host": "gitlab.example.invalid", "links": 1234,
              "last_event_at": "2026-10-26T10:00:00Z", "bound": true, "source": "both" }] }
```

`source` is `webhook` (links, no binding), `binding` (bound, no links: `links` 0,
no `last_event_at`; `host` from the workspace connection's `base_url`, else
`github.com` for GitHub, else the host of the project's other links) or `both`. The repository of a link is its url without
scheme, lowercased, cut before `/-/merge_requests/`, `/merge_requests/`,
`/pull/`, `/commit/` or `/tree/`. In the workspace list
(`GET /workspaces/{ws}/extensions`) `activity.repos` of `gitlab`/`github` is
the same aggregate over the workspace: the 20 most linked repositories, without
`bound`/`source`.

An action answers only on its own method; any other method, an unknown action
or another extension's action is `404`. `check` exists for every extension with a
project level. The old `GET /tr/search`, `GET /projects/{p}/tr/search` and
`POST /projects/{p}/tr/mount` keep working unchanged.

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
- `PUT /api/v1/extensions/keep/instance-settings` with `{"enabled": true, "settings": {"secret_provider": "builtin"|"keep"}}` chooses where new x-secret values are stored (default `builtin`). With `keep`, saving a secret fails while Keep is not configured or unreachable; values stored earlier keep resolving through their own provider. The change takes effect within 30 seconds and is journaled by key only.

Instance administrator = a human user (never an agent key) with the owner/admin role in the oldest non-deleted workspace of the instance. There is no separate instance-level role.
