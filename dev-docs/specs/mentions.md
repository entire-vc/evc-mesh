# @mentions — Full Pipeline

Full specification for the `@`-mention system: regex extraction → slug resolution → `comment_mentions` persistence → `task.mentioned` SSE (agents) → `mention.badge` WS (users) → REST query API.

Implemented in PR #68. Frontend Activity tab (B2) in PR #72.

---

## 1. Slug extraction

**Regex** (in `internal/service/comment_service.go`):

```
(?:^|[\s(\[{])@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b
```

- Matches 2–40 char slugs: starts/ends with `[a-z0-9]`, middle may contain hyphens.
- Leading boundary (start-of-string or whitespace/bracket) prevents email addresses from matching.
- Word boundary `\b` prevents partial matches inside longer tokens.
- Returns unique slugs in order of first appearance (dedup by slug string).

---

## 2. Slug resolution

Resolution is **agent-first, then user**, both scoped to the comment's workspace.

```
slug → AgentService.GetBySlug(ctx, workspaceID, slug)
     ├─ found → agent mention
     └─ nil   → UserRepository.GetByUsername(ctx, workspaceID, slug)
                ├─ found → user mention
                └─ nil   → silently skipped
```

`users.username` added in migration 046 (backfill from email prefix, NOT NULL, `^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`, `UNIQUE(lower(username))` per workspace).

---

## 3. Trigger points

| Event | Where | Behaviour |
|---|---|---|
| Comment created | `POST /api/v1/tasks/:id/comments` | All `@slugs` in body |
| Comment edited | `PATCH /api/v1/comments/:id` | Only slugs **new** relative to previous body (diff) |

**Edit diff logic**: old body slugs are extracted into a set; only slugs absent from that set fire mention events and are persisted. Re-saves of unchanged body produce zero new mentions.

---

## 4. Edge cases

| Scenario | Behaviour |
|---|---|
| Unknown slug (no agent, no user) | Silently skipped |
| Self-mention by agent | Skipped — `actorID == agent.ID && actorType == "agent"` |
| Self-mention by user | Skipped — `actorID == user.ID && actorType == "user"` |
| Same slug twice in one comment | Single mention (dedup by resolved `mentioned_id`) |
| Edit keeps `@alice`, adds `@bob` | Only `@bob` fires; `@alice` already in DB (`ON CONFLICT DO NOTHING`) |
| Assignee is also mentioned | Both `task.commented` (assignee) and `task.mentioned` (mentioned) fire independently |
| Optional deps not wired | `notifyMentions` is a no-op; no panic |

---

## 5. Persistence — `comment_mentions` table

Schema (migration 047):

```sql
CREATE TABLE comment_mentions (
    comment_id     UUID        NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    mentioned_id   UUID        NOT NULL,
    mentioned_kind TEXT        NOT NULL CHECK (mentioned_kind IN ('agent', 'user')),
    mentioned_slug TEXT        NOT NULL,
    extracted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seen_at        TIMESTAMPTZ NULL,
    PRIMARY KEY (comment_id, mentioned_id)
);
CREATE INDEX ix_comment_mentions_mentioned ON comment_mentions (mentioned_id, seen_at);
CREATE INDEX ix_comment_mentions_extracted ON comment_mentions (extracted_at DESC);
```

Insert uses `ON CONFLICT (comment_id, mentioned_id) DO NOTHING` — idempotent on re-process.

---

## 6. Agent SSE — `task.mentioned`

Published to Redis pub/sub channel `agent-notify:<agent-uuid>` after each agent mention row is persisted. Mesh-dispatcher and SSE consumers subscribe per agent.

**Event shape:**

