# Agent runtime integration — schema and API v2

Status: implementation contract. Routes below are planned until the accompanying
backend implementation is merged and deployed. This document grants no launch
permission. Runtime adapters and handoff remain external controller responsibilities.

The integration provider is `agent_runtime`. It uses the existing workspace
`integration_configs` connection and existing `agent_workspace_grants`. One
connection contains several controller instances; it is not one controller per
workspace. Project integrations keep their existing meaning. Agent bindings add a
second binding type; they never duplicate agent identity or Mesh key material.

## Connection and resource ownership

`docs/api/agent-runtime.schema.json` specifies the desired catalog. It is strict:
unknown fields are rejected, including credentials, arbitrary settings and client
claims of launch/QA authorization. Credentials are opaque references to controller
storage, never secret values or host file contents. A catalog belongs to its resource
owner workspace. Resource ownership and receiving-workspace admission are checked
separately. Receiving membership alone never authorizes editing or spending a
foreign resource. Owner administration requires a human workspace owner/admin.

Controllers have independent IDs, host/version/capabilities and an explicitly bound
reporter grant. One physical controller may have connections to several workspaces;
it authenticates each using that workspace's grant. Accounts identify provider
resources, not emails. Each execution mode maps an account to all applicable pools.
Pool identity is canonical `(owner workspace, provider, resource_ref)` across
controllers/connections; aliases resolve to that identity before admission or
reservation. Multiple credentials or hosts do not increase capacity. Numerical
thresholds are explicit verified adapter settings, never invented defaults.

Each binding contains the exact `agent_id`, `workspace_id`, `grant_id`, permitted
profile IDs and routing policy. Grant existence, matching identities, revocation,
receiving-workspace admission and resource-owner permission are freshly checked.
No home-key fallback is allowed. Revoking one grant blocks its new admissions while
leaving another valid grant independent. Resource summaries returned to a receiver
contain only its permitted profiles and availability, never foreign tasks or keys.
Receiver admission is separately persisted and defaults to denied, including
rotation/reinvitation of a stable grant ID. Its internal grant-key fingerprint
never appears in API output. Catalog content changes also require renewed
receiving approval for every binding whose content changed: the binding itself,
its permitted profiles, their accounts, pools and controller host. A save that
leaves all of that unchanged carries the admission to the new digest in the same
transaction (same grant and key only; the admission revision stays), so editing
one binding never denies the others. Disabling without changing content retains approval while
denying every new admission. A resource owner's catalog cannot grant
itself receiving-workspace approval. Effective profiles intersect owner bindings
and the receiver's admitted profile set. Receiver policy never rewrites the
global preferences of a foreign agent identity.

## Versioned routes

All paths below are under `/api/v1`. Workspace and object scopes must also be
checked inside the service/repository; middleware is not the only tenant boundary.
Binding routes require the query parameter `resource_owner_workspace_id` to
identify the owner connection explicitly. Binding references are connection-local,
and the service does not search foreign catalogs by a name supplied by the caller.

