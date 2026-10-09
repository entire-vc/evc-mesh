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
receiving approval; disabling without changing content retains approval while
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
| PUT `/workspaces/:ws_id/runtime` | `{if_revision, enabled, config}` → new desired revision/digest | resource owner admin; atomic CAS; stale writer gets 409 |
| POST `/workspaces/:ws_id/runtime/controllers/:controller_ref/report` | versioned apply/heartbeat/capabilities/pool observations | exact configured reporter grant in this workspace; no generic user/client claims |
| GET `/workspaces/:ws_id/runtime/controllers/:controller_ref/desired` | `{revision, digest, enabled, drain_requested, controller_ref, controller, catalog:{accounts, pools, profiles, bindings}}`: only this controller's profiles, their accounts (with `credential_ref`, a reference, never a secret value), the pools those accounts use and bindings permitting one of its profiles (permitted profiles, primary profiles, quota edges and preferred accounts narrowed to this controller). `ETag` is a hash of the whole response body (revision, enabled and slice); a matching `If-None-Match` gives 304 | exact configured reporter grant of this controller; another key 403, unknown ref 404; read-only |
| GET `/workspaces/:ws_id/runtime/bindings/:binding_id` | desired/actual profile, preferred account, availability and reasons | exact bound agent grant or authorized receiving admin |
| PUT `/workspaces/:ws_id/runtime/bindings/:binding_id/admission` | `{if_revision, permitted_profiles, enabled}` | receiving workspace admin; independent CAS; initially denied |
| POST `/workspaces/:ws_id/runtime/bindings/:binding_id/preview` | purpose, source profile, required capabilities, optional artifact reference | freshly authorized binding; server resolves observations and provenance |
| POST `/workspaces/:ws_id/runtime/bindings/:binding_id/reservations` (+ `/:reservation_id`, `/consume`, `/release`) | atomic execution admission, see "Execution admission" | exact bound agent grant |
| GET `/workspaces/:ws_id/runtime/capacity` (`?agent_id=` optional) | derived execution-state projection per agent identity, see "Capacity projection" | human member of `:ws_id` or an agent key presented for `:ws_id`; no OAuth connectors |
| POST `/workspaces/:ws_id/runtime/artifacts/:provenance_artifact_id/provenance` | `controller_ref`, exact `artifact_revision`, `complete`, authors (agent id, model developer, model family); authors merge, never shrink | trusted reporting controller, bound execution scope and persisted attestation |

Schema v2 is independent of monotonically increasing desired revision. Every save,
including rollback, creates a new immutable revision. `if_revision=0` creates only
when absent. Controller apply reports contain the exact revision and digest:
future, mismatched and stale reports cannot mark the current revision applied.
Heartbeat is separate from apply success. Controller rejection/capability mismatch
and stale heartbeat remain visible. Local emergency pause survives synchronization.
Disabling the integration denies new admissions and requests drain of existing
executions; it never falls back to environment. Absence of a connection preserves
direct mode and existing agent APIs, with unknown runtime facts.

## Routing, preferred accounts and shared pools

The generated policy projection matches bob's `runtime-routing-policy-v2`:
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
`release_unproven`; 410 `reservation_expired` / `reservation_released` (consume
too late); 423 `runtime_disabled`, `identity_cap_unset`. Disabling the
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
  ready,          // max(0, effective - occupied)
  reason,         // "available" | "at_capacity" | "identity_cap_unset"
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
migration upgrade and downgrade. Full local gate and MR CI precede independent
review and Garfield acceptance. New launch behavior remains on an isolated canary
until the cross-controller R4 acceptance; deploying read-only inventory is not
evidence that controller launch/handoff is enabled.

REST responses (`GET`/`PUT /runtime`) return the catalog with every account `credential_ref` blanked: references stay with the controller and are write-only through the API.
