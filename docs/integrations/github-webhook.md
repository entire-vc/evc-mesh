# GitHub Webhook Integration (inbound)

This document describes the **inbound** GitHub → Mesh webhook receiver: GitHub
posts repository events to Mesh, and Mesh links pull requests to tasks and
auto-transitions task status when a linked PR is merged.

> This is the opposite direction from [`webhooks.md`](./../webhooks.md), which
> documents **outbound** webhooks (Mesh → your service). If you are looking for
> "Mesh calls my endpoint when a task changes", read that file instead.

## Endpoint

Mesh exposes two equivalent, public (no-auth) routes for the same handler:

| Route | Notes |
|-------|-------|
| `POST /webhooks/github` | Legacy path; existing repos are configured against it. |
| `POST /api/v1/integrations/github/webhook` | Canonical alias surfaced in the integrations UI. |

Both accept the standard GitHub webhook payload and behave identically. Pick one
per repository.

## Security: HMAC signature

The receiver validates the GitHub `X-Hub-Signature-256` header (HMAC-SHA256 of
the raw body) **when a secret is configured** via the environment variable:

```
MESH_GITHUB_WEBHOOK_SECRET=<random-secret>
```

- Secret **set** → requests with a missing or invalid signature are rejected
  with `401`.
- Secret **empty/unset** → signature validation is skipped (backward
  compatible). **Production must set the secret** — without it the endpoint is
  unauthenticated and anyone can POST forged events.