| Method/path | Request / response | Authority |
| --- | --- | --- |
| GET `/workspaces/:ws_id/runtime` | desired revision/catalog, bounded controller apply status, inventory | owner workspace admin; receivers use the binding route |
| PUT `/workspaces/:ws_id/runtime` | `{if_revision, enabled, config}` → new desired revision/digest; an existing account sent with an empty or missing `credential_ref` keeps the reference stored in the current revision, so the redacted `GET` catalog can be edited and sent back. Receiving admissions of bindings whose approved content (binding, permitted profiles, their accounts, pools, controller host) is unchanged move to the new digest in the same transaction; the others need renewed approval | resource owner admin; atomic CAS; stale writer gets 409 |
| GET `/workspaces/:ws_id/runtime/revisions` (`?limit=` 1..200, default 50) | `{managed, revisions: [{revision, digest, enabled, author_id, author_name, created_at}]}` newest first; `managed` is false (and the list empty) when the workspace has no runner connection | owner workspace admin |
| POST `/workspaces/:ws_id/runtime/controllers/:controller_ref/report` | versioned apply/heartbeat/capabilities/pool observations | exact configured reporter grant in this workspace; no generic user/client claims |
| GET `/workspaces/:ws_id/runtime/controllers/:controller_ref/desired` | `{revision, digest, enabled, drain_requested, controller_ref, controller, catalog:{accounts, pools, profiles, bindings}}`: only this controller's profiles, their accounts (with `credential_ref`, a reference, never a secret value), the pools those accounts use and bindings permitting one of its profiles (permitted profiles, primary profiles, quota edges and preferred accounts narrowed to this controller). `ETag` is a hash of the whole response body (revision, enabled and slice); a matching `If-None-Match` gives 304 | exact configured reporter grant of this controller; another key 403, unknown ref 404; read-only |
| POST `/workspaces/:ws_id/runtime/controllers/:controller_ref/quota-observations` | `{schema_version:1, observations:[{pool, provider, window, unit, used_fraction?, remaining?, reset_at?, observed_at, source, confidence, max_age_seconds, status, error_kind?}]}` → `{recorded, launch_authorized:false}`. Additive to `report`; informational, never an admission decision. Unknown measurements are **omitted** (the strict decoder rejects JSON `null`) and are stored and returned as `null`, never `0`. `used_fraction` is 0..1, `remaining` ≥ 0, `observed_at` must not be in the future, statuses without data (`unknown`, `auth_error`, `network_error`) carry no numbers; the whole batch is validated and stored atomically (≤256); the latest observation per controller/pool/window wins and an older one never overwrites a newer one; pool aliases resolve to the canonical pool | exact configured reporter grant of this controller; a pool outside the controller's accounts 403, provider mismatch / duplicate window / invalid value 400, unknown controller 404 |
| GET `/workspaces/:ws_id/runtime/controllers/:controller_ref/quota-observations` | `{controller_ref, observations:[…, received_at, stale]}`; `stale` is computed at read time from `max_age_seconds`, unknown values are `null` | owner workspace human admin or the controller's reporter grant; workers, connectors and anonymous 403 |
| GET `/workspaces/:ws_id/runtime/bindings/:binding_id` | desired/actual profile, preferred account, availability and reasons | exact bound agent grant or authorized receiving admin |
| PUT `/workspaces/:ws_id/runtime/bindings/:binding_id/admission` | `{if_revision, permitted_profiles, enabled}` | receiving workspace admin; independent CAS; initially denied |
| PUT `/workspaces/:ws_id/runtime/bindings/:binding_id/policy` (`?resource_owner_workspace_id=`) | `{if_revision, permitted_profiles?, policy}` (strict, unknown fields 400; `policy` is the whole routing block of the schema) → the binding view of `GET .../bindings/:id` with the new catalog `revision` and `digest` | resource owner workspace admin (human; agent keys and connectors 403); the server edits only this binding inside the stored catalog, so `credential_ref` and every other entry stay untouched and the client never resends the redacted catalog; same validation as `PUT /runtime` (profiles within permitted, known accounts, explicit API-reserve opt-in); atomic CAS on the catalog revision, stale writer gets 409 `stale_revision`; every save is a new immutable revision. Like any catalog save it changes the digest; the edited binding's admission moves to it (a policy edit only reorders profiles the receiver admitted), other bindings follow the `PUT /runtime` rule |
| POST `/workspaces/:ws_id/runtime/bindings/:binding_id/preview` | purpose, source profile, required capabilities, optional artifact reference | freshly authorized binding; server resolves observations and provenance |
| POST `/workspaces/:ws_id/runtime/bindings/:binding_id/reservations` (+ `/:reservation_id`, `/consume`, `/renew`, `/release`) | atomic execution admission, see "Execution admission" | exact bound agent grant |
| GET `/workspaces/:ws_id/runtime/capacity` (`?agent_id=` optional) | derived execution-state projection per agent identity, see "Capacity projection" | human member of `:ws_id` or an agent key presented for `:ws_id`; no OAuth connectors |
| POST `/workspaces/:ws_id/runtime/artifacts/:provenance_artifact_id/provenance` | `controller_ref`, exact `artifact_revision`, `complete`, authors (agent id, model developer, model family); authors merge, never shrink | trusted reporting controller, bound execution scope and persisted attestation |

