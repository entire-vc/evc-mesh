# Scenario: Authenticated app — initial load

**Feature:** Authenticated shell + task read path
**Scenario ID:** task-board-load
**Owner:** Linus
**Created:** 2026-06-18
**Updated:** 2026-08-20 — rewritten against how this app actually authenticates
**Spec file:** `web/e2e/task-board.spec.ts`

---

## Given

- A dedicated CI user exists in Mesh (`E2E_USER_EMAIL` / `E2E_USER_PASSWORD`), a
  workspace **member** — not an owner, and not a member of any project
- Mesh authenticates natively (`POST /api/v1/auth/login`). There is no Casdoor /
  OIDC flow in this app; an earlier revision of this scenario assumed one and
  drove it against a host that does not resolve
- The workspace contains tasks (the read-path assert requires real rows)

## When

- The suite logs in once, in a single shared browser context
- It reads `/api/v1/auth/me`, `/api/v1/workspaces` and that workspace's task search
- It navigates to the app root (`/`) and waits for `networkidle`

## Then

- Login returns 200 and an access token — a wrong password fails the run here
- `/api/v1/auth/me` returns **our** user's email and `is_active: true`
- The workspace list is a non-empty array, and workspace task search returns a
  page with an `items` array and `total_count > 0`
- The browser is **not** on `/login` (AppLayout redirects anonymous visitors
  there, so staying off it is the auth assert)
- The URL settled inside `/w/<workspace-slug>/…` and workspace navigation rendered
- Zero failed `/api/v1/*` responses during load, zero `console.error`, zero
  `pageerror`, zero HTTP 5xx

## Deep-verify notes (§1c)

- HTTP 200 from `GET /` is NOT the acceptance criterion — it's the SPA shell.
- Every assert is unconditional. The previous spec wrapped its only API assert
  in `if (resp.status() === 200)`, so a 401 asserted nothing, and its shape
  check (`typeof tasks === "object"`) was true for `{}` as well as for an array.
- `GET /api/v1/tasks` is agent-key territory and 404s for a user token; the
  user-facing read path is `GET /api/v1/workspaces/:ws_id/tasks?search=…`.

## F1 fixture

Inline in `task-board.spec.ts`, and **unconditional** — it previously sat behind
`PW_FAIL_ON_CONSOLE_ERROR` / `PW_FAIL_ON_5XX`, which CI never set:
- `window.__ceErrs` collector injected via `addInitScript`
- `page.on("pageerror")` for uncaught exceptions
- `page.on("response")` for any failing `/api/v1/*` call (`/auth/refresh` is
  exempt: the app probes it before it knows whether anyone is logged in)
- `performance.getEntriesByType('resource')` filtered for `responseStatus >= 500`

## Test data

- No fixture data seeded — reads existing workspace data.
- **No `storageState`.** Refresh tokens rotate one-shot and reuse of a revoked
  token revokes every session for the user (`internal/auth/service.go`,
  `ErrTokenReused`). Replaying a saved cookie into a second browser context
  would make the suite kill its own session. One context, one login — which
  also keeps the suite inside the 5-requests/minute cap on `/auth/login`.

## Negative controls (what proves the asserts are alive)

| Break | Expected |
|---|---|
| secrets absent | run fails in `global-setup` (never a skip) |
| wrong password | run fails at the login assert, 401 quoted |
| an assert inverted | that test fails and the job goes red |

## Out of scope

- Task creation / editing / deletion (separate scenarios)
- Mobile viewport (separate visual scenario per §1k)
