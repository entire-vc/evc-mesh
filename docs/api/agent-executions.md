# Agent executions: configured route and observed attempts (schema_version 1)

Read-only telemetry that sits beside the runtime catalog (`agent-runtime.md`).
It answers two separate questions for a project-connected Runner:

- **configured**: what the current runtime policy says should run (harness, model, order);
- **observed**: what a launched adapter actually confirmed, as a list of attempts.

Nothing here admits a binding, launches work, takes a lease or changes routing.
A failed report changes no task status, checkout or attempt outcome. The existing
catalog schema (v2) and `agents.model` / profile APIs are unchanged.

Shared fixtures: `docs/api/fixtures/agent-executions/*.json` (see the end).
Tests: `TestAgentExecution*`, `TestExecutionReport*` (`internal/handler`,
`internal/domain`, `internal/repository/postgres`).

## Endpoints (all under `/api/v1`)

| Method and path | Caller | Purpose |
|---|---|---|
| `GET /workspaces/:ws_id/agent-executions?project_id&agent_id&limit&cursor` | workspace member, bound agent | scoped batch projection for the org chart and agent detail |
| `POST /workspaces/:ws_id/runtime/controllers/:controller_ref/execution-instances` | the controller's exact reporter key | boot registration, returns a server-issued `generation` |
| `POST /workspaces/:ws_id/runtime/controllers/:controller_ref/execution-reports` | the controller's exact reporter key | one batch of attempts, up to 200 |

`:ws_id` of the two POST routes is the **owner** workspace (the one holding the
Runner catalog); the agents and projects named in a row belong to the receiver
workspace of the admitted binding. `:ws_id` of the GET is the workspace whose
agents are read.

### GET agent-executions

`limit` 1..200 (default 50); `cursor` is opaque; sort is project, agent. The
envelope is always

```json
{"schema_version":1,"generated_at":"…","max_age_seconds":120,"items":[ AgentExecutionView ],"next_cursor":null}
```

`next_cursor` is present on every page, `null` on the last one. A bad `limit`
or `cursor` is 400; a `project_id` outside the workspace is 404.

Visibility: a workspace owner/admin reads every project; another member reads
the projects they belong to; an agent key reads only its own telemetry, in the projects it belongs to; a
non-member, a connector and an agent of another workspace get 403. Read rights
carry no write rights.

`AgentExecutionView` is the contract payload plus two additive fields:

- `reason`: `null`, `"unmanaged"` (no Runner binding for the agent), `"integration_disabled"` (a binding exists but the `agent-runner` integration is off in the project) or `"disabled"`
  (managed, but binding/connection switched off). With a reason the item still
  carries `profile` (`source: "agent_profile"`); `configured` is `null` when unmanaged.
- per attempt: `freshness` may also be `history`, see below.

`managed`/`enabled` come from the server: a Runner connection binds the agent,
the project has the Runner extension enabled and the agent is a member
(`managed`); the binding is admitted and switched on (`enabled`). A missing
report does not make a managed project unmanaged: it is `freshness: "unknown"`.

`configured.routes[]` is the policy order of the admitted binding(s): primary
profiles first, then their quota-edge reserves. `harness`/`model` name a setting;
`effort` is `null` because the catalog has none.
`configured.revision` is the *current* server policy; an attempt's
`resolved_route.revision` is immutable evidence of what that attempt selected, and
`policy_matches` says whether the two are the same revision (and digest, if sent).

Each attempt carries `attempt_id`, `controller_ref`, `binding_ref`, `task_id`,
`instance_id`, `generation`, `sequence`, `turn_id`, `state`, `resolved_route`,
`observed {harness, model, effort, source, observed_at, provenance?}`,
`received_at`, `heartbeat_at`, `stale`, `freshness`, `policy_matches`. Several
projects, controllers and turns are listed side by side; nothing is collapsed
into one model per agent and nothing is written to `agents.model`.

### POST execution-instances

`{"schema_version":1,"instance_id":"<opaque>"}` → `{"schema_version":1,"instance_id","generation","received_at"}`.
Generation is monotone per controller. Repeating the same `instance_id` returns
the original row and changes nothing. This is telemetry fencing only: not a
controller claim and not a launch or checkout lease.

### POST execution-reports

```json
{"schema_version":1,"instance_id":"boot-3","generation":3,"sequence":12,"reported_at":"…","attempts":[AttemptReport]}
```

Answer: `{"recorded":true,"duplicate":false,"attempts":N,"launch_authorized":false}`.

Rules, checked in this order; any failure stores nothing (atomic):