Schema v2 is independent of monotonically increasing desired revision. Every save,
including rollback, creates a new immutable revision. `if_revision=0` creates only
when absent. Controller apply reports contain the exact revision and digest:
future, mismatched and stale reports cannot mark the current revision applied.
A report may carry the controller's own build `version` (optional, at most 64
characters of `0-9A-Za-z.+_-`); the Runner screen shows it in place of the
catalog entry's `version` and marks a catalog entry that says otherwise.
While the `agent-runner` extension is installed, every read of the catalog
(inventory, save response, desired) gives each controller the Installed
version of that extension as its `version`: Update moves it, so the catalog
never lags behind an update. The stored entry's `version` is kept only for
an instance without the extension and in revision history. Saving
a catalog without a binding disables that binding's receiving admission.
Heartbeat is separate from apply success. Controller rejection/capability mismatch
and stale heartbeat remain visible. Local emergency pause survives synchronization.
Disabling the integration denies new admissions and requests drain of existing
executions; it never falls back to environment. Absence of a connection preserves
direct mode and existing agent APIs, with unknown runtime facts.

## Routing, preferred accounts and shared pools

The generated policy projection matches the controller’s `runtime-routing-policy-v2`:
`schema_version`, exact `binding`, `accounts` with `quota_pools_by_mode`, `profiles`
with string revision, `primary_profiles`, `quota_edges`, `preferred_accounts`,
`api_reserve_providers`, `max_attempts`, `evidence_max_age_seconds`. The projection
is advisory; `launch_authorized` is always false. Catalog fields are mapped to this
single contract, not a competing runtime configuration. Native probes/atomic
reservation/fencing/handoff are coordinated with the existing controller protocol.

Preference is soft `agent + provider → preferred_account_ref` within permitted
binding profiles. Select an authorized compatible preferred account while every
applicable pool has fresh verified availability, even when another account has
more headroom. Several agents may share the preference; capacity remains global.
Only confirmed quota exhaustion or a verified configured threshold opens quota
reserve edges. Unknown/auth/network/manual pause/grant denial never masquerade as
quota events. Exhausted pool aliases exclude all profiles sharing that pool.
Independent API pools on the same account may be eligible: account inequality is
not the resource-independence check. API must be an explicit primary or an opted-in
reserve for this agent and provider. No task/project/workspace token quota or
mandatory local API budget is introduced. Reset affects new selections; a running
reserve execution is never forcibly returned to the preferred account.

Explain responses contain a stable reason per excluded profile, selected and
preferred/actual account references, observation freshness and policy revision.
Preview performs no launch, reservation, task status update or account switching.
Concurrent capacity reservation is an authority boundary, not a side effect of GET
or preview. Before a controller starts work it must atomically obtain/fence the
existing exact-grant execution authority and all canonical resource pools.

## Execution admission: reservation, consume, release

Status: implementation contract (R3). This is the only execution authority of the
runtime integration; preview and policy projection stay advisory. Direct mode
(no `agent_runtime` connection, or an agent without a binding) does not use
these routes and is unaffected.

All four routes live under `/api/v1/workspaces/:ws_id/runtime/bindings/:binding_id`
and require `?resource_owner_workspace_id=<owner ws>`. `:ws_id` is the receiving
workspace of the binding.

| Method/path | Body → response | Caller |
| --- | --- | --- |
| POST `/reservations` | `AcquireRequest` → `201 Reservation` (replay: `200`, same body) | exact bound agent key (agent + receiving workspace + active binding grant) |
| POST `/reservations/:reservation_id/consume` | `ConsumeRequest` → `200 Reservation` with `consume_receipt` | same bound agent key |
| POST `/reservations/:reservation_id/renew` | `RenewRequest {fence, checkout_request_id, checkout_generation, run_lease_seconds}` → `200 Reservation`; extends the run lease of a consumed (or lapsed, `reconcile`) reservation, never shortens it; a heartbeat, safe to repeat (each call only moves the horizon to `now + run_lease_seconds` for the live writer, so a retry after a lost response cannot harm) | same bound agent key, exact original writer that still holds its live checkout and grant |
| POST `/reservations/:reservation_id/release` | `ReleaseRequest` → `200 Reservation` with `release_receipt` | bound agent key (grant revocation does not block release) or receiving workspace owner/admin |
| GET `/reservations/:reservation_id` | → `200 Reservation` | bound agent key or receiving workspace owner/admin |

