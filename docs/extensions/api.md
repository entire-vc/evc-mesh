# Extensions API

This document defines the extension API contract; availability depends on the deployed version.

This document describes the JSON contract for instance, workspace, and project
extension routes. Keep clients compatible with the documented response schemas
and stable error codes when these routes evolve.

Building your own extension? Start with the
[Extension Developer Guide](developer-guide.md): manifest, permissions, events,
UI slots, release and publishing through Spark.

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
  "consented_version": "0.3.0",
  "consented_commit": "4be1…",
  "needs_consent": true,
  "pending_consent": {
    "version": "0.4.0",
    "commit": "9c07…",
    "added_capabilities": ["api:tasks:write", "events:task.created"],
    "added_events": ["task.created"]
  },
  "created_at": "…",
  "updated_at": "…"
}
```

`settings` are validated against the manifest `settings` schema for the
`workspace` level.

`consented_version` / `consented_commit` name the release the consent was given
for; both are empty on a consent recorded before they were stored or backfilled
from the extension actor. `pending_consent` is computed on every read from the
installed release, nothing is stored: the rights that release declares and the
consent does not cover. It is absent (and `needs_consent` is `false`) when there
is nothing new to agree to. A connection without any consent record shows every
declared right as pending. See "Re-consent to a newer manifest".

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
| `POST /extensions/install` (catalog) | instance admin | `{ "source": "catalog", "id", "version"? }` | `201 { extension_id, version, source: "catalog", trust_level, commit_sha, artifact_sha256, catalog_scope }`. Without `version`: the default catalogue's latest (`catalog_scope: "default"`). With `version`: exactly that release from Spark's exact receipt, also for an id the default catalogue does not list (`catalog_scope: "scoped"`, pinned). See [Installing an exact version](#installing-and-updating-an-exact-version-scoped-releases) |
| `POST /extensions/repo/refs` | instance admin | `{ repo_url, auth_token? }` | `200 { tags: [{name, sha}], branches: [{name, sha}] }` |
| `POST /extensions/repo/preview` | instance admin | as install | `200` manifest, `capabilities`, `compatible`, `commit_sha`, `trust_level: "custom"`, `warning`; installs nothing |
| `GET /extensions/updates` | instance admin | – | `200 { updates: [{extension_id, current_*, latest_*, update_available, new_capabilities, needs_consent, pending, can_rollback, catalog_scope?, error?}] }`. A row with `catalog_scope: "scoped"` is pinned: it is never offered the default catalogue's latest |
| `POST /extensions/{id}/update` | instance admin | `{ consent_commit_sha?, target_version?, expected_target_commit_sha?, target_scope? }` | `200 { outcome: updated \| up_to_date \| pending_consent, ... }`. An update that adds permissions stays `pending_consent` until `consent_commit_sha` names that commit. `target_version` + `expected_target_commit_sha` choose a release: from the default catalogue (`target_scope` absent or `"default"`; a pinned install becomes unpinned) or from Spark's exact receipt (`target_scope: "scoped"`; the install stays or becomes pinned) |
| `POST /extensions/update-all` | instance admin | – | `200 { results: [...] }`, same shape per extension |
| `POST /extensions/{id}/rollback` | instance admin | – | `200` the version before the last update; `409` when there is none |
| `POST /extensions/{id}/move-to-catalog/plan` | instance admin | `{ target_version?, target_scope? }` | `200 MovePlan`; changes nothing. See [Moving a repository install to the catalog](#moving-a-repository-install-to-the-catalog) |
| `POST /extensions/{id}/move-to-catalog` | instance admin | `{ expected_version, expected_commit_sha, target_version, target_commit_sha, target_scope?, consent_commit_sha? }`, header `Idempotency-Key` | `200 { outcome: moved \| already_moved, plan }` |
| `POST /extensions/{id}/repin/plan` | instance admin | `{ repo_url?, ref, auth_token?, remove_auth_token? }` | `200 RepinPlan`; changes nothing. See [Re-pinning a repository install](#re-pinning-a-repository-install) |
| `POST /extensions/{id}/repin` | instance admin | as plan, plus `{ expected_version, expected_commit_sha, expected_target_commit_sha, consent_commit_sha? }`, header `Idempotency-Key` | `200 { outcome: repinned \| up_to_date, plan }` |

For a tag `ref` the updates check also offers a newer semver tag; for a branch it
compares the head with the installed `commit_sha`. With the instance auto-update
flag on, a background pass every 30 minutes applies updates that add no
permissions; the others wait for consent.

### Moving a repository install to the catalog

An extension installed from a repository (`source: "repo"`) can be moved to the
release Spark publishes under the same id, without uninstalling it. Everything
keyed by the id stays as it is: workspace connections and their consent,
project scope and bindings, event subscriptions, the extension actor and its
token, instance settings and the disable flag, the instance allow-list, and
`keep:` references in connection config. No project outside the bindings
becomes reachable.

1. `POST /extensions/{id}/move-to-catalog/plan` with an optional
   `target_version` (default: the catalog's latest). The answer:
   `{ extension_id, current, target, added_capabilities, removed_capabilities,
   needs_consent, one_way: true }`, where `current` and `target` each carry
   `source, version, commit_sha, artifact_sha256?, trust_level, repo_url?,
   repo_ref?, manifest`. The target artifact is fetched and verified exactly as
   a catalog install does (signed catalog, Spark marks the release installable,
   artifact SHA-256 equals the signed digest, manifest id and version match).
2. `POST /extensions/{id}/move-to-catalog` with `expected_version` and
   `expected_commit_sha` (the `current` side of the plan) and `target_version`
   and `target_commit_sha` (its `target` side). The move is one transaction:
   the row is locked, checked against the expected state, switched to
   `source: "catalog"` with the catalog trust, digest, commit and manifest; the
   repository URL, ref, the previous/pending/skipped repository state and the
   stored repository token are deleted; a `moved_to_catalog` journal entry is
   written. When `added_capabilities` is not empty, `consent_commit_sha` must
   equal `target_commit_sha`; nothing is parked as pending.

With `target_scope: "scoped"` (and `target_version`, required then) the target
is read from Spark's exact receipt instead of the default catalogue, so the id
need not be listed there, and the moved install is pinned to that version (see
[Installing and updating an exact version](#installing-and-updating-an-exact-version-scoped-releases)).
`current`/`target` then carry `catalog_scope`.

The same version as the repository install is allowed; an older one is not
(`409 extension_move_downgrade`). Refusals change nothing:

| Status and code | When |
|---|---|
| `400` | a required field is missing |
| `404` | the id is not installed |
| `404 extension_not_in_catalog` | the catalog does not list the id or that version |
| `409 extension_not_repo_install` | the install is not from a repository |
| `409 extension_builtin_runtime` | the catalog record is the code shipped with Mesh |
| `409 extension_state_changed` | the install or the catalog release is not what the plan showed (another admin, an update or a catalog change in between) |
| `409 consent_required` | the target adds permissions and `consent_commit_sha` is not the target commit |
| `409 extension_blocked` / `extension_release_revoked` | the release is blocked or yanked |
| `409 extension_move_downgrade` | the target is older than the installed version |
| `422 extension_artifact_digest_mismatch` | the catalog gives no digest for the release, or the artifact does not match it |
| `422 extension_incompatible` | the release does not support this Mesh API version |
| `422 extension_manifest_invalid` | Spark does not mark the release installable, or the artifact's manifest is not that id and version |
| `503 extension_catalog_unavailable` | the signed catalog or Spark cannot be read |

**The move is one way.** The repository state is not kept, so
`POST /extensions/{id}/rollback` right after the move answers
`409 extension_rollback_unavailable`: a catalog rollback only restores a catalog
snapshot. From then on update, auto-update and rollback of the id go through the
catalog path only and never call the old repository. Going back to a repository
means uninstalling and installing again.

A retry of an apply that already happened answers `200 already_moved` and
writes nothing. With an `Idempotency-Key` header the retry returns the stored
answer byte for byte (`Idempotent-Replayed: true`).

### Installing and updating an exact version (scoped releases)

A publisher can release a version in Spark with the `scoped` publication scope:
it stays out of the default catalogue (no `latest`, no listing, no Updates
offer) and is fetched only by exact id and version. **Scoped is unlisted, not
private**: Spark serves the receipt and the artifact to anyone who names the id
and the version, without authentication. Do not put secrets in a scoped
release.

Mesh reads one release with
`GET {spark}/api/v1/mesh-extensions/catalog.json?scope=scoped&slug={id}&version={x.y.z}&mesh_api=1`.
The receipt is trusted exactly like the default catalogue (Ed25519 signature
over the exact bytes with the instance's catalog key, schema 1, ETag equal to
the body hash) and is refused unless its `selection` is
`{scope: "scoped", slug: id, version}` and it carries exactly one entry, of that
id, with exactly one release, of that version. The artifact comes from the
ordinary route and must hash to the receipt's digest; the manifest inside must
be that id and version; Mesh API compatibility, the blocklist (the receipt's
and the cached default catalogue's), the id conflict check and the consent for
added permissions are those of any catalog install. Spark's 404 for a missing,
incompatible, yanked, blocked, suspended or non-public selection is
`404 extension_not_in_catalog`; a receipt that does not verify or is not the
one asked for is `502 extension_catalog_receipt_invalid`. Nothing is written
on a refusal.

- Install: `POST /extensions/install { "source": "catalog", "id", "version" }`.
- Update to another exact version:
  `POST /extensions/{id}/update { "target_version", "expected_target_commit_sha", "target_scope": "scoped", "consent_commit_sha"? }`.
  The version must be newer than the installed one; `expected_target_commit_sha`
  must be the receipt's commit.
- Move a repository install: `move-to-catalog{,/plan}` with
  `"target_scope": "scoped"`.

**Such an install is pinned** (`catalog_scope: "scoped"`): the automatic update
pass never moves it, `GET /extensions/updates` never offers it the default
catalogue's `latest`, and a plain `update` answers `up_to_date`, also after
the id appears in the default catalogue. It moves only when an admin chooses a
version: an exact one keeps it pinned, a default-catalogue one
(`target_scope: "default"`) unpins it. `rollback` returns to the previous
release with the scope it had; for a scoped snapshot the exact receipt of that
version decides whether it is still offered.

**Blocking.** Spark keeps a scoped block out of the default blocklist and
answers the exact receipt of a blocked, yanked or suspended scoped release with
404. The hourly blocklist pass therefore re-reads the exact receipt of every
pinned install: a 404 (or a receipt whose commit or digest no longer matches the
installed one) switches the extension off for every workspace with the reason
"Spark no longer offers this exact release (blocked, yanked or withdrawn)",
writes `catalog_blocked` and notifies instance admins; a block in the receipt's
own blocklist does the same with its reason; a receipt that is served again
switches it back on. Spark unreachable or a receipt that does not verify
changes nothing. An exact update to a version Spark still offers switches the
extension back on at once.

### Re-pinning a repository install

`POST /extensions/{id}/repin/plan` and `POST /extensions/{id}/repin` change the
`ref` of a repository install, and optionally its `repo_url` and token, with the
same plan/apply shape. `ref` is a branch, a tag or a full 40-hex commit SHA.
The plan answers `{ extension_id, current, target, added_capabilities,
removed_capabilities, needs_consent, token, auto_update, changed }`; `current`
and `target` carry `repo_url, ref, pin_kind (commit | tag | branch),
commit_sha, version, manifest`.

- Apply requires `expected_version` and `expected_commit_sha` (the current
  install) and `expected_target_commit_sha` (the plan's `target.commit_sha`);
  a missing one is `400`. A current install that changed since the plan, or a
  branch or tag that now resolves to another commit, is
  `409 extension_state_changed`.
- A commit pin must resolve to itself (`422 extension_manifest_invalid`). The manifest at the target must carry the
  same id (`409 extension_id_conflict`).
- Added permissions need `consent_commit_sha` equal to the target commit
  (`409 consent_required`); nothing is parked as pending.
- Token: `auth_token` replaces the stored token (stored encrypted),
  `remove_auth_token: true` deletes it, neither keeps it. When `repo_url`
  changes and a token is stored, one of the two is required
  (`400 extension_repo_token_choice_required`): a stored token is never sent to
  a new address unasked. The plan's `token` says `keep`, `replace`, `remove` or
  `none`.
- On the same URL the old pin stays available to `rollback`; on a new URL the
  previous state is cleared.
- Auto-update leaves a commit pin where it is (`auto_update` in the plan says
  so) and makes no repository request for it. A tag pin moves to a newer semver
  tag and a branch pin follows its head, as before.
- A retry: `200 up_to_date` when the install is already at the target; with
  `Idempotency-Key` the stored answer is replayed.

Both operations write the instance journal (`moved_to_catalog`, `repinned`)
with the from/to version, commit, source and token change; read it with
`GET /extensions/instance/journal`. A repository token never appears in an
answer, the journal or the log.

`GET /extensions/events` (event catalog) is unchanged.

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
| `POST /workspaces/{ws}/extensions/{id}/consent` | `{ "version": "0.4.0", "commit": "9c07…" }` | `200 WorkspaceExtension`. Re-consent of an existing connection to the installed release, see below. `version` and `commit` must name exactly what is installed (`commit` is empty for a builtin extension), else `409 version_changed` with `context: { version, commit }` of the installed release and nothing changes. No `version`: `400`; no connection: `404` |
| `PUT /workspaces/{ws}/extensions/{id}/project-scope` | `{ "project_scope": "workspace" \| "bound" }` | `200 { "extension_id", "project_scope" }`. See "Project scope" below. `bound` on an extension without `levels.project`: `400 validation_failed`; no connection: `404` |

Errors common to all: `404 not_found` (unknown extension id or no connection
where one is required), `403 forbidden`, `400 validation_failed` (settings do
not match the manifest schema; `details` carries the field messages).

### Re-consent to a newer manifest

An instance update can bring a manifest that declares more rights (API scopes,
events) than the workspace agreed to at connect. Those added rights do not act
until an owner or admin (`manage_webhooks`) agrees to them:

- the extension token, push subscriptions and deliveries use the rights that are
  both declared by the installed manifest and consented by the workspace;
- `POST …/consent` with the `version` and `commit` from `pending_consent`
  records the consent as exactly the rights that release declares, with
  `consented_version/commit/at/by`, and in the same transaction replaces the
  extension actor's capability snapshot. The transaction locks the installed
  release row: an instance update in flight is waited for and then refused with
  `409 version_changed`, so a consent never lands on a release the admin did
  not review;
- the connection keeps its id, settings, project scope and bindings, its
  enabled/disabled state, its actor id and its token. The same token gains the
  added rights on its next request;
- after the consent Mesh repeats the install handshake so the service learns
  the new event set. This handshake never issues, rotates or posts the
  token, even to a `token_path` service whose token is not yet marked delivered;
  such a first delivery stays with the ordinary handshake, as before the
  consent. A failed handshake does not undo the consent; it is retried
  by the next health check, exactly like a failed connect. The service reads its
  current rights from `GET /ext/me`.

Rights a newer manifest **dropped** stop acting at once, without any consent:
the effective set is always intersected with what the installed manifest
declares. They are not pending, and the next consent removes them from the
record, because a consent is always the whole declared set of one release.
A rollback of the instance to an older release therefore does not bring back a
right the workspace no longer consents to: it acts only if the older release
declares it *and* the current consent covers it.

Connections that had no consent record were backfilled from their extension
actor's snapshot (what the token already held). A builtin connection without a
record was backfilled with its builtin manifest's declared set: that is what
its status always reported, and builtins use neither `ext_` tokens nor push
events. Any other connection with neither is not given the declared set: it
receives no events and shows `needs_consent` until someone consents.

Delivery also needs the extension to be registered on the instance: a
connection left behind by an extension that is no longer installed receives
nothing, since there is no manifest to declare its events.

`POST /extensions/{id}/update` with `consent_commit_sha` is the instance
admin's agreement to install a release; it does not consent on behalf of the
workspaces. Each workspace consents on its own connection.

### Project scope

A connection's `project_scope` decides which projects its extension actor
(`ext_` token) reaches:

- `workspace` (default, and every connection made before scopes existed): the
  whole workspace it is connected to.
- `bound`: only projects with an enabled binding for the extension
  (`PUT /projects/{p}/extensions/{id}` with `"enabled": true`).

For a `bound` connection every API route that addresses a project — by
`{proj_id}`, `{task_id}` (full or short id), `by-short-id/{short}`,
`{doc_id}`, `{artifact_id}`, `{event_id}` or `{link_id}` — answers `403` when
that project is not bound (a missing id answers the same), and the refusal is
counted in the connection's violations. `GET /workspaces/{ws}/projects` and
`GET /workspaces/{ws}/export/hierarchy` list only the bound projects. Routes
that address projects through their body (`PATCH /vcs-links/ship-sha`) or
create one (`POST /workspaces/{ws}/projects`) are refused. Event deliveries
(webhook and NATS) carry only events whose `project_id` is bound; events with
no project (workspace members, agent keys) are not delivered. `POST
/ext/{id}/ui-context` refuses a project or task that is not bound. Disabling or
removing a binding stops both API access and events for that project at once.

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

## Event delivery signature

Events reach an extension's webhook with a signed request. The manifest field `external.signature_version` selects the scheme: absent or `1` is the `{timestamp}.{body}` HMAC, `2` also authenticates `X-Mesh-Delivery` and `X-Mesh-Event`. Version negotiation, canonical bytes, a verification example, migration and rollback are in [docs/webhooks.md](../webhooks.md#extension-signature-v2).

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
