# Host-local QA assignment v1 (backend)

Capability `host_local_qa_assignment_v1`, schema_version 1. A separate surface from the
legacy `readonly_metadata` existing-session routes: own routes, own tables
(`host_local_qa_receivers`, `host_local_qa_deliveries`), own admission and revision.
The server never executes the delivered text; there is no command, argv, cwd, tool or URL field.

## Switch

`HOST_LOCAL_QA_ASSIGNMENT_ENABLED` — only the exact value `true` turns the capability on.
Unset or anything else = OFF: every route, revoke included, answers an authenticated caller
`403 host_local_qa_disabled` before reading the body or touching state. While OFF the guard
check also refuses, so a receiver fails closed; admissions lapse by their own expiry (8h at most).

Disable (no schema or image change, producer, ledger and legacy untouched):
remove the variable or set it to anything but `true` and restart the API.

## Fixed policy

`internal/hostlocalqa/policy.json` and `brief.txt` are embedded; their SHA-256 are pinned in
`policy.go` (policy `8bfef84c…652ad`, text `762203ba…c47d8c`). A request cannot introduce a
policy, a target workspace/agent/hostname, a sender or new text. Changing either file requires a
new pin and a reviewed diff.

## Routes (under /api/v1)

| Route | Caller |
|---|---|
| `POST /runner/host-local-qa/v1/receivers` | agent key + that agent's runner bearer |
| `DELETE /runner/host-local-qa/v1/receivers/:receiver_id` | same |
| `GET /runner/host-local-qa/v1/deliveries?host_id=&session_id=` | same |
| `POST /runner/host-local-qa/v1/deliveries/:delivery_id/ack` | same |
| `POST /runner/host-local-qa/v1/deliveries/:delivery_id/check` | same (authoritative guard) |
| `POST /host-local-qa/v1/deliveries` | sender agent key (fixed allowlist) |
| `GET /host-local-qa/v1/deliveries/:delivery_id` | the sending agent |

Chat-session tokens and OAuth connectors are refused. Workspace, agent and runner come from the
credentials; the body must agree with them and the policy (403 `host_local_qa_target_denied`,
the same answer for every identity field) and never selects them.

## Bodies

All bodies are strict: exactly the listed keys at every depth, duplicate keys and trailing data
refused, UUIDs canonical lowercase, hashes lowercase 64-hex, integers real JSON integers
(bool, string, float are refused), revisions positive.

* register: `{schema_version, capability, policy_sha256, target, producer, expires, allowed_sender_agent_ids}`.
  `expires` is an absolute epoch, in the future and at most 8h ahead by the server clock. Every
  registration of the same identity bumps `admission_revision` and fails queued work admitted for an
  older revision (`admission_stale`).
* send: exactly `schema.json` (frozen). `request_id` is immutable per sender across capabilities: an identical
  replay returns the same delivery (200), any change is `409 request_conflict`, and a request id already used on
  the legacy route is a conflict too (and vice versa).
* check: `{receiver_id, admission_revision, delivery_id, request_id, capability, policy_sha256, text_sha256, target, producer, expires}`;
  no text, no token. 200 returns `{active:true, …same fields…, checked_at}`; anything else is `403 host_local_qa_check_denied`.
  Check writes nothing.
* ack: `{schema_version, capability, receiver_id, admission_revision, delivery_id, request_id, policy_sha256, text_sha256, target, producer, expires, state, observed_at}`
  plus optional `result_comment_url` (https) and `error_code` (required for `failed` and `uncertain`, forbidden otherwise).
  `observed_at` is RFC 3339 UTC (`…Z`). States: queued→received|failed|uncertain, received→acknowledged|failed|uncertain;
  queued→acknowledged is `409 delivery_state_conflict`. `received` needs an unexpired delivery.
* receipt (sender GET, ack response): `delivery_id, request_id, schema_version, capability, target, producer, receiver_id,
  admission_revision, policy_sha256, text_sha256, expires, state, received_at, acknowledged_at, result_comment_url, error_code`.
  No text, transcript or credential. `acknowledged` is a producer ACK, not an install.

Revoke (`204`) invalidates the admission: queued work fails `receiver_revoked`, a received delivery becomes
`uncertain` (`delivery_uncertain`), check/list/send are refused. It touches no legacy admission, producer, ledger or file.

## Deploy and rollback

Migration `20261103100_host_local_qa_assignment.sql` is additive (two new tables, nothing references
them); its Down drops both. Production rollback that keeps the producer, ledger and legacy path:
1. set `HOST_LOCAL_QA_ASSIGNMENT_ENABLED` off (default) and restart — QA routes refuse;
2. re-deploy the previous image — the tables stay unused and harmless;
3. only if the schema must go: `goose down` one step (drops only the two QA tables).
