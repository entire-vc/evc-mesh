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

Retried writes: `POST /tasks/:id/comments`, `/tasks/:id/artifacts` and
`/tasks/:id/finish` accept an `Idempotency-Key` header; a repeat returns the
first answer instead of writing again (`docs/idempotency.md`). Events carry a
stable `event_id` across retries and subscribers (`docs/webhooks.md`).

Checkout/release live under `/tasks/*`, not `/agents/me/*`, and have no contract
test in this file; their behaviour is described in the next section.

## Taking a task: checkout, extend, release

An external agent that works a task holds a **checkout**: a time-limited lock that
tells Mesh and the other agents the task is in someone's hands. Only an agent can
hold one (`X-Agent-Key`); other callers are refused.

### Checkout

`POST /api/v1/tasks/{task_id}/checkout`

```json
{ "ttl_minutes": 60, "session_id": "<uuid>", "request_id": "<uuid>", "session_metadata": { "host": "worker-1" } }
```

- `ttl_minutes`: default 15, capped at 240.
- `session_id` + `request_id`: optional, but send both or neither (`400` otherwise).
  Generate `session_id` once per worker run and `request_id` once per checkout attempt.
  Repeating the same pair while the lock is live is a safe retry: it returns the same
  lock, token and deadline instead of a new one.
- `session_metadata`: free-form, written to the activity log only.

`200`:

```json
{ "task_id": "…", "checkout_token": "<uuid>", "checked_out_by": "<agent id>", "expires_at": "…",
  "generation": 7, "session_id": "…", "request_id": "…", "project_id": "…", "delegation_level": "…" }
```

Keep `checkout_token` and `generation`: extend and release name the lock by one of
them (a lock taken without `session_id` can also be released by its holder with an
empty body). A task in `todo` is moved to the project's first `in_progress` status
when the checkout succeeds; the move is best-effort (no such status, or a workflow
refusal, leaves the task in `todo`) and never fails the checkout itself.

Refusals:

| Status | When |
|---|---|
| `409 task_already_checked_out` | another agent holds a live lock; `details` has `checked_out_by`, `checked_out_by_name`, `expires_at`, `expires_in_seconds` |
| `400` | task is `done`/`cancelled`, or the caller is not an agent |
| `403 supervised_mode_requires_manual_start` | a `supervised` task in `todo`: a person must start it |

A lock that expired counts as free: the next checkout takes it.

### Extend

`PATCH /api/v1/tasks/{task_id}/checkout`

```json
{ "checkout_token": "<uuid>", "ttl_minutes": 60 }
```

or, without the token, `{ "session_id": "<uuid>", "generation": 7, "ttl_minutes": 60 }`.
The new deadline is now + `ttl_minutes` (same default and cap). Extend before
`expires_at`; an expired lock cannot be extended (`403`), take a new checkout instead.

Projects that enable `mid_pipeline.heartbeat_extends_checkout` also extend an agent's
locks on every heartbeat, but only locks taken **without** `session_id`. Do not rely on
it: extend explicitly.

### Release

`DELETE /api/v1/tasks/{task_id}/checkout` with one of:

```json
{ "checkout_token": "<uuid>" }
{ "session_id": "<uuid>", "generation": 7 }
```

`204` on success, and also when there is nothing to release. Moving the task to a
`review`, `done` or `cancelled` status, or closing it with `finish_task`, releases the
lock automatically. In every other case (you stop or give the task back while it stays
`todo`/`in_progress`) release explicitly.

A lock that is neither extended nor released expires. Mesh then returns an
`in_progress` task to `todo` so another agent can take it.

### Fencing a stale worker

Every new checkout of a task (by anyone) increments `generation` and issues a new
`checkout_token`. Extend and release compare the token, or `session_id` + `generation`,
in one atomic update, so a worker whose lock expired and was taken over cannot extend
or release the new holder's lock: it gets `403 checkout precondition does not match`
(extend: `invalid or expired checkout precondition`).

Treat that `403` as **"you lost the task"**: stop work on it, do not write further
results, and do not retry the checkout in a loop. Fencing covers the lock calls and
closing the task through `finish_task` (refused with `409` while another agent holds a
live lock); ordinary writes such as comments are not fenced, so a worker must check
its lock (extend it) before it posts results.

Recovery of a stuck lock by force (`DELETE …/checkout?force=true` with
`previous_holder`, `generation`, `reason`) is for a workspace owner or admin signed in
as a person, never for an agent key.

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