Request bodies are strict JSON (unknown/duplicate fields → 400); every field
listed is required (`checkout_request_id` may be `null`).

```
AcquireRequest {
  idempotency_key: string        // attempt key, ^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,199}$
  profile_id: string             // must be in the effective admitted profile set
  task_id: uuid                  // task in the receiving workspace
  checkout_generation: int64     // tasks.checkout_generation of the caller's live checkout
  checkout_request_id: uuid|null // tasks.checkout_request_id of that checkout (writer request)
  worker_ref: string             // controller-local worker slot, same pattern as idempotency_key
  expected_catalog_revision: int64, expected_catalog_digest: string   // binding revision + digest
  expected_admission_revision: int64                                  // receiving admission revision
  expected_profile_revision: string                                   // projection profiles[].revision
  expected_pool_set_digest: string
  ttl_seconds: int               // 10..900, lifetime of the UNCONSUMED reservation
}
ConsumeRequest { fence: int64, checkout_generation: int64, run_lease_seconds: int /* 60..86400 */ }
ReleaseRequest {
  fence: int64, checkout_request_id: uuid|null, checkout_generation: int64,
  stopped: bool,
  proof: { kind: "no_child" | "stopped_process_birth_proof" | "not_started",
           evidence_ref: string /* required for stopped_process_birth_proof, "" otherwise */ }
}
Reservation {
  reservation_id: uuid, fence: int64,
  state: "reserved" | "consumed" | "reconcile" | "released" | "expired",
  resource_owner_workspace_id, binding_ref, agent_id, workspace_id, grant_id,
  task_id, checkout_generation, checkout_request_id, worker_ref, controller_ref, profile_id,
  catalog_revision, catalog_digest, admission_revision, profile_revision,
  pools: [canonical pool id], pool_set_digest,
  expires_at, created_at,
  consume_receipt: null | { receipt_id: uuid, consumed_at, run_lease_expires_at },
  release_receipt: null | { release_id: uuid, released_at, proof_kind }
}
```

The response never contains API keys, grant-key fingerprints, credential
references, the idempotency key or any other agent's tasks.

**Pools are server-derived.** The client never names a pool. For the profile the
server takes `accounts[profile.account_ref].quota_pools_by_mode[profile.execution_mode]`,
resolves aliases, maps each to the canonical id
`<owner workspace id>:<provider>:<resource_ref>` (the same ids as the policy
projection), de-duplicates and sorts them. `pool_set_digest` = lowercase hex
SHA-256 of those ids joined with `\n`. The client only sends the digest it
expects; a mismatch is a stale-revision conflict.

**Acquire** runs in one transaction and either reserves everything or nothing:
1. Caller is the exact bound agent key; the binding grant is active and its key
   unchanged (else 403). Same `idempotency_key` for this agent → the stored
   reservation is returned unchanged (`200`), whatever its state, provided the
   whole request scope is identical; a different scope → 409 `idempotency_scope_mismatch`.
2. Connection enabled, binding enabled, receiving admission enabled, profile and its
   controller enabled (else 423 `runtime_disabled`); profile admitted (else 403).
3. Catalog revision+digest, admission revision, profile revision and pool-set
   digest equal the expected values (else 409 `stale_revision`).
4. Task belongs to the receiving workspace and is checked out by this agent with
   a live lease, the given generation and writer request (else 409 `writer_lease_mismatch`).
5. Identity cap: `agents.max_concurrent_tasks` counts the agent's reserved +
   consumed + reconcile reservations across all workspaces, bindings and
   controllers. `0` (unset) → 423 `identity_cap_unset` (no invented default);
   reached → 409 `identity_cap_reached`.