```json
{
  "event_type": "task.mentioned",
  "timestamp": "2026-05-21T00:00:00Z",
  "workspace_id": "<uuid>",
  "agent_id": "<uuid of mentioned agent>",
  "actor_id": "<uuid of commenter>",
  "actor_type": "agent | user",
  "actor_name": "Garfield",
  "task": {
    "id": "<uuid>",
    "project_id": "<uuid>",
    "title": "Task title",
    "priority": "high",
    "description": "…up to 500 chars…",
    "assignee_id": "<uuid or null>",
    "assignee_type": "agent | user",
    "labels": ["mesh", "backend"],
    "status": {"id": "<uuid>", "name": "In Progress", "category": "in_progress"}
  },
  "comment": {
    "id": "<uuid>",
    "body": "…up to 500 chars…",
    "author_id": "<uuid>"
  },
  "task_id": "<uuid>",
  "project_id": "<uuid>",
  "payload": {
    "mentioned_slug": "garfield"
  }
}
```

---

## 7. User WS badge — `mention.badge`

Published to Redis channel `ws:user:<user-uuid>` after each user mention row is persisted. Frontend WebSocket hub pushes this to connected browser sessions.

**Event shape:**

```json
{
  "event": "mention.badge",
  "workspace_id": "<uuid>",
  "task_id": "<uuid>",
  "comment_id": "<uuid>"
}
```

Frontend uses this to increment the unseen-mention badge without a full page reload. `GET /me/mentions/unseen_count` can be polled as fallback.

---

## 8. REST API

All endpoints require JWT authentication. `me` is derived from the JWT actor (`agent` or `user`); mentions are filtered by `mentioned_id = me.id`.

### `GET /api/v1/me/mentions`

Returns paginated mention records for the caller.

**Query params:**

| Param | Type | Default | Description |
|---|---|---|---|
| `seen` | bool | — | Filter by seen status (`true`/`false`) |
| `since` | RFC3339 | — | Return mentions extracted after this timestamp |
| `project_id` | UUID | — | Scope to a single project |
| `limit` | int | 50 | Max results (1–100) |

**Response** `200 OK` — array of `CommentMentionView`:

```json
[
  {
    "comment_id": "<uuid>",
    "mentioned_id": "<uuid>",
    "mentioned_kind": "agent | user",
    "mentioned_slug": "garfield",
    "extracted_at": "2026-05-21T00:00:00Z",
    "seen_at": null,
    "task_id": "<uuid>",
    "task_title": "Task title",
    "project_id": "<uuid>",
    "comment_body": "…",
    "author_id": "<uuid>",
    "author_name": "Pavel"
  }
]
```

### `POST /api/v1/me/mentions/:comment_id/seen`

Marks a mention as seen. Idempotent — repeated calls return 204.

**Response** `204 No Content`

### `GET /api/v1/me/mentions/unseen_count`

Returns the count of unseen mentions. Cached for 10 s (`Cache-Control: max-age=10`).

**Response** `200 OK`:

```json
{"count": 3}
```

---

## 9. Implementation map

| Layer | File |
|---|---|
| Regex + extraction | `internal/service/comment_service.go` — `mentionRegex`, `extractMentionSlugs` |
| Resolver + notify | `internal/service/comment_service.go` — `notifyMentions` |
| Agent lookup | `internal/repository/postgres/agent_repo.go` — `GetBySlug` |
| User lookup | `internal/repository/postgres/user_repo.go` — `GetByUsername` |
| Persistence | `internal/repository/postgres/comment_mention_repo.go` |
| WS publisher | `internal/service/comment_service.go` — `WSPublisher` / `RedisWSPublisher` |
| Mention service | `internal/service/mention_service.go` |
| REST handler | `internal/handler/mention_handler.go` |
| Migrations | `migrations/20260520046_add_users_username.sql`, `migrations/20260520047_create_comment_mentions.sql` |

---

## 10. Out of scope

- ❌ Markdown render `@username` as hyperlink (Phase 3)
- ❌ `@<typing>` autocomplete in comment editor (Phase 3)
- ❌ Web push / Telegram alerts (Phase 2)
- ❌ Profile UI to rename `username` (separate task `06f9f2dc`)
- ❌ Dispatcher-side spawn logic (Riker — activates automatically from `task.mentioned` event)
