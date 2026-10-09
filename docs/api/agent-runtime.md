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
| GET `/workspaces/:ws_id/runtime/bindings/:binding_id` | desired/actual profile, preferred account, availability and reasons | exact bound agent grant or authorized receiving admin |
| PUT `/workspaces/:ws_id/runtime/bindings/:binding_id/admission` | `{if_revision, permitted_profiles, enabled}` | receiving workspace admin; independent CAS; initially denied |
| POST `/workspaces/:ws_id/runtime/bindings/:binding_id/preview` | purpose, source profile, required capabilities, optional artifact reference | freshly authorized binding; server resolves observations and provenance |
| POST `/workspaces/:ws_id/runtime/artifacts/:artifact_id/provenance` | `controller_ref`, exact `artifact_revision`, `complete`, authors (agent id, model developer, model family); authors merge, never shrink | trusted reporting controller, bound execution scope and persisted attestation |

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
