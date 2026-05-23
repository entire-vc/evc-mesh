# Comment Triage Enforcement (`❓ Blocking @user` → triage)

Server-side, defense-in-depth enforcement of the workflow rule in
`CLAUDE-workflow.md §0b`: when an agent leaves a **blocking question** for a human
in a task comment, the task is automatically moved into the project's **triage**
column so the blocker surfaces in the Triage Inbox instead of sitting silently in
`in_progress`.

This complements the procedural rule (agents are *supposed* to call
`move_task → triage` themselves in the same tool sequence). Agents forget or err;
the server catches the violation.

## Markers

Agents annotate comments with one of two explicit markers:

| Marker | Meaning | Server behavior |
|--------|---------|-----------------|
| `❓ **Blocking @<user>**: <ask>` | Blocking question — work is stuck pending a human decision/input. | Task auto-moves to **triage** (see gates below) + a system comment is appended. |
| `ℹ️ **FYI @<user>**: <inform>` | Non-blocking informational update. | **No** status change. The mention is still recorded in the mentions feed as usual. |

### Marker grammar

The blocking marker is matched **anchored to the start of a line**, case-insensitively:

```
(?im)^\s*(?:❓\s*)?\*{0,2}\s*Blocking\s+@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b
```

- The `❓` emoji and the markdown bold (`**`) are both **optional** — `Blocking @pavel`,
  `**Blocking @pavel**`, and `❓ **Blocking @pavel**` all match.
- The slug subpattern is identical to the `@mention` slug constraint
  (`agents.slug` / `users.username`): starts and ends with alphanumerics, may
  contain hyphens in the middle, 2–40 chars, **no underscore**.
- `ℹ️ **FYI @user**` never matches (no `Blocking` keyword) → it is a pure no-op.
- A **quoted** line (`> ❓ **Blocking @pavel**`) does **not** match — the leading
  `>` is neither whitespace nor one of `❓`/`*`/`Blocking`, so the line anchor fails.
  This avoids false-positives when an agent quotes someone else's earlier question.

## Behavior and gates

When a comment is **created** or **edited**, the server evaluates the body. The
auto-move only happens when **all** of the following hold, checked in order:

1. **Marker present** — the body contains a blocking marker (above). Otherwise no-op.
2. **Human gate** — at least one `@`-mentioned slug *anywhere in the body* resolves
   to a **user** (a human), not just agents. Agents are notified over SSE and do not
   need a triage move, so `❓ **Blocking @someAgent**` alone is a no-op (the mention
   is still recorded). `❓ **Blocking @pavel @someAgent**` *does* trigger, because a
   human is present.
3. **Idempotency** — the task is **not** already in a `triage`, `done`, or
   `cancelled` category. This makes re-comments and double-fires from edits safe.
4. **Triage column exists** — the project has a status whose category is `triage`.
   Projects without a triage column are a graceful no-op.

If all gates pass, the task is moved to the project's first `triage`-category status
via the task service (so the move emits the normal activity-log entry, SSE event,
agent notification, and any configured auto-transition cascade), and a **system
comment** is appended:

```
🤖 Auto: задача переведена в triage из-за «❓ Blocking @<user>» в комментарии выше (per CLAUDE-workflow.md §0b)
```

The `<user>` named is the first mentioned human slug.

### Edit semantics

On comment **edit**, enforcement re-runs only when the marker was **newly added**
(absent in the previous body, present in the new one). Editing a comment that
already carried the marker does not re-fire. The idempotency gate (#3) is a second
safeguard against double-triage.

## System comment author

The auto-generated comment is written with `author_type = system` and
`author_id = uuid.Nil` (`00000000-0000-0000-0000-000000000000`). This is safe:
`comments.author_id` is `NOT NULL` but carries **no foreign key**, and the
`author_name` SELECT resolves `system` comments to `NULL` — the UI renders them as
**"System"** regardless of the id. The system comment is written directly through
the comment repository (not the normal create path), so it never re-enters the
marker parser. Its body also does not begin with a blocking marker, so it could not
re-trigger enforcement even if it did.

## Where it lives

- Parser + enforcement: `internal/service/comment_service.go`
  (`blockingMarkerRegex`, `hasBlockingMarker`, `enforceBlockingTriage`,
  `firstMentionedUserSlug`).
- Hooked into `commentService.Create()` and `commentService.Update()` after the
  existing `notifyMentions` step. Both the REST handler and the MCP `add_comment`
  tool flow through `commentService`, so both are covered.
- Triage-status lookup reuses the shared `findStatusIDByCategory` helper in
  `internal/service/auto_transition.go`.
- Wiring: `WithCommentTaskService(taskService)` in `cmd/api/main.go`. If the task
  service is not wired, the enforcement step is skipped entirely.

All steps are **best-effort**: any failure (move error, system-comment write error)
is logged under `[comment-triage]` and never blocks the comment mutation that
triggered it.

## Not included (deferred)

Dedicated push/Telegram alerting on auto-triage is **out of scope** for this
enforcement layer (tracked as a separate follow-up). The auto-move itself is a
structural signal surfaced in the Triage Inbox, and `comment.created` already
dispatches in-app and (where subscribed) Web Push notifications.
