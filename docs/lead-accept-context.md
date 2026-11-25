# Lead-accept context (CH→Mesh `lead-accept-context/v1`)

A narrow, read-only projection that lets the ContentHub observer of Spark
decide a lead-accept: is lead `lead_gitlab_id` allowed to lead-accept in
`entire-vc/evc-spark` by the merge gate's current registry, do the named Mesh
evidence records exist unchanged in the ContentHub project, and does the
gate-intake receipt of the approval (if any) agree with the request.

The endpoint only reads. It never changes an approval, a card, a comment or an
artifact, never merges and never publishes. The one thing the server writes is
a registry observation posted by the publisher.

Code: `internal/domain/lead_accept.go`,
`internal/repository/postgres/lead_accept_repo.go`,
`internal/service/lead_accept_service.go`, `internal/middleware/lead_accept_auth.go`,
`internal/handler/lead_accept_handler.go`, `cmd/api/routes_lead_accept.go`,
`cmd/api/lead_accept_cli.go`. Migration `20261121300_lead_accept_context.sql`.

## Routes and credentials

| Route | Credential | Answers |
|---|---|---|
| `GET /api/v1/lead-accept-context` | reader `mlr_…` | 200 context or an LA6 refusal |
| `POST /api/v1/lead-accept-registry/attestations` | publisher `mlp_…` | 201 receipt or a refusal |

Tokens: prefix + 43 base64url characters (32 random bytes); the server keeps
only the SHA-256. Every answer on both routes carries `Cache-Control: no-store`.

`mw.LeadAcceptGuard` is Echo-level and registered before the gate-intake guard:

- no credential → 401 `unauthorized`;
- an `mlr_`/`mlp_` token that is unknown, revoked, malformed or past its
  rotation grace → 401 `unauthorized` (one answer for all), on any route;
- a valid `mlr_`/`mlp_` token on any route but its own → 403
  `lead_accept_scope_forbidden`; it never reaches DualAuth or a handler;
- any other credential on a lead-accept route (agent/lane key in
  `X-Agent-Key`, user or admin JWT, `mgi_`, `ext_`, any other bearer) → 403
  `lead_accept_scope_forbidden` on its presence alone. It is not validated, so
  the route is no oracle of other keys.

The principals live in their own tables (`lead_accept_principals`,
`lead_accept_audit`), not in `gate_intake_principals`: a reader carries a
request binding a gate-intake principal has no column for, and the
gate-intake principal, its key and its route stay untouched.

## Reader binding

A reader is bound at issue, for good, to one exact request (LA4):
`binding_sha256` = `request_sha256` of the request file, plus the Mesh
workspace and the ContentHub evidence project. Rotate keeps the binding; a new
request needs a new principal name. On every GET the request's hash is compared
first: a different value in any field, a different evidence set, or another
project is 403 `lead_accept_scope_forbidden`, before any registry or evidence
read and without naming the field.

`request_sha256` is the SHA-256 of the canonical JSON of the request: keys
sorted, no whitespace, no HTML escaping, integers as integers, `evidence`
sorted. It equals Python
`json.dumps(req, sort_keys=True, separators=(",", ":"), ensure_ascii=False)`.
Golden vector (Spark!1235):
`61a6ec8206a3beddf9f8fa2829beb7cbfc06109dabf0307110015481841c5205`.

## GET query

All parameters exactly once, nothing else (`evidence` 1–4 times, no repeats):

| Parameter | Format |
|---|---|
| `project` | `group/name` (only `entire-vc/evc-spark` is ever bound) |
| `mr_iid`, `lead_gitlab_id`, `review_note_id`, `lead_accept_note_id`, `approval_version` | positive integer, no leading zeros |
| `head_sha` | 40 lowercase hex |
| `approval_id` | lowercase UUID |
| `payload_sha256` | 64 lowercase hex |
| `evidence` | `comment:<task uuid>/<comment uuid>` or `artifact:<task uuid>/<artifact uuid>` |

`lead_accept_note_id` must be greater than `review_note_id`.

## GET refusals (LA6), in check order

| HTTP | error_code | when |
|---|---|---|
| 401 | `unauthorized` | no token, wrong, revoked, or past grace |
| 403 | `lead_accept_scope_forbidden` | the token is not a reader |
| 400 | `validation_failed` | a parameter missing, extra, repeated or malformed |
| 403 | `lead_accept_scope_forbidden` | `request_sha256 ≠ binding_sha256` |
| 403 | `evidence_scope_forbidden` | an evidence task is missing, deleted or outside the bound project/workspace |
| 503 | `registry_unavailable` | no observation accepted yet |
| 503 | `registry_source_error` | the current observation is `ok=false` |
| 503 | `registry_stale` | `now − observed_at > 150 s` |
| 503 | `registry_out_of_sync` | `in_sync = false` |
| 409 | `lead_accept_not_applicable` | the policy closes lead-accept for the project |
| 409 | `lead_not_authorized` | `lead_gitlab_id` is not a lead of the project |
| 409 | `evidence_revoked` | the record is gone or belongs to another task |
| 409 | `evidence_changed` | a comment was edited (`updated_at ≠ created_at`) |
| 409 | `evidence_unsupported` | an artifact is over 1 MiB (record or bytes) |
| 503 | `evidence_unavailable` | an artifact's bytes cannot be read |
| 409 | `approval_binding_conflict` | a gate-intake receipt exists but differs or cannot be verified |

