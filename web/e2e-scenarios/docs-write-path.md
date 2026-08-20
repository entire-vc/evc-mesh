# Scenario: A user creates a document, comments on it, and the run cleans up

**Feature:** Authenticated write path (documents + document comments)
**Scenario ID:** docs-write-path
**Owner:** Howard
**Created:** 2026-08-20
**Spec file:** `web/e2e/authed-app.spec.ts`

---

## Why this scenario exists

§1n says a user-facing task does not reach `done` without an independent
authenticated browser run with **behavioural** asserts. Until 2026-08-20 the
CI user was a workspace `viewer`, whose permission matrix in
`internal/middleware/rbac.go` is literally empty — so every write endpoint
answered 403 and the required check could only ever prove reads. The rule and
the harness disagreed, and the harness was going to lose quietly: work would
keep closing on prose.

## Given

- The CI user `E2E_USER_EMAIL` belongs to exactly one workspace,
  **`e2e-ci-sandbox`**, with role **`member`**, and to no real workspace.
  `admin@entire.vc` is the sandbox owner, so the CI credential cannot delete
  the workspace or change roles — verified live: `DELETE /workspaces/<id>` and
  `PATCH /workspaces/<id>/members/<self>` both answer 403.
- The sandbox holds two permanent fixtures: project **`e2e-fixtures`** and the
  task **"E2E read fixture — do not delete"** (the read assert names it).
- `APP_BASE_URL` is the deployed host. This suite writes to a real server; the
  sandbox is what keeps that from being a real *workspace*.

## When

- The suite logs in once (see `task-board-load.md` for why one context, no
  `storageState`).
- The browser opens `/w/e2e-ci-sandbox/p/e2e-fixtures/docs`, clicks **New**,
  types a title unique to this run (`[e2e] document <run id>-<attempt>`) and
  presses **Create**.
- It then writes a body to that document, posts a comment quoting a phrase from
  it, resolves the comment and unresolves it.

## Then

- The title the user typed is **rendered back** in the document tree. It did not
  exist anywhere on the page before the click, so this is a behavioural assert
  and not a presence check on pre-existing furniture.
- Asked independently, the server holds **exactly one** document with that
  title. (No `count == before + 1` assert: two PR runs can be in flight at once,
  and a count is not concurrency-safe. The unique title is the same claim
  without the flake.)
- `POST /documents/:id/comments` returns **201** — under the old `viewer`
  credential this line is a 403, which is precisely the defect being kept fixed.
- The stored anchor quotes the document (`anchor.exact` equals the quote,
  `orphaned: false`): the server resolved the quote against the real body rather
  than trusting client offsets (`pkg/mdoc.SpanMatchesQuote`).
- Resolve sets `resolved_at`; unresolve clears it; the comment is in the
  document's thread list read back through the endpoint the reading view uses.
- The F1 harness (failed `/api/v1/*`, `console.error`, `pageerror`, 5xx) covers
  the create round-trip too, so a cheerful UI over a failed request is caught.

## Test data and cleanup

- Everything this run creates is named with the `[e2e]` prefix plus the GitHub
  run id, and is deleted in `afterAll` — **comments before documents**: a
  document delete is soft and the comment routes resolve the document first, so
  a comment that outlives its document becomes unreachable over the API and can
  only be removed in SQL (learned the expensive way on `#fd2bfec6`).
- Cleanup is driven by the ids that actually came back, not by what the plan
  said should exist, so a mid-way failure still cleans up.
- The suite prints the sandbox document list before and after, and asserts none
  of its own ids remain. It does **not** assert "no scratch documents at all" —
  that would go red on a concurrent run's in-flight document.
- A leftover from a crashed run is swept at the start of the write test, but
  only if it is older than an hour, for the same concurrency reason.

## Negative controls (what proves the asserts are alive)

| Break | Expected |
|---|---|
| invert the "server holds exactly one such document" assert | that test fails, job red |
| put the CI user back in a real workspace | the blast-radius assert fails, job red |
| CI user demoted to `viewer` | comment create is 403, job red |

## Out of scope

- Creating the comment through the editor's own selection UI — that is D7's
  scenario (`#fd2bfec6`); this one proves the permission and the write path
  exist for it to use.
- Task creation through the board UI.
