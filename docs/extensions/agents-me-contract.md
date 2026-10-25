# Agent API contract: `/api/v1/agents/me/*`

External runners (`entire-vc/evc-mesh-runner`, manifest `mesh-extension.json`,
`mesh_api: ">=1 <2"`) authenticate with `X-Agent-Key` and use these routes. The
schema is in `docs/openapi.yaml`; the wire format is pinned by
`internal/handler/agent_me_contract_test.go`.

```
go test ./internal/handler -run Contract
```

## Covered endpoints

| Endpoint | Pinned |
|---|---|
| `GET /agents/me` | `id`, `workspace_id` (workspace the key authenticated into), `home_workspace_id`, `name`, `agent_type`, `status`; no key hash; 401 without key; 500 when the key workspace is unresolved |
| `GET /agents/me/tasks` | `tasks`, `count`, `total_count`, `has_more`; filters `status_category`, `project_id`, `limit`/`page_size`; empty feed is `[]`; 400 bad filter, 401, 403 |
| `GET /agents/me/tasks/poll` | same body plus `changed`; `false` on timeout; bad filter answers 400 before the wait |
| `GET /agents/me/events/stream` | SSE frames `id:` / `event:` / `data:`; event types `task.assigned`, `task.updated`, `comment.created`; replay in order via `Last-Event-ID`; 410 `cursor_expired` (keys `error`, `error_code`, `code`, `message`), 400 malformed cursor, 401 |
| `POST /agents/me/sessions/report` | `session_id`, `totals.{tokens_in,tokens_out,estimated_cost,model_used}`; additive per task; 401, 501 |

Not covered here: checkout/release (`/tasks/:id/checkout`, the `checkout_token` and
`release_task` quirks) live under `/tasks/*`, not `/agents/me/*`; they are a separate unit.

## What is a breaking change

Breaking, and raises `ExtensionsAPIVersion` (see `api-version.md`):

- removing or renaming a route, a response key, a query parameter, or an SSE event type;
- changing a key's type or meaning (`has_more` from boolean, `tasks` from array);
- changing a status code a client branches on (410 for an expired cursor, 403/401 split);
- changing SSE framing (`id:`, `event:`, `data:` lines).

Not breaking: new optional response keys, new query parameters, new event types,
new routes. Clients must ignore unknown keys.

A contract test that turns red is the signal: either the change is a regression,
or it is breaking and needs the ADR and version bump from `api-version.md`. Never
edit the test to match without one of the two.