The current observation is the one with the greatest `observed_at` of all
publishers. It is read before the evidence and read again when the answer is
built; if it changed, or its 150 s ran out meanwhile, the evaluation runs again
on the new state (at most three times, then 503 `registry_unavailable`). A lead
revoked by a new observation is therefore refused at once, never after the TTL.

Artifact bytes are read from object storage and hashed on every GET (at most
1 MiB + 1 byte is read).

`approval_binding.gate_intake_receipt` is `none` (no receipt for
`approval_id`) or `match`. The receipt stores only the gate-intake request
hash; its fields are recovered from the card the receipt created and accepted
only if they hash back to that request hash. A card whose fields no longer
prove the receipt (edited, soft deleted or gone) is 409 with `context.reason =
receipt_unverifiable`; fields that differ from the request (draft/approval
version, payload, MR, head) are 409 with `context.reason = fields_differ`.

## Attestation (publisher → Mesh)

`POST /api/v1/lead-accept-registry/attestations`, `Content-Type:
application/json`, body ≤ 16 KiB, no unknown fields.

ok=true:

```json
{
  "schema": "lead-accept-registry-attestation/v1",
  "observation_id": "<uuid, new per observation>",
  "observed_at": "<RFC 3339 UTC, taken before the reads>",
  "ok": true,
  "git_commit": "<40 hex>",
  "origin_main_commit": "<40 hex>",
  "registry_last_commit": "<40 hex>",
  "registry_blob_sha1": "<40 hex>",
  "origin_registry_blob_sha1": "<40 hex>",
  "registry_sha256": "<64 hex>",
  "mirror_policy_blob_sha1": "<40 hex>",
  "origin_mirror_policy_blob_sha1": "<40 hex>",
  "mirror_policy_sha256": "<64 hex>",
  "gate_code_sha256": "<64 hex>",
  "worktree_clean": true,
  "in_sync": true,
  "projects": {
    "entire-vc/evc-spark": {
      "lead_accept_allowed": true,
      "policy_reason": "",
      "leads": [{"username": "howard", "gitlab_id": 6, "lane": "Howard"}]
    }
  }
}
```

ok=false (only these fields):

```json
{
  "schema": "lead-accept-registry-attestation/v1",
  "observation_id": "<uuid>",
  "observed_at": "<RFC 3339 UTC>",
  "ok": false,
  "error_step": "fetch | read | blob | policy | visibility",
  "error": "<one line, 1–300 bytes>"
}
```

The server computes `in_sync = worktree_clean ∧ registry_blob_sha1 =
origin_registry_blob_sha1 ∧ mirror_policy_blob_sha1 =
origin_mirror_policy_blob_sha1`. The publisher's `in_sync` is required and must
equal that value; a contradiction is 400 `validation_failed` naming `in_sync`.

Under one transaction-scoped advisory lock, with `received_at` taken after the
lock, in this order:

| HTTP | error_code | when |
|---|---|---|
| 409 | `attestation_clock_skew` | `observed_at > received_at + 5 s` |
| 409 | `attestation_delayed` | `ok=true` and `received_at − observed_at > 30 s` (an `ok=false` is never late) |
| 409 | `attestation_replayed` | the `observation_id` was accepted before |
| 409 | `attestation_not_newer` | `observed_at ≤` the current observation's |

201 answers `{observation_id, received_at, in_sync, ok}`. Each accepted
observation is a new row; rows are kept 30 days (at least 7 by the contract)
and older ones pruned on intake.

## Operator command (host-local, `mesh-api lead-accept`)

Runs with the environment of `mesh-api.service`, instead of the server. The
token is written only to `--token-file`, created new with mode 0600 (an
existing file is refused); it is never printed. There is no public API that
issues one.

```
mesh-api lead-accept issue  --kind reader --operator <uuid> --name <name> \
                            --request-file <request.json> --token-file <new path>
mesh-api lead-accept issue  --kind publisher --operator <uuid> --name <name> --token-file <new path>
mesh-api lead-accept rotate --operator <uuid> --name <name> --token-file <new path> [--grace 1h]
mesh-api lead-accept revoke --operator <uuid> --name <name> --reason "<why>"
mesh-api lead-accept show   --operator <uuid> --name <name>
```

- `--name` matches `^[a-z0-9][a-z0-9-]{1,62}$`.
- A reader's `--request-file` is the LA4 request as JSON (the GET parameters,
  integers as numbers, `evidence` as an array). It is validated like the GET
  query, its `project` must be `entire-vc/evc-spark` and each evidence task a
  live task of the ContentHub project; otherwise nothing is created.
- `issue` prints `id`, `name`, `kind`, `binding_sha256`, `token_prefix`,
  `token_file`. Compare `binding_sha256` with the client's `request_sha256`.
- A revoked name is re-armed by `issue` only with the same kind and binding.
- `show` prints the principal, its audit and, for a publisher, its latest
  observation; never a token or a hash. Like every command it requires an
  operator UUID and commits its audit before printing any metadata.
- After a successful `issue` or `rotate` commit, an output error returns a
  nonzero exit status but preserves the token file. Inspect the principal
  with `show` before retrying; the saved token remains valid.

## Rollback

The feature is additive: three new tables, two new routes, a subcommand.
Rolling back = deploying the previous build; the tables can stay (nothing
reads them) or be dropped with `goose down` of `20261121300`.