The same secret value must be entered in the GitHub webhook configuration (see
[Setup](#setup)).

## Required headers

| Header | Required | Effect if missing |
|--------|----------|-------------------|
| `X-GitHub-Event` | yes | `400 Bad Request` |
| `X-GitHub-Delivery` | yes | `400 Bad Request` — used for idempotency |
| `X-Hub-Signature-256` | only when secret is set | `401 Unauthorized` if absent/invalid |

## Idempotency (dedup)

GitHub retries failed deliveries (including any 5xx) and guarantees
`X-GitHub-Delivery` is unique per delivery attempt. Mesh records each delivery
ID in Redis (`SET <key> NX EX`, key `mesh:webhook:gh:delivery:<delivery_id>`,
TTL 7 days) **before** any HMAC or JSON work:

- First time a delivery ID is seen → processed normally.
- Repeat delivery ID → short-circuits with `200 {"status":"duplicate"}` and no
  side effects (no double transition, no duplicate comment).
- If Redis is unavailable, the handler **fails open** (processes the event and
  logs the error) — better a possible duplicate than dropping all webhooks.

## Supported events

| `X-GitHub-Event` | Behaviour |
|------------------|-----------|
| `pull_request` | Upserts a VCS link and applies the [transition policy](#pull-request--task-transition-policy). |
| `push` | Links the task referenced in the head-commit message (existing commit-linking behaviour). |
| anything else | Ignored → `200 {"status":"ignored"}`. |

## PR → task linkage

The task a PR belongs to is resolved in this order:

1. A full `MESH-<task-uuid>` token in the **PR title**, then the **PR body**.
   Only a complete 36-char UUID is recognised here (e.g.
   `MESH-264f7eb6-1a2b-4c3d-9e8f-0a1b2c3d4e5f`).
2. **Fallback**: a previously-linked task found by
   `(provider=github, link_type=pr, external_id=<PR number>)`. If a PR was ever
   linked to a task before (via title/body on an earlier event, or a manual
   link), later events resolve it by PR number even without a `MESH-` token.
   When several historical links exist, the **newest** association wins.

If neither resolves a task, the event is acknowledged with
`200 {"status":"ok","reason":"no_task_ref"}` and nothing changes.

> **Tip:** short prefixes shown in the UI (e.g. `[#264f7eb6]`) are **not**
> parsed from PR text — only the full `MESH-<uuid>`. To link by short id, create
> the link explicitly via `POST /api/v1/tasks/:task_id/vcs-links` once; from then
> on the PR-number fallback keeps it linked.

## Pull request → task transition policy

On every `pull_request` event the VCS link row is upserted so the link status
stays in sync (`open` / `merged` / `closed`). **Status transitions only happen
on `action=closed`:**

| Situation | Action |
|-----------|--------|
| `closed`, not merged | Comment "closed without merge — no status change". No transition. |
| `closed` + `merged`, but the task has other PR links still non-terminal | Comment "Awaiting N more PR(s)…". No transition (multi-PR awareness). |
| `closed` + `merged`, all linked PRs terminal, task in **in_progress** | Move task → **review**. |
| `closed` + `merged`, all linked PRs terminal, task in **review** | Move task → **done**. |
| `closed` + `merged`, task in any other status | Comment "no auto-transition". No change. |

On a successful transition Mesh posts a system comment authored as **Garfield**
(`source: github-webhook`):

```
🤖 Auto: PR #123 merged (commit `a1b2c3d`) → moved to done.
```

### Response shape (pull_request)

```json
{
  "status": "ok",
  "task_id": "264f7eb6-...",
  "transitioned": true,
  "reason": "transitioned",
  "old_status": "review",
  "new_status": "done"
}
```

`reason` is one of: `transitioned`, `not_closed`, `closed_without_merge`,
`awaiting_other_prs`, `source_status_not_eligible`, `no_task_ref`,
`task_not_found`. The handler always returns `200` for well-formed, authentic
events (even when no transition occurs) so GitHub does not retry; genuine
processing failures are logged and returned as `200 {"status":"error_logged"}`
to avoid retry storms.

## Database

Migration `20260522055_vcs_links_extend.sql` adds a non-unique lookup index for
the PR-number fallback path:

```sql
CREATE INDEX IF NOT EXISTS idx_vcs_links_external_lookup
    ON vcs_links(provider, link_type, external_id);
```

## Setup

1. **Set the secret in prod** (operator):

   ```
   MESH_GITHUB_WEBHOOK_SECRET=<generate a strong random value>
   ```

   Restart the API so the value is picked up. Verify it is non-empty —
   otherwise HMAC validation is silently off.

2. **Apply the migration** `20260522055` (pre-apply per the deploy runbook,
   then ship the binary).

3. **Add the webhook in GitHub** — repo (or org) **Settings → Webhooks → Add
   webhook**:
   - **Payload URL**: `https://mesh.entire.host/webhooks/github`
   - **Content type**: `application/json`
   - **Secret**: the same value as `MESH_GITHUB_WEBHOOK_SECRET`
   - **Events**: select *Pull requests* and *Pushes* (or "Send me everything").

4. **Link a PR to a task** — include `MESH-<task-uuid>` in the PR title or body,
   e.g.:

   ```
   feat(webhook): auto move_task on PR merge — MESH-264f7eb6-1a2b-4c3d-9e8f-0a1b2c3d4e5f
   ```

   Merge the PR → the linked task auto-advances `in_progress → review → done`.

## Verifying

- **Invalid signature** (secret configured):

  ```bash
  curl -i -X POST https://mesh.entire.host/webhooks/github \
    -H 'X-GitHub-Event: pull_request' \
    -H 'X-GitHub-Delivery: 00000000-0000-0000-0000-000000000001' \
    -H 'X-Hub-Signature-256: sha256=deadbeef' \
    -H 'Content-Type: application/json' \
    -d '{}'
  # → 401
  ```

- **Replay** — sending the same `X-GitHub-Delivery` twice returns
  `200 {"status":"duplicate"}` on the second call with no side effects.

- **End-to-end** — open a PR in a configured repo with `MESH-<uuid>` in the
  title, merge it, then confirm the task moved (review→done) and a system
  comment with the merge SHA was posted.

## Related surfaces (avoid confusion)

Mesh has several distinct integration surfaces — keep them separate when
debugging:

| Surface | Direction | Where |
|---------|-----------|-------|
| **GitHub webhook (this doc)** | inbound: GitHub → Mesh | `POST /webhooks/github`, global secret `MESH_GITHUB_WEBHOOK_SECRET` |
| **Outbound webhooks** | outbound: Mesh → your service | per-workspace, see [`webhooks.md`](./../webhooks.md) |
| **Workspace integration toggles** | config flags | `integration_configs` (e.g. the "Connect GitHub" UI toggle) |
| **Team Relay** | outbound artifact push | per-project, separate integration |