6. One active reservation per task (409 `task_writer_active`) and per
   `(owner connection, controller, worker_ref)` (409 `worker_active`).
7. Every canonical pool has fewer active claims than its `max_concurrency`
   (else 409 `pool_exhausted`).
8. A new monotonically increasing `fence` is issued; state `reserved`,
   `expires_at = now + ttl_seconds`.

**Consume** is the durable start-consumed CAS, executed after the controller
re-checks STOP/pause/gates/dependencies and its own writer lease, right before
spawn. It succeeds only if the fence and checkout generation match, the
reservation is `reserved` and unexpired, and at commit time the catalog
revision/digest, admission revision, profile revision, grant (active, same key)
and the task's live checkout (agent, generation, writer request) are still the
ones bound at acquire. It writes `consume_receipt`; `expires_at` becomes
`run_lease_expires_at`. A replay on a consumed reservation with the same fence
and generation returns the same receipt (also while `reconcile`); once the
reservation is `released` or `expired`, consume answers 410 so a late replay can
never authorize a start. A lost consume response is reconciled
by `GET` or by replaying the acquire with the original `idempotency_key`; never
by a new reservation.

**TTL and reconcile.** Only an unconsumed reservation expires (`expired`, all
claims freed). A consumed reservation past `run_lease_expires_at` is reported as
`reconcile` and stays occupied: timeout, missing PID or controller restart never
free capacity. Lowering `max_concurrent_tasks` or `max_concurrency` never revokes
an active reservation; it only refuses new acquires.

**Release** frees the claims only with the exact `reservation_id`, `fence`,
`checkout_request_id` and `checkout_generation` of the original writer (else 409
`writer_mismatch`), `stopped: true` and a positive proof: `no_child` or
`stopped_process_birth_proof` (with `evidence_ref`); `not_started` is accepted
only while the reservation is still `reserved`. `stopped: false`, a missing or
unsupported proof → 409 `release_unproven`, the reservation stays occupied.
Release replay with the same writer returns the same `release_receipt`; it never
touches a newer reservation of the same task or worker. Releasing an already
`expired` reservation returns it unchanged.

**Errors** use the standard body `{code, message, details}`; `details` carries
the stable reason: 400 invalid request; 403 not the bound caller, revoked/rotated
grant, profile not admitted; 404 unknown binding/reservation/task (also for a
foreign one); 409 `idempotency_scope_mismatch`, `stale_revision`,
`writer_lease_mismatch`, `identity_cap_reached`, `task_writer_active`,
`worker_active`, `pool_exhausted`, `fence_mismatch`, `writer_mismatch`,
`release_unproven`, `not_consumed` (renew before consume); 410
`reservation_expired` / `reservation_released` (consume or renew too late); 423
`runtime_disabled`, `identity_cap_unset`, `controller_paused` (the profile's
controller reported `emergency_paused`), `controller_unavailable` (its latest
report is missing, stale, rejected, for another revision or grant). New
acquire needs a current controller; an idempotent replay of an existing
reservation does not. Disabling the
connection or revoking the grant denies new acquire and consume; existing
reservations stay occupied until released or (if unconsumed) expired.

## Capacity projection (read-only)

`GET /workspaces/:ws_id/runtime/capacity` answers, for every agent identity of
the workspace (home agents and agents with an active grant; `?agent_id=` narrows
to one, unknown → 404), how much execution capacity is configured, occupied and
ready. It is derived from durable state in one read-only snapshot and admits
nothing: only `POST /reservations` grants execution. Direct mode (no runtime
connection) uses the same projection; its occupancy is the held checkouts.

