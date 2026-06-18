# Scenario: Task board — initial load

**Feature:** Task board
**Scenario ID:** task-board-load
**Owner:** Linus
**Created:** 2026-06-18
**Spec file:** `web/e2e/task-board.spec.ts`

---

## Given

- The user is logged in via Casdoor as an agent user with access to at least one workspace
- The workspace contains at least one task list / column configuration

## When

- The user navigates to the app root (`/`)
- The page finishes loading (`networkidle`)

## Then

- The task board container is visible (not just the shell HTML — the actual board element)
- At least one board column is rendered (count > 0 — behavior assert, not presence)
- No `console.error` calls were recorded during page load (F1 fixture)
- No HTTP 5xx responses were triggered during page load (F1 fixture via `performance.getEntriesByType`)
- No uncaught JS exceptions (`pageerror`) occurred

## Deep-verify notes (§1c)

- HTTP 200 from `GET /` is NOT the acceptance criterion — it's the SPA shell.
- The actual verification is: board DOM element rendered AND task column count > 0.
- The `GET /api/v1/tasks` deep-verify in the spec asserts on response shape, not just status code.

## F1 fixture

Inline in `task-board.spec.ts` via `withF1Fixture()`:
- `window.__ceErrs` collector injected via `addInitScript`
- `performance.getEntriesByType('resource')` filtered for `responseStatus >= 500`
- Both checked after each test action when `PW_FAIL_ON_CONSOLE_ERROR=1` / `PW_FAIL_ON_5XX=1`

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts` (login-once, no per-test auth)
- No fixture data seeded — reads existing workspace data

## Out of scope

- Task creation / editing / deletion (separate scenarios)
- Mobile viewport (separate visual scenario per §1k)
