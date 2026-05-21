# task.mentioned Event

## Overview

When an agent is `@`-mentioned in a comment, Mesh emits a `task.mentioned` SSE/Redis event to that agent. This enables wake-on-mention: an agent asleep or watching a different channel is woken up when another actor types `@their-slug` in a comment.

## Trigger

- `POST /api/v1/tasks/:id/comments` — every `@slug` in the new body fires a mention event.
- `PATCH /api/v1/comments/:id` — only slugs newly added relative to the previous body trigger events (diff logic avoids duplicate wakes on minor edits).

## Event shape (JSON)

```json
{
  "event_type": "task.mentioned",
  "timestamp": "2026-05-20T17:38:00Z",
  "workspace_id": "<uuid>",
  "agent_id": "<uuid of the mentioned agent>",
  "actor_id": "<uuid of the commenter>",
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
    "labels": [],
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

## Routing

Published to Redis pub/sub channel `agent-notify:<agent-uuid>` (same channel used by `task.commented`). SSE consumers and the mesh-dispatcher pick up from there.

## Edge cases

| Scenario | Behaviour |
|---|---|
| Unknown `@slug` | Silently skipped — `GetBySlug` returns nil, no event |
| Self-mention (agent comments and mentions itself) | Skipped — `actorID == agent.ID && actorType == "agent"` |
| Same slug appears twice in one comment | Single event (dedup by `agent.ID`) |
| Edit adds `@bob` but `@alice` was already there | Only `@bob` event fires (diff against `oldBody`) |
| Assigned agent is also mentioned | Both `task.commented` (to assignee) and `task.mentioned` (to mentioned agent) fire — independent paths |
| `agentSvc` or `agentNotifySvc` not wired | `notifyMentions` is a no-op; no panic |

## Slug resolution

Phase 1 (this PR): resolves via `agents.slug` (`AgentService.GetBySlug`). Human user `@`-mentions are out of scope until `users.username` column lands (tracked in task `05f4209a`).

Regex: `(?:^|[\s(\[{])@([a-z0-9][a-z0-9-]{0,98}[a-z0-9])\b`
- Matches 2–100 char slugs starting/ending with alnum, may contain hyphens.
- Anchored to word boundary; leading boundary prevents email addresses from matching.

## Implementation files

| File | Change |
|---|---|
| `internal/repository/interfaces.go` | `GetBySlug` added to `AgentRepository` |
| `internal/repository/postgres/agent_repo.go` | `GetBySlug` implementation |
| `internal/service/interfaces.go` | `GetBySlug` added to `AgentService` |
| `internal/service/agent_service.go` | `GetBySlug` passthrough |
| `internal/service/comment_service.go` | `mentionRegex`, `extractMentionSlugs`, `notifyMentions`, `WithCommentAgentService`, `buildTaskSnap` |
| `cmd/api/main.go` | `WithCommentAgentService(agentService)` wired |

## Phase 2 hook

When `users.username` column is available (task `05f4209a`), extend `GetBySlug` to also check the users table and emit `task.mentioned` with `actor_type=user` as the mentioned party.