```
RuntimeCapacity { workspace_id, observed_at, source: "durable_state", agents: [AgentCapacity] }
AgentCapacity {
  agent_id, name,
  configured,     // agents.max_concurrent_tasks (0 = unset)
  effective,      // configured, or 0 when unset (no invented default)
  reserved,       // unconsumed, unexpired reservations
  running,        // consumed reservations inside their run lease
  reconcile,      // consumed reservations past their run lease (unknown)
  writers,        // held, live task checkouts without an active reservation
  stale_writers,  // checkouts still held past their lease expiry (unknown)
  unknown,        // reconcile + stale_writers
  occupied,       // reserved + running + reconcile + writers + stale_writers
  ready,          // max(0, effective - occupied); 0 while no controller of the agent is current
  reason,         // "available" | "at_capacity" | "identity_cap_unset" | "controller_unavailable"
  controllers,    // [{controller_ref, state: "current"|"paused"|"stale"|"unreported"}] behind the agent's bindings in this workspace; empty in direct mode
  tasks: { ready, waiting, waiting_by: { human_gate, triage, parked_wait, dependencies, start_after } }
}
```

Capacity fields are global to the agent identity across all workspaces, like the
reservation cap; `tasks` counts only this workspace and carries no task ids.
Rules:

- Occupancy comes only from reservations and held checkouts, never from display
  status, Todo/In Progress or `current_tasks`. A reservation or checkout counts
  regardless of the task's status, triage stage or `human_gate`.
- Unknown is occupied: a consumed reservation past its run lease and a checkout
  past its lease expiry stay in `occupied` (and in `unknown`) until released.
- Waiting is derived, not a flag: an open (triage/todo/in_progress) task assigned
  to the agent, without a checkout or active reservation, is `waiting` when it has
  an armed `human_gate`, sits in a `triage`-category status, has an unreleased
  registered parked wait, an open `blocks` dependency or a future `start_after`;
  otherwise it is `ready`. A waiting task stops consuming capacity only after its
  reservation is released and its checkout is released (quiesced).
- Backlog and review tasks are not schedulable and are not counted in `tasks`.

## QA for an exact artifact revision

QA policy supports `off`, `prefer`, `strict`; `quota_fallback` defaults to false.
Reviewer identity must differ from every author identity in every mode. Author
identities and actual model developer/family come from persisted trusted execution
attestations linked to the exact artifact SHA, not current Team preferences or
client `qa_allowed`, author-model, quota or exhausted flags. Another transport or
billing account does not establish model independence. Unknown/incomplete
provenance cannot satisfy strict. New artifact SHA needs a new decision.

In strict mode inspect every configured, authorized, compatible independent
alternative. Use one if available. Strict→prefer is permitted only when explicitly
enabled, at least one independent alternative exists and all such alternatives
are blocked exclusively by fresh confirmed provider quota. No independent
alternative, unsupported capabilities, stale/unknown/auth/network evidence,
revocation, STOP and API denial block this relaxation. The decision records exact
SHA, original/effective policy, excluded candidates, chosen actual model and quota
evidence. It is labeled a compromise, never strict acceptance. Recovery restores
strict for new reviews without restarting a running review. A requiring-strict
artifact still requires an independent recheck.

## Acceptance commands and rollout

Actual PostgreSQL/HTTP tests must prove two-grant isolation, owner/receiver RBAC,
revocation, concurrent desired/apply CAS, all-pool aliases, direct mode, disabled
drain and no secret fields. Routing tests cover shared preferred groups,
fallback/reset/no ping-pong, explicit API modes, unknown/error evidence and QA
exact-SHA quota-only fallback with positive and negative controls. Test additive
migration upgrade and downgrade. Run the applicable local and CI checks and obtain independent review before deployment.
Verify new launch behavior in an isolated canary against every participating controller;
deploying read-only inventory does not establish that controller launch/handoff is enabled.

REST responses (`GET`/`PUT /runtime`) return the catalog with every account `credential_ref` blanked: references stay with the controller and are write-only through the API.

## Provider name: `runner`

The runtime catalog is the builtin extension `runner`. Until the rename
migration of `integration_configs.provider` has run, rows carry the legacy name
`agent_runtime`; the API and the repository read both names and the legacy name
stays accepted as a synonym for one release. The generic integrations endpoints
still refuse to write or delete the runtime connection (`400`, "use the
versioned runtime API"); the Runner extension reports status through
`GET /workspaces/{ws}/extensions` without the resource inventory.
