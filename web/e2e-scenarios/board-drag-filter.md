# Scenario: Board — drag between columns and search filter

**Feature:** Board drag-and-drop (status change) + toolbar search filter
**Scenario ID:** board-drag-filter
**Owner:** Hugh
**Created:** 2026-09-25
**Spec file:** `web/e2e/board-drag-filter.spec.ts`
**Depends on:** `data-testid="board-column"` / `data-column-id` / `data-status-id`
and `data-testid="task-card"` / `data-task-id` in `web/src/pages/board.tsx`
(landed `d037ca1b`, live on prod — verified below, not assumed).

---

## Given

- Same sandbox as `task-board-load.md`: `E2E_USER_EMAIL` / `E2E_USER_PASSWORD`,
  member of exactly the scratch workspace `e2e-ci-sandbox`, project
  `e2e-fixtures`. Own login (own browser context), not shared with the other
  spec files — Mesh's one-shot refresh-token rotation forbids replaying a
  session between contexts (see `task-board-load.md`'s "Test data" section).
- The sandbox project has at least two non-closed statuses (required for a
  cross-column drag to mean anything) — read live via
  `GET /api/v1/projects/:id/statuses`, not assumed from a fixture file.
- This suite creates its own throwaway task (`[e2e-board] drag+filter
  <run-tag>`) via `POST /api/v1/projects/:id/tasks` rather than reusing the
  permanent "E2E read fixture — do not delete" row — dragging or filtering
  around a task other suites may be reading concurrently would be a shared
  fixture race, not a fixture reuse.

## When

1. The throwaway task is created directly in the first non-closed status
   (by `position`), then the board is loaded.
2. A drag is driven with `page.mouse` (down → move past the 5px
   `activationConstraint` → move in intermediate steps → up) from the card to
   the second non-closed status's column — the same technique
   `web/perf/counters.spec.ts`'s `board.drag` measurement already proved
   reliable against dnd-kit's `PointerSensor`/`closestCorners` on this exact
   page.
3. The toolbar search box (`placeholder="Search tasks..."`) is filled with the
   task's unique run-tag, then cleared again.

## Then

- **Drag, DOM:** immediately after the drop, the card is gone from the source
  column's DOM subtree (`toHaveCount(0)`) and present, exactly once, inside
  the target column's DOM subtree (`toHaveCount(1)`) — presence in the target,
  not just absence from the source, so a drop that vanishes the card
  entirely would not pass.
- **Drag, server (§1n — a repaint is not a move):** a fresh, independent
  `GET /api/v1/tasks/:id` (not the store's cached copy) reports
  `status_id` equal to the **target** status's id exactly — not merely
  "different from the source", which a drop into a third, unintended column
  would also satisfy.
- **Filter, behavioral:** before filtering, the board shows more than one
  card (the throwaway task is not the only row — otherwise a search that
  "returns everything" could pass by accident). After typing the run-tag,
  the board shows **exactly one** card, and its `data-task-id` is the
  throwaway task's — not `toBeVisible()` on a card already there, and not a
  bare "count went down".
- After clearing the search box, the visible card count returns to exactly
  the pre-filter count — proves the filter is reversible state, not a
  one-way navigation.
- F1 harness, unconditional through the whole scenario: zero failed
  `/api/v1/*` responses, zero `console.error`, zero `pageerror`, zero
  resource-timing entries with `responseStatus >= 500` (`/auth/refresh` is
  exempt, same reason as `task-board-load.md`).

## Deep-verify notes (§1c)

- The client-side "card left the source column" assert alone was the
  pre-`data-testid` gap this task exists to close (`#25aa01f1`'s AC): a
  visual reflow without a persisted status change would still make the card
  disappear from the source DOM subtree, so the server-side `GET` is not
  optional decoration — it is the assertion that actually distinguishes a
  real move from a client-only repaint.
- The filter assert names the surviving card's `data-task-id`, not a card
  count on its own — a search box that filtered to the WRONG single card
  (e.g. always the first one, ignoring the query) would still make a
  bare-count assertion pass.

## F1 fixture

Inline in the spec, same shape as `task-board-load.md`'s: `window.__ceErrs`
via `addInitScript`, `page.on("pageerror")`, `page.on("response")` for
failing `/api/v1/*` calls, resource-timing `responseStatus >= 500`.

## Test data

- One throwaway task per run, title-prefixed `[e2e-board]`, deleted in
  `afterAll` regardless of which assertion failed. Cleanup is itself
  deep-verified: after the `DELETE`, a `GET` on the same id must answer 404 —
  a `DELETE` returning 204 without the row actually being gone would
  otherwise report false success (the same class of gap `docs-write-path.md`
  closes for documents).
- No `storageState` — same one-context, one-login reasoning as
  `task-board-load.md`.

## Negative controls (what proves the asserts are alive)

| Break | Expected |
|---|---|
| server-side assert inverted (`toBe(originalStatusId)` instead of the target) | fails at that line with both ids quoted |
| filter assert loosened to "count decreased" instead of "count is exactly 1 with the right id" | still catches a wrong-card bug, but was proven insufficient in isolation — see Deep-verify notes |
| secrets absent | run fails in `global-setup`, never a skip |
| `data-testid`/`data-column-id` absent from the deployed bundle (the exact prior state of this task, before `d037ca1b`) | run fails at `dragCard()`/`boundingBox()` — "card has no bounding box" — not a silent no-op |

Run against a deliberately inverted server-side assertion (`.toBe(sourceStatusId)`
instead of `targetStatusId`), captured in the closing comment: pipeline
[#6055, job `authed-e2e` (92675)](https://git.entire.host/entire-vc/evc-mesh/-/jobs/92675),
**failed** for the expected reason:

```
Error: the drag must persist server-side to the TARGET status, not just repaint the column
expect(received).toBe(expected) // Object.is equality
Expected: "76b8505a-4454-4e0b-bd0c-a1ac60ead015"
Received: "eb0fd990-8f7d-420f-b13c-225e354301d0"
  > 227 |   ).toBe(sourceStatusId); // TEMP RED CONTROL — §0x, reverted in the next commit
```

All 7 other jobs (migration-reversibility-gate, perf-counters, perf-api-bin,
perf-bundle, `test:web`, tenancy-rbac-gate, repo-integration, `test`, lint,
mcp-reference-gate, nginx-real-ip-syntax, self-host-env) green — only the
deliberately broken assertion failed, at the expected line, quoting both ids —
not a setup/auth/selector problem.

## Out of scope

- Priority/assignee/tag filters (same toolbar, same `filteredTasks` code path
  in `board.tsx` — search alone already exercises the general filter
  mechanism the card asked for; a UI ambiguity note for anyone adding those
  next: `board-toolbar.tsx` renders TWO `<select>` elements for
  `priorityFilter`/`assigneeFilter` — one for desktop, one inside the
  `filtersOpen && (...)` mobile panel — the mobile one only exists in the DOM
  once that panel has been opened, so a selector that does not scope past the
  desktop `hidden md:contents` wrapper will find two matches the moment
  mobile filters are touched).
- **Trap actually hit during this task's own red/green cycle** (not
  hypothetical): `placeholder="Search tasks..."` is not unique to the board
  toolbar — `web/src/components/layout/header.tsx` renders a global,
  cmdk-style search combobox with the identical placeholder, present on every
  page including the board. `page.getByPlaceholder("Search tasks...")`
  resolved to 2 elements and failed in strict mode
  (pipeline #6063, job 92786). Fixed by selecting on role instead:
  `page.getByRole("textbox", { name: "Search tasks..." })` — the toolbar's
  plain shadcn `<Input>` is `role=textbox`, the header's is `role=combobox`.
- Reordering within a column (`sortBy`) — different code path
  (`calculatePosition`), not covered here.
- Visual/mobile viewport — separate visual scenario per §1k.
