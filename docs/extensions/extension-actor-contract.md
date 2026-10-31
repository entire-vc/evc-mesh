# External extension contract

What an external extension (`runtime: external`) can rely on: the calls Mesh
makes to the service, the routes the extension actor reaches with its `ext_`
token, and the event bodies. The reference implementation is
[`examples/external-extension`](../../examples/external-extension); Mesh's tests
drive it with the real senders, so the reference and this page cannot drift
apart silently.

```
go test ./internal/... -run 'Contract|RefExt'
```

## Calls Mesh makes to the service

The install, token and event calls are `POST` with JSON, `X-Mesh-Timestamp`
(Unix seconds) and `X-Mesh-Signature` = hex HMAC-SHA256 of
`"{timestamp}.{body}"` with the workspace signing secret. The health probe is
a bodyless `GET`.

| Call | Body | Answer Mesh expects | Pinned by |
|---|---|---|---|
| install (`install_path`) | `extension_id`, `version`, `workspace_id`, `signing_secret`, `events`, `events_path`, `install_code`? — signed with the secret it carries | 2xx | `internal/extensions/external/refext_contract_test.go` |
| token (`token_path`) | `extension_id`, `workspace_id`, `extension_token`; each delivery has a strictly newer timestamp | 200 `{"ok":true}`; anything else keeps the previous token | same |
| event (`events_path`) | the event body plus `workspace_id`; headers `X-Mesh-Event` (public name) and `X-Mesh-Delivery` (same on every retry of one delivery) | 2xx; otherwise retried after 1, 5, 25 s | `internal/service/refext_delivery_contract_test.go` |
| health (`health_path`) | `GET`, no body | 200 | `refext_contract_test.go` |

A status move is delivered as `task.moved` (internally `task.status_changed`).
Screens get the context token described in [ui-iframe.md](./ui-iframe.md),
pinned by `internal/extensions/uictx/refext_contract_test.go`.

## Event bodies

| Event | Keys |
|---|---|
| `task.created` | `task_id`, `project_id`, `title`, `priority`, `assignee_id`, `status_id` |
| `task.updated` | `task_id`, `project_id`, `changes` |
| `task.moved` | `task_id`, `project_id`, `old_status_id`, `new_status_id` |
| `task.assigned` | `task_id`, `project_id`, `assignee_id`, `assignee_type` |
| `project.created`, `project.updated` | `workspace_id`, `project_id`, `slug`, `name`, `is_archived` |
| `workspace.member.added`, `workspace.member.removed` | `workspace_id`, `user_id`, `role` |

Pinned by `internal/service/public_event_payload_contract_test.go`. The full
list of public event names is `internal/extensions/events.go`.

## Extension actor routes

Authenticated with `Authorization: Bearer ext_…`.

| Route | Pinned |
|---|---|
| `GET /ext/me` | `actor` (`ext:<id>@<workspace>`), `actor_id`, `extension_id`, `connection_id`, `workspace_id`, `capabilities` (consented, grouped by kind), `violations.{count,last_at,last}`; the token is never echoed; 403 for any other principal |
| `POST /ext/me/token/rotate` | `extension_token`, `token_prefix`, `token_length`, `actor`, `actor_id`, `connection_id`, `workspace_id`, `previous_valid_for`, `previous_valid_until`; `Cache-Control: no-store` |
| `DELETE /ext/me/token/previous` | 204 |

Pinned by `internal/handler/extension_actor_contract_test.go`.

Every other route an extension reaches is listed, with the capability it
needs, in `extensionRouteCapabilities` (`internal/middleware/extension_auth.go`).
A route outside the list, or one whose capability was not consented, answers
403 and counts as a violation. The list as published is pinned by
`internal/middleware/extension_routes_contract_test.go`, together with the
routes that stay closed to extensions by design (deciding a human gate,
checkout, workspace administration).

## What is a breaking change

Breaking, and raises `ExtensionsAPIVersion` (see [api-version.md](./api-version.md)):

- a field removed or renamed in a call Mesh makes, or a header renamed;
- a different signature scheme, or `X-Mesh-Delivery` changing between retries;
- a key removed or renamed in an event body, or an event renamed;
- a route removed from the extension list or moved to another capability;
- a key removed or renamed in the `/ext/me*` answers, or a status code changed.

Not breaking: new keys, new events, new routes for extensions. Receivers must
ignore unknown keys.

A contract test that turns red is the signal: either the change is a
regression, or it is breaking and needs the ADR and version bump. Never edit a
pinned table to match without one of the two.
