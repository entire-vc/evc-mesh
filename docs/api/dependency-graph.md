# Dependency graph read contract

`GET /api/v1/projects/:proj_id/dependency-graph` is read-only. The authenticated
workspace and explicit project membership apply to the project and focus task.
Human workspace administrators retain their existing project-access bypass;
agents always need explicit project membership. Cross-project focus is allowed
only within the same workspace and when the focus task is accessible.

## Modes and continuation

- No query parameters: legacy full compact graph, including canonical edges.
  Existing timeline callers keep their complete `tasks`/`dependencies` response.
- `?mode=overview&page=1&page_size=200`: compact project overview. Defaults are
  page 1 / size 50; size must be 1–200 (1000 is rejected with 400). Nodes sort by
  priority (urgent, high, medium, low, none), then UUID. `search` matches title or
  UUID, case insensitive. Terminal nodes are collapsed unless `completed=true`.
- `?focus=<uuid>&before_page=1&after_page=1`: center and direct prerequisites /
  dependents, ten nodes per branch; at most 21 task nodes initially. Search does
  not hide the focus chain. `relations_page=1` explicitly adds up to ten hierarchy
  / related nodes. `completed=true` expands terminal groups. Follow each branch's
  `has_more` with its own page number and accumulate IDs, deduplicating shared
  before/after/related nodes. Pages are positive integers up to 1,000,000.

Pass the first response's `revision` on subsequent requests with the same mode,
project and focus. Changed visible data returns 409; restart pagination and
replace the accumulated view. Revision covers visible names, categories,
ordering, counts, canonical incident edges and visible cycle diagnostics.
Changing restricted metadata or unrelated private topology does not expose
activity through the token. A single response uses a read-only repeatable-read
transaction. Empty results are arrays, never null.

## Counts and completeness

`total_count` is the number of eligible overview tasks, or the number of unique
center / neighbor / relation identities in focus (including collapsed terminals).
`scope_total_count` includes terminal overview tasks matching the search;
`completed_count` reports terminal identities. Focus branches independently
provide `ids`, `total_count`, `completed_count`, `unknown_count`, `page`, `has_more`.
The top-level `unknown_count` deduplicates restricted/missing boundary identities.

`has_more` reports outstanding pages; focus also reports unexpanded relations.
`visibility_complete=false` means a boundary's metadata cannot be established.
`complete=true` means continuation is exhausted, no focus terminals remain
collapsed and visibility is complete. On a final overview page it describes
completion of the accumulated walk, rather than that page containing every task.
An overview with terminal collapsing is complete for its requested open-work scope.
Never infer “ready” from `complete` or zero visible edges.

Each available node carries exact direct `blockers`, `unfinished_blockers`,
`unknown_blockers` and `dependents`. Unknown prerequisites conservatively count
as unfinished; their private status cannot affect these counts. Both `done` and
`cancelled` are terminal, matching canonical server auto-transition semantics.
These are dependency facts; human gates, start-after and other workflow locks
remain authoritative independently.

## Identity, edges and diagnostics

The compact node has `id`, `project_id`, `project_name`, `project_slug`, `title`,
`status_id`, `status_name`, `status_category`, `priority`, `assignee_name`,
`parent_task_id`, `human_gate`, `start_after`, counts and `cycle`. Descriptions,
custom fields, comments, checkouts and secrets are never fetched. Accessible
external nodes retain project metadata. Restricted/missing nodes retain only
identity, `availability` and zero/empty metadata; those counters are unknown,
not evidence of zero blockers. A missing or inaccessible focus returns 404.

Canonical blocking direction is `depends_on_task_id → task_id`. Only `blocks`
contributes blocker counts and cycles. `parent_task_id`, `is_child_of` and
`relates_to` remain separate relations; no synthetic blocking edges are created.
Overview provides facts/counts and an empty edge array; focus provides canonical
incident edges whose endpoints are included in the current payload. Expanding a
completed bridge restores its original edges. Shared neighbors and self-loops
never inflate the unique top-level count.

Historical cross-workspace edges (written before the workspace guard on
`task_dependencies` existed; new ones are refused by `assertSameTenancy` and the
trigger) are not served: an edge with an endpoint in another workspace is dropped
before any read, so the foreign task id never appears, not even as a `restricted`
node, and it does not count toward `blockers`, `unknown_blockers` or `dependents`.
`restricted` therefore only means a task of the same workspace the actor may not
see. Production holds no such edges at the time of writing (0 of 3290); the filter
is a guard, not a cleanup.

Actor-authorized workspace ID-only topology detects cycles leaving and
re-entering the project through accessible projects. Tenant-invalid or restricted
edges do not enter cycle detection or revision hashing. `cycle_count` is the
authorized focus component size; `cycle_ids` contains only available nodes in the
payload. Restricted boundaries remain unknown and visibility-incomplete; absence
of a visible cycle does not assert that an inaccessible path is acyclic.
No diagnostic changes tasks, edges or checkout/workflow state.

## Verification and cost

Overview performs five bulk SELECTs; focus performs six (plus transaction
control, independent of node count). Focus fetches metadata only for its one-hop
neighborhood, parent and children, never all unrelated project tasks. ID-only
authorized workspace topology is read for cycle diagnostics. Pagination is applied
after complete counting; no repository PageSize 1000→200 clamp is involved.

Real PostgreSQL tests exercise the actual HTTP handler and project-access
middleware: 201 nodes with the blocker after page 1, 1000 nodes / 3000 edges,
stable continuation, restricted-status invariance, deleted endpoints, human /
agent policy, cross-workspace refusal and canonical edge preservation. Optional
`pg_stat_statements` instrumentation on an isolated verification database
asserts six focus reads and logs p50/p95 for 20 focus requests. Projection tests
cover hierarchy, terminal bridges, cross-project cycles and legacy self-loops
(new self-loops are rejected by the database constraint).
