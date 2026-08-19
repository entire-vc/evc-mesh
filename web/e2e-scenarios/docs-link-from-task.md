# Scenario: Docs — link a document from a task description and from a comment

**Feature:** Docs — document links in task text (epic Docs, unit D8)
**Scenario ID:** docs-link-from-task
**Owner:** Garfield
**Created:** 2026-08-20
**Spec file:** `web/e2e/docs-link-from-task.spec.ts` — **follows the deploy, see "Sequencing"**

---

## Given

- The user is logged in via Casdoor as an agent user with access to at least one project
- That project has at least two documents with distinguishable titles
- That project has at least one task

## When / Then

### 1. From the task description

- **When** the user edits the task description, types `[[` followed by part of a document's title
- **Then** an inline menu appears listing matching documents — and **only** matching ones
- **When** they pick one (click, or arrows + Enter)
- **Then** the text now contains a markdown link whose label is the document title and whose target is `/w/:ws/p/:project/docs/:docId`
- **Then** the caret sits **after** the link, not inside the URL
- **When** the description is saved and rendered
- **Then** the link is visible, and **clicking it opens that document in the same tab** — no new browser tab, no full page reload

### 2. From a comment

- Every assertion above, in the comment box on the same task
- **And** `@` still opens the mention menu in the same box — the two triggers coexist

### 3. The menu is honest

- **When** the query matches nothing → **then** the menu says so and offers **no** options. It must not fall back to listing every document: a writer who sees a list after typing a query that matched nothing will pick the wrong document.
- **When** Escape is pressed → **then** the menu closes and the text is **unchanged**, `[[query` and all.
- **When** the writer types ordinary markdown like `[label](https://example.com)` → **then** no menu opens.

### 4. External links are unaffected

- **When** the description contains an ordinary external link
- **Then** it still opens in a new tab with `rel="noopener noreferrer"`
- This is the negative control for run 1: a fix that made every link navigate in place would satisfy "the document link opens in the same tab" and break every external link in the product.

### Every run

- No `console.error` / `pageerror` (F1 fixture)
- No HTTP 5xx (F1 fixture)
- Anything the run created is removed at the end, pass or fail

## Deep-verify notes (§1c)

- The assertion in run 1 is **which document opened** — the URL after the click, and the document title on screen. "A link exists" and "navigation happened" both pass while pointing at the wrong document.
- **HTTP 200 proves nothing here.** The route is client-side; a broken link and a working one both leave the server answering the same SPA shell.
- Asserting the **absence** of the menu in run 3 matters as much as its presence in runs 1-2: a trigger that fires on every `[` would pass every positive case and make markdown unusable.

## Sequencing — why the spec is not in the same PR as the feature

`Authed E2E` in `ci.yml` runs `npx playwright test` against **`APP_BASE_URL`, which defaults to production**. A spec for a feature that is not deployed yet fails on its own PR and blocks the merge that would deploy it. Same order as `docs-paragraph-link.md` and `docs-text-comments.md`: feature merges and deploys → spec follows, green against a prod that has the feature.

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts` (login once)
- Creates its own document with a run-unique title so two concurrent runs cannot match each other's suggestions
- Deletes it in teardown

## Out of scope

- Searching document **content**, and choosing a scope of docs / Team Relay — that is D9. This unit searches titles within the task's own project.
- Linking a document from another project (documents are project-scoped by design; the task's project is the scope)
- Mobile viewport (separate visual scenario per §1k)
