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
| `POST /agents/me/sessions/report` | `session_id`, `totals.{tokens_in,tokens_out,estimated_cost,model_used}` plus `totals.{tokens_in,tokens_out,estimated_cost}_incomplete`; additive per task; request field `unknown`; `Idempotency-Key` accepted; 400, 401, 501 |

Retried writes: `POST /tasks/:id/comments`, `/tasks/:id/artifacts`,
`/tasks/:id/finish`, `/tasks/:id/move` and `POST /agents/me/sessions/report` accept an `Idempotency-Key` header; a repeat returns the
first answer instead of writing again (`docs/idempotency.md`). Events carry a
stable `event_id` across retries and subscribers (`docs/webhooks.md`).

### Reconciling a lost session report

A report is additive, so repeating it without a key counts it twice. To make a
report safe to repeat, send one `Idempotency-Key` per report (a UUID) and, if the
answer is lost, send the **same body with the same key** again:

- the first report was applied: the repeat returns the first answer with
  `Idempotent-Replayed: true` and adds nothing;
- the first report never arrived: the repeat is the first and is applied once;
- the same key with another body answers `409 idempotency_key_reused` and adds
  nothing. A new report is a new key.

Concurrent reports (different keys) sum exactly onto the one active session for
the agent and task; two first reports do not create two sessions. A caller that
sends no key behaves as before. Rules for keys: `docs/idempotency.md`.

### Unknown usage is not zero

A caller that does not know its tokens or cost says so instead of sending `0`.

Request (`POST /agents/me/sessions/report`), new optional field:

```json
{ "tokens_in": 1200, "model": "m-1", "unknown": ["tokens_out", "estimated_cost"] }
```

- `unknown`: any of `tokens_in`, `tokens_out`, `estimated_cost`, each separately. A listed
  measure adds nothing; sending a non-zero number for it, or a name outside the three, is `400`.
- Absent or empty `unknown`: every measure is a known figure, exactly as before (an omitted
  number is a known `0`). Callers that send numbers today change nothing.
- The session keeps the fact. It is never cleared by a later report: known + unknown is an
  **incomplete** total, not a clean number.

Response `totals`: `tokens_in`, `tokens_out`, `estimated_cost` are the known sum of the
session, `null` when no report gave a known figure (never `0`); `tokens_in_incomplete`,
`tokens_out_incomplete`, `estimated_cost_incomplete` are `true` when any report said the
figure is unknown or none gave one. A real `0` stays `0` with the flag `false`.

Summary, `GET /tasks/{task_id}/cost-summary`:

```json
{ "total_cost": 3.0, "tokens_in": 157, "tokens_out": null,
  "cost_incomplete": true, "tokens_in_incomplete": true, "tokens_out_incomplete": true,
  "session_count": 2, "rework_count": 0, "quality_flag": "multi-turn" }
```

- `total_cost`, `tokens_in`, `tokens_out`: sum of the sessions' known figures; `null` when no
  session has one. A task with no sessions answers `0` and no flags, as before.
- `cost_incomplete`, `tokens_in_incomplete`, `tokens_out_incomplete`: per measure, `true` when
  some session's figure is unknown or was never reported. The three flags are independent.
- Workspace analytics (`cost_metrics`): `cost_incomplete`, `tokens_in_incomplete`,
  `tokens_out_incomplete` are `true` when a reporter said the figure is unknown for a session
  in the period; the totals are the known sum.

Pinned by `TestContract_AgentsMeSessionsReport_UnknownMeasures` (wire) and
`TestUnknownUsage_*` in `internal/repository/postgres` (all known, all unknown, mixed across
reports and sessions, real zero, plain numbers).

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
results, and do not retry the checkout in a loop.

**Result writes take the same precondition.** `POST /tasks/{id}/comments`,
`/artifacts`, `/finish` and `/move` accept optional headers naming the lock you hold:

| Header | Value |
|---|---|
| `X-Mesh-Checkout-Token` | the `checkout_token` of your checkout |
| `X-Mesh-Checkout-Session-Id` + `X-Mesh-Checkout-Generation` | instead of the token: your `session_id` and `generation` (both; bound to the calling agent) |
| `X-Mesh-Expected-Input-Revision` | optional, alone or with the lock: the task's `input_revision` you worked from |

With them the write commits only if, at the moment of the write and under the same
row lock, you hold exactly that live lock (and `input_revision` is unchanged).
Otherwise nothing is written and the answer is `409`:

| `error_code` | Meaning |
|---|---|
| `checkout_precondition_failed` | the lock is gone: a newer generation (even your own), expired, released or force-released |
| `input_revision_changed` | title, description, custom fields or delegation level changed since you read `input_revision`; `context.input_revision` has the current one |

A malformed header is `400` and writes nothing. Without the headers the routes behave
as before, which means an older attempt of the same agent, or one whose lock expired
and nobody took, can still publish: send the precondition on every result write.
Together with `Idempotency-Key` the stored answer wins: a retry of a write that
happened is replayed even after the lock is gone, a write that did not happen is
not performed under a lost lock (`docs/idempotency.md`).

**Reassignment does not touch the lock (yet).** Assigning the task to someone else
(`POST /tasks/{id}/assign` or a move that changes the assignee) leaves the current
checkout as it is: the holder's extend and fenced writes still pass, and the new
assignee's checkout answers `409` until that lock ends. Ending the previous holder's
lock on reassignment is a later, separate change of this contract.

**Handing a task to a new process.** A lock belongs to one `session_id`; the same agent
cannot take over its own live lock from a new session (the checkout answers `409`,
only a retry with the same `session_id` + `request_id` gets the same lease back). To
move work to a new process, release with the old `session_id` + `generation`
(`DELETE /tasks/{id}/checkout`), then check out again from the new session; the new
lease has a higher `generation`, and every fenced write of the old process is refused
from then on.

**`input_revision`** is in the task JSON. It advances only when the assignment content
changes: `title`, `description`, `custom_fields` or `delegation_level`. Checkout, extend, release,
heartbeat, `runtime_state`, comments, artifacts, status moves and assignee changes
leave it, unlike `version` and `updated_at`, which advance on every row write.
Project-level settings (DoD gate definitions, rules) are not part of it.

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
