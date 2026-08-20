# Scenario: Authenticated app — initial load

**Feature:** Authenticated shell + task read path
**Scenario ID:** task-board-load
**Owner:** Linus
**Created:** 2026-06-18
**Updated:** 2026-08-20 — moved to the E2E sandbox workspace; the suite now also
covers a write path (`docs-write-path.md`)
**Spec file:** `web/e2e/authed-app.spec.ts` (renamed from `task-board.spec.ts`)

---

## Given

- A dedicated CI user exists in Mesh (`E2E_USER_EMAIL` / `E2E_USER_PASSWORD`).
  It belongs to exactly one workspace — the scratch **`e2e-ci-sandbox`** — with
  role **`member`**, and to no real workspace. The isolation, not the role, is
  what bounds a leaked credential: `member` inside a sandbox that holds nothing
  but its own fixtures can do nothing worth doing, while `viewer` in the real
  workspace could read every task in it and still prove no write path (§1n).
  `admin@entire.vc` owns the sandbox, so CI cannot delete it or change roles
- Mesh authenticates natively (`POST /api/v1/auth/login`). There is no Casdoor /
  OIDC flow in this app; an earlier revision of this scenario assumed one and
  drove it against a host that does not resolve
- The sandbox contains the permanent fixture task "E2E read fixture — do not
  delete" (the read assert names it, rather than settling for `total_count > 0`)

## When

- The suite logs in once, in a single shared browser context
- It reads `/api/v1/auth/me`, `/api/v1/workspaces` and that workspace's task search
- It navigates to the app root (`/`) and waits for `networkidle`

## Then

- Login returns 200 and an access token — a wrong password fails the run here
- `/api/v1/auth/me` returns **our** user's email and `is_active: true`
- The workspace list is **exactly** `[e2e-ci-sandbox]` — a blast-radius assert:
  adding this credential to a real workspace turns the check red the same day
- Workspace task search returns a page whose `items` contain the fixture task
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

Inline in `authed-app.spec.ts`, and **unconditional** — it previously sat behind
`PW_FAIL_ON_CONSOLE_ERROR` / `PW_FAIL_ON_5XX`, which CI never set:
- `window.__ceErrs` collector injected via `addInitScript`
- `page.on("pageerror")` for uncaught exceptions
- `page.on("response")` for any failing `/api/v1/*` call (`/auth/refresh` is
  exempt: the app probes it before it knows whether anyone is logged in)
- `performance.getEntriesByType('resource')` filtered for `responseStatus >= 500`

## Test data

- Reads the sandbox fixtures (`e2e-fixtures` project + the fixture task).
  Documents and comments created by the write scenario are cleaned up there —
  see `docs-write-path.md`.
- **No `storageState`.** Refresh tokens rotate one-shot and reuse of a revoked
  token revokes every session for the user (`internal/auth/service.go`,
  `ErrTokenReused`). Replaying a saved cookie into a second browser context
  would make the suite kill its own session. One context, one login — which
  also keeps the suite inside the 5-requests/minute cap on `/auth/login`.

## Artifact hygiene

`trace` and `screenshot` are **off**, deliberately. Every API call here carries
`Authorization: Bearer <live token>`; a trace records headers, and GitHub masks
secrets in logs but not inside uploaded artifacts. Playwright cannot redact
headers from a trace, so a failing run would otherwise publish a working token
and real task titles into `playwright-report`.

## Negative controls (what proves the asserts are alive)

| Break | Expected |
|---|---|
| secrets absent | run fails in `global-setup` (never a skip) |
| wrong password | run fails at the login assert, 401 quoted |
| an assert inverted | that test fails and the job goes red |

## Out of scope

- Document and comment writes — `docs-write-path.md`, same spec file
- Task creation / editing / deletion (separate scenarios)
- Mobile viewport (separate visual scenario per §1k)
