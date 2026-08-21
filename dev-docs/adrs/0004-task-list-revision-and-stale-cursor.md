---
created: 2026-08-21T13:10+03:00
updated: 2026-08-21T13:10+03:00
author: Linus
status: proposed
project: mesh
type: adr
tags:
  - mesh
  - api
  - pagination
  - list-tasks
  - consistency
---

# ADR-0004 — `task_list_revision` counter and stale-cursor rejection for `list_tasks`

**Status:** Proposed · awaiting review before subtasks #5 (migration+triggers) and #6 (cursor validation) start
**Task:** [ebcf18e8](https://mesh.entire.host/t/ebcf18e8-f39c-49a6-b2b6-c47cfaa09f7a) — part 1/7 of [ad22bfda](https://mesh.entire.host/t/ad22bfda-beda-4408-97bf-161dab29ff7a)
**Depends on:** nothing shipped. Consumed by #5 (schema) and #6 (validation).

---

## Context

`ad22bfda` (Riker, 2026-08-20) measured three ways the Mesh API answers
*plausibly* where it can't answer *correctly*. This ADR covers one of them,
§3.8 of the source document: `list_tasks` can hand a client a page computed
against one snapshot of the data, and a later page of the same walk computed
against a different snapshot, with nothing in the response telling the caller
their walk is no longer coherent. The fix direction was already decided at
the parent: a revision counter the client must present back, rejected outright
when it no longer matches. This ADR turns that one paragraph into an
exhaustive, checkable spec.

### What `list_tasks` actually is today (read before designing)

I read the implementation before designing anything, per the task's own
instruction not to design in a vacuum. Two things changed the shape of this
ADR from what the parent task's wording implies:

1. **There is no cursor.** `pkg/pagination` (`pagination.go`) is pure
   `page`/`page_size` OFFSET pagination — `Params{Page, PageSize}` →
   `OFFSET/LIMIT`. `TaskRepo.List` (`internal/repository/postgres/task_repo.go:672`)
   is exactly this, filtered by `project_id` and ordered
   `updated_at DESC, tasks.id ASC` (the `id` tiebreak already closes the
   non-unique-sort-key class documented in memory `solution-workspace-cursor-corpus-tie-overlap-a7ae4c76`
   and task `#a1012e55`). So "the pagination cursor" the task description
   points at does not exist for `list_tasks` — the closest real cursor
   implementation in this codebase is `CommentCursor` (`internal/domain/comment_view.go`,
   used by `ListByAuthor`/`ListRecentByWorkspace` in `comment_repo.go`): a
   plain, non-opaque `(created_at, id)` tuple, sent back by the client as
   `before`/`before_id`, returned as `next_cursor`/`next_cursor_id`. §Decision 3
   below extends the **existing offset mechanism** (`pagination.Params`/`Page[T]`)
   rather than inventing a new cursor type, and borrows the comment-cursor's
   *transparency* convention (plain field, not a base64 opaque blob) because
   that is the one precedent this codebase already has.
2. **The task row's list-visible shape is wider than `tasks` columns.**
   `taskComputedCols` (`task_repo.go:76`) computes `subtask_count`,
   `artifact_count`, `vcs_link_count`, `assignee_name`, `reviewer_name`,
   `created_by_name` live in the `SELECT`. `artifact_count` and
   `vcs_link_count` come from the separate `artifacts` and `vcs_links`
   tables — **inserting or deleting one of those rows changes a task's
   list-visible shape without writing to `tasks` at all.** This is exactly
   the kind of gap the task asked me to be exhaustive about, and it would
   have been missed by reading only the `tasks` table's own mutation paths.

---

## Decision 1 — which mutations increment the counter

Enumerated by reading every write path that can change what a `list_tasks`
row looks like, not by guessing from table names. Each row states the
concrete DB operation and where it lives.

| # | Mutation | DB operation | Why it's list-visible | Included? |
|---|---|---|---|---|
| 1 | Task create | `INSERT INTO tasks` | new row appears in the list | ✅ |
| 2 | Task field update (title, description, priority, due_date, custom_fields, assignee, reviewer, delegation_level, human_gate, …) | `UPDATE tasks` | changes columns returned directly | ✅ |
| 3 | Task move (status change) | `UPDATE tasks SET status_id = …` | changes `status_id`, and moves the row across status-filtered pages | ✅ |
| 4 | Task soft-delete | `UPDATE tasks SET deleted_at = …` | row disappears from every `list_tasks` call (`deleted_at IS NULL` is unconditional in `TaskRepo.List`) — confirmed **no hard `DELETE FROM tasks`** exists anywhere in `internal/` | ✅ (covered by #2's trigger — same statement shape) |
| 5 | Label attach/detach | `UPDATE tasks SET labels = …` | `labels` is `pq.StringArray` **directly on `tasks`** (`domain.Task.Labels`, `db:"labels"`) — not a join table. Already covered by #2; listed separately only because the task description asked for it explicitly. | ✅ (same trigger as #2, no separate hook needed) |
| 6 | Label **rename** (renaming a label string across every task that carries it) | — | **No such operation exists in the codebase today.** Labels are free-form strings on the array; there is no rename endpoint or service function (`grep -rn "RenameLabel"` → nothing). If one is ever added, it will be a bulk `UPDATE tasks SET labels = …` over many rows in one project and is automatically covered by #2's trigger with no new code. Documented here so a future implementer doesn't have to re-derive that it's already covered. | N/A today, auto-covered if built |
| 7 | Subtask create/delete | `INSERT`/`UPDATE tasks` (parent's `subtask_count` is a live subquery) | changes the **parent's** row shape without writing to the parent row. Covered because the subtask insert/update is itself mutation #1/#4, in the **same project** (subtasks live in the same project as their parent in every path I found), so the same per-project counter bump also invalidates the parent's page. | ✅ (covered by #1/#2, no separate hook) |
| 8 | Artifact upload | `INSERT INTO artifacts` | bumps `artifact_count` on the owning task's row, with **no write to `tasks`** | ✅ — needs its **own** trigger on `artifacts` |
| 9 | Artifact delete | `DELETE FROM artifacts` (`artifact_repo.go:151`) | same as #8, opposite direction | ✅ — same trigger, `AFTER DELETE` |
| 10 | Artifact metadata update | `UPDATE artifacts SET metadata = …` (`artifact_repo.go:138`) | `metadata` is **not** in `taskComputedCols` — not list-visible today | ❌ excluded; if artifact metadata is ever surfaced in a list row, add this |
| 11 | VCS link create/delete | `INSERT`/`DELETE FROM vcs_links` (`vcs_link_repo.go:69,104,145`) | bumps `vcs_link_count`, no write to `tasks` | ✅ — own trigger on `vcs_links` |
| 12 | Comment/thread add or edit | `INSERT`/`UPDATE INTO comments` | **does not touch `tasks.updated_at`** (grepped `comment_service.go` + `comment_repo.go` for any `UPDATE tasks` — none), and `taskComputedCols` carries no comment-derived field (no `comment_count`, no last-comment preview). A `list_tasks` row is byte-for-byte identical before and after a comment is added. | ❌ excluded, see justification below |
| 13 | Assignee/reviewer/creator display-name change (renaming a user or agent) | `UPDATE users`/`UPDATE agents` | `assignee_name`/`reviewer_name`/`created_by_name` are joined live and would technically go stale | ❌ excluded, see justification below |
| 14 | Task status **definition** change (rename a status, change its category) on `task_statuses` | `UPDATE task_statuses` | could affect which rows match a `status_category` filter without any `tasks` row changing | ❌ excluded, see justification below |

**Justification for #12 (comments/threads) — the task explicitly asked me to
decide, not just describe.** I checked the actual response shape instead of
assuming: a `list_tasks` row carries `thread_id` but no comment content,
count, or timestamp. Adding a comment cannot change what any `list_tasks`
caller sees. Wiring a trigger for it would invalidate every open cursor in a
project on every comment — the single highest-frequency write in this
system — for zero payoff. **If a future change adds a `comment_count` or
`last_comment_at` field to the list row, this exclusion must be revisited in
the same PR**, not left to rot the way the identical "non-unique sort key"
fix rotted for comment cursors while task cursors got it (`#a7ae4c76`'s own
writeup names this exact failure mode: a fix that doesn't sweep every
sibling case).

**Justification for #13 and #14 — deliberately excluded, not overlooked.**
Both are real gaps in strict correctness, but including them fails a
cost/benefit test that #8–#11 pass: a user or agent renaming their
`display_name`, or an admin renaming a status, is a rare, workspace-wide
administrative action, not a per-task edit. Wiring it to the **per-project**
counter (Decision 2) would mean every open project's cursor across the
**entire workspace** goes stale on one profile rename — a blast radius wildly
disproportionate to the staleness it prevents (a name column lagging by
one page-walk is a cosmetic issue; a mid-page-walk task disappearing or
duplicating is a correctness issue). Named explicitly, with reasoning, so a
future reader can see this was decided and not missed — same standard the
enumeration itself is held to.

---

## Decision 2 — scope: **per-project**, not per-workspace

**Decision: one `task_list_revision` counter per `project_id`.**

**Justification (concrete tradeoff, not both options described):**

- `TaskRepo.List` — the function backing the `list_tasks` MCP tool this
  whole task is about — takes `projectID` as a **mandatory** first argument
  and every query is `WHERE project_id = $1 …`. There is no all-projects
  `list_tasks` call in the codebase; a caller paginating tasks is, by
  construction, paginating **one project**.
- A single workspace-wide counter would mean a task edit in *any* project
  invalidates the in-flight cursor of a caller paginating a *different*
  project. Measured against this workspace's real shape: `list_tasks` is
  the single most-called Mesh tool across the fleet (every agent's
  wake-up/status-check flow), and a workspace has upward of a dozen
  concurrently-active projects. A workspace-scoped counter would make
  **every** `list_tasks` cursor effectively single-page-lived — invalidated
  within seconds by unrelated projects' normal traffic — which defeats the
  purpose of having pagination at all and would make the 410 the *common*
  case instead of the *exceptional* one it's supposed to be.
- The implementation cost of "one counter vs. many" is not a real
  differentiator here: whichever scope is chosen, the trigger fires per
  write and updates one row. A per-project counter needs the trigger
  function to resolve `project_id` (already present on `tasks` directly,
  and reachable from `artifacts`/`vcs_links` via one join to `tasks`) — no
  harder than resolving a workspace, and the two watched-tables-without-`project_id`
  cases (`artifacts`, `vcs_links`) need that join either way.
- The two workspace-wide list surfaces that exist today —
  `TaskRepo.ListByStatusCategory` (workspace + category, backs
  triage-style views) and `TaskRepo.ListByAssignee` (workspace + assignee,
  backs `GET /agents/me/tasks` and its long-poll twin) — are **explicitly
  out of scope for this mechanism.** Both are documented as re-polled
  fresh each call (`ListByAssignee`'s own comment calls its consumer "the
  long-poll twin"), not walked page-by-page against a client-held cursor
  across a session the way `list_tasks` is. They have no persisted cursor
  to go stale. If a future change turns either into a stateful
  multi-page walk, it needs its own revision scope decision — most likely
  workspace-scoped, following the same reasoning applied here in reverse —
  and is not silently covered by this ADR.

**Storage:** a new table, not a column bolted onto `projects`:

```sql
CREATE TABLE task_list_revisions (
    project_id  UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    revision    BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Not a column on `projects` because `projects` already carries its own
`updated_at` (semantically: "the project's own metadata changed") and mixing
in a counter that bumps on every contained task's write would either (a)
require a second timestamp/column pair to avoid corrupting that semantic, or
(b) silently make `projects.updated_at` fire on unrelated child-row writes —
worth avoiding for a one-table, no-cost alternative. A missing row (project
created before this migration) reads as revision `0`; the migration
back-fills one row per existing project so this is a theoretical case only,
not a runtime null-check every reader needs.

---

## Decision 3 — cursor / continuation-token encoding

**Decision: extend the existing `pagination.Params`/`pagination.Page[T]`
types with a `list_revision` field. Do not introduce a new cursor type or an
opaque/base64 token.**

Concretely, in `pkg/pagination/pagination.go`:

```go
type Params struct {
    Page      int
    PageSize  int
    SortBy    string
    SortDir   string
    RawLimit  int
    // ListRevision, when non-zero, is the task_list_revision the caller
    // observed on a previous page of this same walk. Bound from the
    // `list_revision` query param. Zero means "first page of a new walk" —
    // no staleness check is possible or needed.
    ListRevision int64 `query:"list_revision"`
}

type Page[T any] struct {
    Items        []T
    TotalCount   int
    Page         int
    PageSize     int
    TotalPages   int
    HasMore      bool
    // ListRevision is the task_list_revision this page was computed
    // against. The caller echoes it back as `list_revision` on the next
    // page request of the same walk.
    ListRevision int64 `json:"list_revision"`
}
```

**Why extend this instead of building a keyset/tuple cursor like
`CommentCursor`:** the task pointed me at "the existing pagination cursor" to
avoid inventing a new mechanism, and the closest thing that phrase can mean
in this codebase either (a) doesn't exist for `list_tasks` (offset-only
today), or (b) is `CommentCursor`, a different endpoint family entirely.
Migrating `list_tasks` from `page`/`page_size` to a `(updated_at, id)`
keyset cursor would be a larger, valuable-but-unrequested change — it would
also fix the *separate* problem of OFFSET pagination reflowing rows when
earlier rows are inserted/deleted mid-walk (distinct from the *revision
staleness* problem this task is scoped to). I flag it as a named follow-up
below rather than silently doing it or silently not considering it. What
this ADR does do is borrow the one convention `CommentCursor` establishes
that transfers directly: **the token is a plain, visible field, not an
opaque blob** — a caller (and a human debugging a support ticket) can read
`list_revision: 47` directly off the JSON, the same way they can already
read `next_cursor`/`next_cursor_id`.

**Read-back mechanics:**
1. First call to `GET /projects/{id}/tasks` (`page` unset or `1`, no
   `list_revision`): the handler reads current `task_list_revisions.revision`
   for the project (defaulting to `0` if the row doesn't exist yet),
   computes the page as today, and returns it stamped with that revision in
   `list_revision`.
2. Client requests page 2+ and includes `list_revision=<value from page 1>`.
3. Handler re-reads the current revision for the project and compares
   **before** running the paged query (Decision 4 covers the mismatch path).
   On match, proceeds exactly as today and re-stamps the same revision on
   the response (it cannot have changed between the compare and the query
   in the same request, since nothing here holds a transaction open across
   requests — a same-millisecond write landing between the compare and the
   query is an accepted, vanishingly small race, not a correctness gap the
   client can act on differently than "your next page might also 410").

---

## Decision 4 — stale-cursor rejection contract

**HTTP 410 Gone**, mirroring the one precedent already in this codebase for
exactly this situation — `AgentHandler.EventStream`'s SSE `Last-Event-ID`
expiry (`internal/handler/agent_handler.go:916-922`), which already uses 410
+ an `"error": "cursor_expired"` body for "your continuation token is no
longer valid, start over."

```json
{
  "error": "list_revision_stale",
  "message": "task_list_revision changed since this cursor was issued (had 47, now 52); restart pagination from page 1",
  "requested_revision": 47,
  "current_revision": 52
}
```

- `error` uses a distinct code (`list_revision_stale`, not the SSE handler's
  `cursor_expired`) because the client-facing recovery action differs:
  SSE's `cursor_expired` says "call `get_my_tasks` for full state"; this one
  says "call `list_tasks` again from page 1 with no `list_revision`." Reusing
  the same string for two different required client actions would be exactly
  the "plausible but wrong" failure class this whole epic exists to close.
- `message` is human-readable and states the concrete before/after numbers —
  not just "stale" — so a support/debug read of a failed request tells the
  full story without a second lookup.
- `requested_revision` / `current_revision` are included as typed fields (not
  only embedded in `message`) so a client can branch on them programmatically
  without string-parsing, matching this codebase's existing preference for
  structured error bodies (`apierror.ValidationError`'s `Validation map[string]string`
  is the same pattern: prose in `Message`, structured detail in a named
  field).
- **Implementation note for subtask #6:** `pkg/apierror` has no `Gone()`
  constructor today (the SSE path builds its `map[string]string` inline,
  bypassing `apierror.Error` entirely). Recommend subtask #6 add
  `apierror.Gone(message string, requested, current int64) *Error` with the
  two extra fields threaded through `apierror.Error` (currently
  `Code/Message/Details/Validation`) rather than hand-rolling a second raw
  map literal — one fewer inconsistent error shape in the codebase, not one
  more. Not required to land this ADR; flagged so #6 doesn't have to
  re-discover the gap.
- **What must NOT happen:** silently falling back to page 1, silently
  serving the requested OFFSET against the new state, or returning 200 with
  a `stale: true` flag the caller can ignore. The parent task's own
  complaint is that a plausible-looking 200 is worse than a clear rejection;
  a soft-fail 200 here would reproduce the exact defect being fixed.

---

## Migration & trigger sketch (for subtask #5 — not implemented here)

Per §1b (deploy discipline), this is additive-only and ships before any code
reads it:

1. `CREATE TABLE task_list_revisions` (Decision 2), back-filled with one row
   per existing `project_id` at `revision = 0`.
2. One trigger function, `bump_task_list_revision(project_id UUID)`, doing
   `INSERT … ON CONFLICT (project_id) DO UPDATE SET revision = task_list_revisions.revision + 1, updated_at = NOW()`
   (the `INSERT … ON CONFLICT` form makes the back-fill step in (1) a
   non-blocking correctness net, not a hard prerequisite — a project created
   between migration and back-fill still gets a row on its first bump).
3. `AFTER INSERT OR UPDATE ON tasks FOR EACH ROW` → calls the bump function
   with `NEW.project_id` (covers mutations #1–#5, #7 from Decision 1's table
   in one trigger).
4. `AFTER INSERT OR DELETE ON artifacts FOR EACH ROW` → resolves
   `project_id` via `SELECT project_id FROM tasks WHERE id = COALESCE(NEW.task_id, OLD.task_id)`,
   then bumps (covers #8–#9).
5. `AFTER INSERT OR DELETE ON vcs_links FOR EACH ROW` → same join pattern
   (covers #11).
6. Verify RLS interaction before shipping: `migrations/20260301030_enable_rls_policies.sql`
   enables row-level security on at least some of these tables; confirm the
   trigger functions run with sufficient privilege (`SECURITY DEFINER` or an
   RLS-exempt role) to write `task_list_revisions` regardless of the
   invoking request's row visibility — untested here, this ADR only names
   the check subtask #5 must not skip.

---

## Out of scope / named follow-ups

- **Keyset/tuple cursor migration for `list_tasks`** (replacing
  `page`/`page_size` with a `(updated_at, id)` cursor like `CommentCursor`)
  would additionally fix OFFSET-reflow on insert/delete mid-walk. Valuable,
  not requested by this task, not free (it changes the wire contract for
  every `list_tasks` caller). Separate task if wanted.
- **Workspace-wide list surfaces** (`ListByStatusCategory`, `ListByAssignee`)
  — explicitly not covered; see Decision 2's justification.
- **Display-name and status-definition staleness** (#13, #14 in Decision 1)
  — explicitly excluded with reasoning, not silently dropped.
- **`artifacts.metadata` update** (#10) — excluded because it isn't
  list-visible today; revisit in the same PR that makes it list-visible, if
  ever.

---

## References

- `pkg/pagination/pagination.go` — current offset pagination, extended by
  Decision 3.
- `internal/repository/postgres/task_repo.go:76` (`taskComputedCols`),
  `:672` (`List`) — the exact query and computed columns this ADR is scoped
  to.
- `internal/domain/comment_view.go`, `internal/repository/postgres/comment_repo.go:180-245`
  — `CommentCursor`, the codebase's one existing real cursor, used as the
  transparency-convention precedent in Decision 3.
- `internal/handler/agent_handler.go:883-922` — SSE `Last-Event-ID` 410
  precedent, used as the status-code and body-shape precedent in Decision 4.
- `pkg/apierror/apierror.go` — existing structured error shape.
- Mesh memory `solution-workspace-cursor-corpus-tie-overlap-a7ae4c76` — the
  non-unique-sort-key cursor bug class and its "the fix does not generalise
  itself" lesson, informing why Decision 1's enumeration is written as an
  explicit table rather than a general rule.
- Parent task `ad22bfda` — original three-failure-class writeup and §3.8
  source text this ADR turns into a spec.