1. Body: unknown or duplicate fields at any depth, secret-bearing fields and bodies over 1 MiB are 400.
   `null` is legal (unknown model, no task). Opaque ids and observed values are single tokens
   (`[A-Za-z0-9][A-Za-z0-9._:+-]{0,99}`), so a path, space or address cannot ride along.
2. Key: only the controller's exact reporter key of the owner workspace; users, connectors, the
   receiver's agent and a revoked or rotated grant are 403.
3. Fencing: the instance must be registered with that generation, and no newer generation may exist (409).
4. Sequence: the same instance+sequence+body is idempotent (`duplicate:true`); the same sequence with another
   body, or a lower sequence, is 409.
5. Time: `reported_at` and every `observed_at` in the future are 400.
6. Each row: its `binding_ref` is in the owner catalog and bound to that `workspace_id`/`agent_id`; the
   controller serves one of the binding's profiles; the binding is admitted; the project exists in the
   receiver workspace, the agent is a member and the Runner is enabled for it (a finished attempt may still be
   closed after the Runner was turned off); `task_id`, if present, belongs to the project. Any miss is the same
   generic 403, so a reporter learns nothing about foreign data.
7. Attempt: a finished attempt (`stopped`/`completed`/`failed`) never runs again (409); a new `turn_id` resets
   model and effort to unknown, so it may name a model only with an `observed_at` newer than the previous one (400).
   A resumed session that the new process re-attaches is a new `attempt_id`.

Rows are upserted by (project, agent, controller, attempt); an attempt missing from a batch is left alone.
Finished attempts and old envelopes are pruned after 7 days.

A read returns at most 100 attempts per project and agent, the newest by receive time. When more exist the item carries `"attempts_truncated": true`; the field is absent otherwise.

`AttemptReport`: `workspace_id`, `project_id`, `agent_id`, `binding_ref`, `task_id|null`, `controller_ref` (must equal
the URL), `instance_id`/`generation` (must equal the envelope), `attempt_id`, `turn_id|null`,
`state` (`starting|running|waiting|stopped|completed|failed`),
`resolved_route {source, revision, digest?, profile_ref?, harness?, model?, effort?}`,
`observed {harness|null, model|null, effort|null, source, observed_at}`, optional `provenance {harness?, model?, effort?: {source, observed_at}}`.
Never accepted: environment, prompt/output, argv, paths, account e-mail, credential refs, tokens, free-form metadata.

## Freshness

Publisher refresh 30 s; window 120 s (`max_age_seconds`). Age is judged by server `received_at` **and** the
publisher's `reported_at`, so a late delivery cannot revive old data. A heartbeat never makes an unknown model known
and `heartbeat_at` is not the model's observation time (`observed.observed_at` is).

| freshness | when |
|---|---|
| `history` | `stopped`/`completed`/`failed`, whatever the age; also every attempt of an item that is not enabled |
| `offline` | controller disabled, paused or without its live reporter grant, telemetry written under a since-rotated grant, or heartbeat older than the window |
| `stale` | generation is not the controller's newest, or `reported_at`/`received_at` older than the window |
| `unknown` | otherwise fresh, but the model of the current turn is not confirmed (`observed.model` is null) |
| `current` | none of the above |

Item `freshness` summarizes without choosing one attempt: `current` > `stale` > `offline` > `unknown`; history is ignored.
`history` is an addition to the four values of the root contract; consumers must treat it as "not running".

## Fixtures

`docs/api/fixtures/agent-executions/<name>.json`, each `{name, kind, now, request|response, expect}`:
`managed-current`, `managed-no-usage`, `managed-model-unknown`, `two-attempts-two-models`,
`policy-revision-mismatch`, `resume-model-reset`, `controller-restart`, `late-old-publisher`, `unmanaged`,
`disabled` (read responses, with an `expect.jq` check) and `forbidden-foreign` (403, DB-enforced),
`rejected-secret-field` (400) (report requests). `TestAgentExecutionFixture*` decodes every response strictly into the
server types, so a UI or publisher that follows the fixtures cannot drift from the wire.

Receipt checks (against a saved read):

```sh
jq -e '.schema_version == 1 and (.items | length > 0) and all(.items[]; .managed == true and .configured.source == "runtime_policy")' canary-read.json
jq -e '[.items[].attempts[] | select(.state == "running" and .freshness == "current")] | length == 2' concurrent-read.json
jq -e 'all(.items[].attempts[]; .observed.model == null or .observed.source != "agent_profile")' no-usage-read.json
```

## Rollback

Migration `20261031200_runtime_execution_observations.sql` only adds three tables; its Down drops them. The previous
image never names them, so rolling back the image first and then the migration is safe; `TestRuntimeExecutionMigration_UpDownUp`
and the CI `migration-reversibility-gate` cover Down.
