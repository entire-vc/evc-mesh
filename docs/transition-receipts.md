# Transition receipts: opt-in product done

Foundation unit for server-side commit authority over "product done". It adds a
new path next to `POST /tasks/:id/move`; it does not change `move`, MCP, the
board, Runner or the UI. A project that has not opted in behaves exactly as
before.

## What is and is not claimed

- Claimed: a receipt-bound done commit exists and is checked inside one transaction.
- Not claimed: that every path to done goes through it. `move`, auto-transition,
  VCS webhooks and the lease reaper are unchanged, so "one writer" does not hold
  yet. Enforcing the competing paths is a later unit.
- `source_ref` / `source_hash` are stored with `source_trust = "claimed"`: they
  come from the request and are not confirmed by a registry or deploy receipt.

## Schema (migration `20261101100_transition_receipts.sql`, additive)

- `transition_receipt_policies(project_id, enabled, policy_version, allowed_producers, allowed_acceptors, allowed_exemptions)` — absent row or `enabled=false` is off.
- `task_transition_receipts` — append-only (trigger rejects UPDATE/DELETE). No foreign key to `tasks` or `vcs_links`: the evidence is a snapshot, so a later edit of VCS metadata cannot rewrite a historical receipt.
- Rollback: `goose down` drops both tables and the trigger; or set `enabled=false` per project, which stops the path without touching data.

## HTTP

`PUT|GET /projects/:proj_id/transition-receipt-policy` (manage_project) — body
`{enabled, allowed_producers:[{producer_id, actor_id}], allowed_acceptors:[uuid], allowed_exemptions:["research"|"trivial"]}`. Every PUT advances `policy_version`.

`POST /tasks/:task_id/transitions/product-done` — required: `domain=product`,
`operation=product_done`, `class` (code|config|visible|research|trivial),
`to_status_id` (a done-category status of the project), `expected_status_id`,
`expected_version`, `idempotency_key`, `producer_id`, `accepting_actor_id`,
`reason`, `evidence`. Optional: `producer_session`, `source_ref`, `source_hash`, `acceptance_ref`.

- code/config/visible: `evidence = {vcs_link_id, head_sha}`. The server reads the link of this task: it must be a merged pr link whose recorded `head_sha` equals the request's and whose `ship_sha` equals that head. `ship_sha` is writable by any task updater (metadata PATCH), so the snapshot stores it as `ship_trust: claimed`: a consistency check against the head, not proof of an independent verdict. The independence guarantee in this unit comes from the policy: only an allow-listed producer bound to the credential actor can commit, and the accepting identity must be allow-listed and differ from that actor. Binding an authenticated verdict to the head is the gate-receipt unit.
- research/trivial: `evidence = {exemption_type == class, artifact_ref}` and the project policy must list the type. A label or actor type is never an exemption.
- 200 `{receipt, replayed}`; 409 `cas_conflict` (stale version/status) or `idempotency_key_payload_mismatch`; 422 with a stable `code` (`transition_policy_disabled`, `producer_not_authorized`, `acceptor_not_authorized`, `acceptor_not_independent`, `evidence_*`, `human_gate_active`, `parked_wait_active`, `dod_gate_blocked`, `target_not_done_status`, `exemption_not_allowed`).

`GET /tasks/:task_id/transition-receipts` lists receipts.

## Rules

- No blanket system bypass: the credential actor (from auth, whatever its type) must be bound to `producer_id` in the policy.
- Idempotency scope: task + operation + producer + key. Same key and canonical payload returns the stored receipt even after the version moved; same key with another payload is a conflict. Replay is checked before the CAS.
- State update, receipt, activity row and outbox/event rows commit together under the task row lock; any failure rolls all back.
- The accepting identity must be allow-listed and differ from the credential actor. The server records it; it does not authenticate that identity itself.

## Enforcement (`enforce_done`, migration `20261101300_product_done_enforcement.sql`)

`enforce_done` is a second, default-off flag on the policy (PUT body `enforce_done`, requires `enabled`). A policy
that was `enabled` before this migration stays unguarded until `enforce_done` is set explicitly. Rollback: set
`enforce_done=false` for the project (receipts and intents stay), or `goose down` one step plus the prior image.

### Barrier

A deferred constraint trigger `tasks_product_done_guard` on `tasks` (INSERT, or UPDATE of `status_id`/`project_id`) runs
at commit. In a project with `enforce_done`, a task whose row now sits in a done-category status after an entry into
done must have, written by the same transaction (`commit_txid = txid_current()`), a receipt for exactly this task,
status and version, bound to a non-revoked acceptance intent of this task whose acceptor equals the receipt's
accepting actor. It reads the policy `FOR SHARE`, so a concurrent policy update serialises with the commit check.
`task_statuses_product_done_category_guard` refuses re-categorising a populated status as done in an enforced project.
Because it sits on the table, it covers every writer, raw SQL under the application role included.

Limit: the receipt and intent tables are writable by the same database role, so a process with raw INSERT access can
still write a well-formed pair. The guard removes accidental and application-code bypasses, not a hostile superuser
or migration admin (outside the product credential threat model). Source ref/hash stay `claimed`.

### Acceptance intent

