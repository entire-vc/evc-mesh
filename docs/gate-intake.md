# Gate intake (`POST /api/v1/gate-intake`)

An external approval system creates exactly one Mesh card per approval through
this endpoint. The caller is a narrow service principal: it can do this one
thing, and the destination of the card (workspace, project, assignee, labels,
priority) is bound to the principal on the server. The caller never names it.

The first and only schema is `contenthub-spark-gate-wake/v1`: ContentHub asks
for the Spark publication gate of an approved article.

## Authentication

`Authorization: Bearer mgi_<43 characters>`.

- A missing, malformed, unknown, revoked or expired token, and any other
  credential (user JWT, agent key) on this route, gets the same `401`
  (`unauthorized`). The answer does not say which.
- A valid `mgi_` token on any other method or route gets `403`
  (`forbidden`) before any other authentication runs, so the token
  reads, writes and administers nothing else.
- Only the SHA-256 of a token is stored. A token is shown once, in the file the
  operator command writes, and never in an answer, a log or the audit.

The guard is an Echo-level middleware (`mw.GateIntakeGuard`, registered with
`e.Use` in `cmd/api/routes_gate_intake.go`), so it runs after route lookup and
before every group's authentication.

## Request

`Content-Type: application/json` (else `415`), at most 16 KiB (else `413`),
exactly one JSON object with exactly these eleven fields:

| Field | Type | Rule |
|-------|------|------|
| `schema` | string | `contenthub-spark-gate-wake/v1` |
| `approval_id` | string | canonical lowercase UUID, not nil |
| `task_id` | integer | ≥ 1 |
| `draft_version` | integer | ≥ 1 |
| `payload_sha256` | string | 64 lowercase hex |
| `manifest_sha256` | string | 64 lowercase hex, differs from `payload_sha256` |
| `spark_project_id` | integer | `12` |
| `mr_iid` | integer | ≥ 1 |
| `head_sha` | string | 40 lowercase hex |
| `mr_url` | string | `https://…/-/merge_requests/<mr_iid>` |
| `article_url` | string | `https://spark.entire.vc/blog/<slug>` |

- A destination field (`workspace_id`, `project_id`, `assignee_id`,
  `assignee_type`, `labels`, `status`, `status_id`, `priority`) → `403`
  `intake_target_fixed`, nothing is written.
- Any other unknown, missing, duplicated or mistyped field → `400`
  `validation_failed` with the fields named.
- `Idempotency-Key` is optional; if sent it must equal `approval_id`, else
  `400` `idempotency_key_mismatch`. A header that is sent empty or more than
  once is a mismatch too.

## Idempotency

The approval is the key. One principal and one `approval_id` give one card,
forever:

- The card and its receipt (`gate_intake_receipts`, primary key
  `(principal_id, approval_id)`) are written in **one transaction**. Two
  concurrent requests for the same approval serialise on that key; the second
  creates nothing.
- The receipt stores the SHA-256 of the canonical request (the eleven fields,
  keys sorted, no whitespace, integers in decimal) and the first answer.
- A repeat with the same fingerprint → `200`, `Idempotent-Replayed: true`, the
  same `task_id`. This holds after a lost answer, after a crash between commit
  and answer, after a key rotation and after the card is deleted. There is no
  expiry: the route does **not** use the 24-hour answer store of
  [idempotency.md](idempotency.md).
- The same approval with a different fingerprint → `409`
  `approval_payload_conflict`, context `{approval_id, task_id, request_sha256,
  existing_request_sha256}`; nothing is written.
- The card also carries the external ref `contenthub-approval:<approval_id>`.

Events, notifications and the activity log follow only a `201`.

## Answers

`201` (created) and `200` (replay) carry:

```json
{
  "schema": "mesh-gate-intake/v1",
  "approval_id": "…",
  "task_id": "…",
  "project_id": "…",
  "request_sha256": "…",
  "replayed": false,
  "created_at": "2026-11-21T10:00:00.000000Z"
}
```

A retry policy: retry `5xx` and network errors with the same body; never retry
`400`, `401`, `403`, `409`, `413`, `415` without a change.

## Operator runbook

Principals are managed only on the Mesh host, with the binary and environment
of `mesh-api.service`. There is no API that issues, rotates or reads a token.
Every action writes a row to `gate_intake_audit` (operator, action, reason,
token prefix; never the token).

On the Mesh host (`/opt/evc-mesh/bin/mesh-api`, `EnvironmentFile=/opt/evc-mesh/.env.prod`):

```bash
MESH="systemd-run --wait --pipe -p EnvironmentFile=/opt/evc-mesh/.env.prod /opt/evc-mesh/bin/mesh-api gate-intake"

# issue: the token goes to a NEW file, mode 0600 (an existing file is refused)
$MESH issue --operator <operator-uuid> --name contenthub-spark-gates \
  --workspace <workspace-uuid> --project <project-uuid> --assignee-agent <agent-uuid> \
  --label kind:ops --label spark-gate --priority high \
  --schema contenthub-spark-gate-wake/v1 --token-file /root/contenthub-spark-gates.mgi

# show: binding, token prefix, receipt count, audit trail
$MESH show --name contenthub-spark-gates

# rotate: new token to a new file; the old one keeps working for --grace (default 1h, 0s = at once)
$MESH rotate --operator <operator-uuid> --name contenthub-spark-gates \
  --token-file /root/contenthub-spark-gates.2.mgi --grace 1h

# revoke: every token of the principal stops at once
$MESH revoke --operator <operator-uuid> --name contenthub-spark-gates --reason "<why>"
```

- `issue` refuses a live name (use `rotate`). `issue` of a revoked name re-arms
  the **same** principal id with a new token, so its receipts keep answering;
  the binding must be the one it had.
- `issue` checks the binding before writing: the project and the agent belong
  to the workspace, the project has a default status, the priority and schema
  are known.
- If the database refuses, the token file is removed: a file on disk is always
  a live token.
- Move the token file to the caller's secret store, then delete it. The name of
  a principal may be published; its token never.
- The subcommand needs a binary that has it: an older binary ignores the
  argument and starts a second server. Check `/version` first.