`POST /tasks/:id/acceptance-intents` (acceptor = credential actor, never a body field; must be in `allowed_acceptors`,
must not be a producer/executor, the assignee or the checkout holder). Body mirrors the product-done scope plus
`deploy_ref`, `live_ref`, `reason`. The server recomputes the evidence snapshot from stored state and pins its hash;
the intent binds task, class, operation, from status/version and target. `POST .../:intent_id/revoke` (acceptor only)
appends a revocation. `GET` lists. A lead role is a producer entry with `role=lead`: that identity may accept
another identity's result and execute the typed commit itself (`acceptor_role=lead` / `acting_role=lead`); no other
identity gets this. The assignee or checkout holder of the result is refused as acceptor whatever the role, both when
the intent is recorded and when it is consumed.
Under `enforce_done` product-done requires `acceptance_intent_id`; one intent backs one receipt.
Refusals: `acceptance_intent_required|not_found|wrong_task|revoked|used|scope_mismatch|stale`,
`acceptance_evidence_mismatch`, `evidence_artifact_hash_required`, `acceptor_not_authorized|not_independent`.

### Route map (all reach the guard; nothing is exempt)

| Route | Source | Outcome in an enforced project |
|---|---|---|
| REST/MCP move, bulk | `TaskService.MoveTask` -> `TaskRepo.UpdateTransition` (`task_transition.go:72`) | denied 422 `product_done_receipt_required` |
| server auto_transition, VCS merged/review-subject auto-done, lease reaper | same `UpdateTransition` | denied; logged by the caller as a failed move |
| finish_task, typed outcome | `typed_outcome_command.go:203` | denied for a done target |
| parked wait release | `parked_wait.go:440` | writes `completed_at=NULL`; to a done status denied |
| generic update | `TaskRepo.Update` (`task_repo.go:765`) | denied |
| project remap | `TaskRepo.MoveToProject` (`task_repo.go:1619`), `task_move_batch.go:307` | denied when the target is done in an enforced project |
| create / import / recurring / template into done | `task_repo.go:514`, `recurring_repo.go`, `task_template_repo.go` | denied (INSERT fires the guard) |
| status category -> done | `task_status_repo.go:64` | denied while the status holds tasks |
| PATCH `status` | `task_handler.go` | already 422 `status_change_via_patch_rejected` |
| typed product-done | `TransitionReceiptRepo.CommitProductDone` | the only way in |

Tests: `internal/repository/postgres/product_done_enforcement_db_test.go` (integration tag, run by the MR pipeline;
`TestEnforce_*`). Route controls include a policy-off positive control and a control with the trigger disabled that
must succeed, proving the denials come from the guard.

## Wire contract `board-product-done.authenticated.v1` (migration `20261102100_product_done_wire_v1.sql`, additive)

What the fleet Board consumer pins. Nothing above changes: the foundation digest, the stored evidence
snapshot, and the legacy request (caller-supplied `accepting_actor_id`) keep working.

- `PUT .../transition-receipt-policy` takes an optional `scope_id` (omitted keeps the stored value).
- `GET /projects/:id/product-done-capabilities`: flat record for the calling credential: `wire_contract`,
  `scope_id`, `project_id`, `producer_id` (the producer the policy binds to that actor), `credential_actor_id`,
  `credential_client_id` (always null: no client UUID exists), `authenticated_acceptance`, `immutable_receipts`,
  `enabled`, `enforce_done`, `enforcement_ready` (= enabled && enforce_done && bound producer && non-empty
  scope), `policy_version`, `payload_digest_algorithm`. An unbound credential gets 200 with
  `enforcement_ready=false`.
- `GET /tasks/:id/product-done-acceptance`: `{"acceptance": {...}}`, only for a credential bound to a producer.
  The newest intent for the task's current status/version that is neither revoked nor consumed. The accepting
  actor is read from the stored intent. `evidence` is request-shaped (`vcs_link_id`+`head_sha`, or
  `exemption_type`+`artifact_ref`[+`artifact_hash`]); `evidence_hash` and `snapshot_hash` are canonical-JSON
  SHA-256 (the snapshot hash covers every other field). Needs an assignee on the task: `result_author_id` is how
  independence is judged, and a null author is not an acceptance the consumer will take.
- `POST .../transitions/product-done` accepts `acceptance_ref` (a UUID naming the intent; alias
  `acceptance_intent_id`) with no `accepting_actor_id`. Evidence must equal what the intent pinned. A UUID
  `acceptance_ref` that differs from `acceptance_intent_id` is refused (`acceptance_ref_mismatch`); a free-form
  legacy `acceptance_ref` is only a note.
- Receipts that consumed an intent also carry `scope_id`, `acceptance_hash` (= the acceptance `snapshot_hash`),
  `payload_digest_v1` with `payload_digest_algorithm=canonical-json-without-idempotency-key-v1` (canonical JSON
  of the body as received, key excluded), and `evidence`/`evidence_hash` in the request shape; the stored
  snapshot moves to `evidence_snapshot`/`evidence_snapshot_hash`. Receipts without an intent keep the legacy
  `evidence` shape. Replay compares both digests.
- Canonical JSON = UTF-8, keys sorted, compact, no HTML escaping: byte-identical to Python
  `json.dumps(sort_keys=True, separators=(",", ":"), ensure_ascii=False)`. Golden vector:
  `TestCanonicalJSON_MatchesConsumerEncoder`.

Proof: `TestTransitionReceiptE2E_WireContractRoundTrip` (real Postgres, real handlers) and the unchanged
consumer `fleet_product_done.close()` run against the same handlers returned `done`.
