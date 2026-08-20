# Scenario: Mentions — the bell and the Mentions tab agree, and both include documents

**Feature:** Docs — @-mentions in document comments reach the mention inbox
**Scenario ID:** docs-mention-inbox
**Owner:** Bill
**Created:** 2026-08-20
**Task:** `01b2a9c9` (found on acceptance of `#0401f2a9` / PR #632)
**Spec file:** `web/e2e/docs-mention-inbox.spec.ts` — **follows the deploy, see "Sequencing"**

---

## The defect this pins

On one screen, at the same moment: the bell showed **2** notifications «Ann Author
mentioned you on: Rollback Plan», and Activity → Mentions showed «No mentions yet.»
Both were rendering honestly from their own source — the bell reads the
notification table, the tab read `/me/mentions`, which is task-only. The tab was
not empty; it had not looked.

## Given

- Two users (or a user and an agent) with access to the same project — an author
  and a recipient. The recipient is the one logged in for the assertions.
- That project has a document with at least one paragraph.

## When / Then

### 1. A document mention appears in the Mentions tab

- **When** the author comments on the document with `@<recipient>`
- **And** the recipient opens Activity → Mentions
- **Then** a row naming **that document by title** is present
- **And** the tab does **not** say «No mentions yet» anywhere on the page

### 2. The bell and the tab do not contradict each other

- **When** the recipient opens Activity → Mentions **with the bell dropdown open**
- **Then** whatever the bell counts, the tab shows: a non-zero bell badge and an
  empty-state tab is a **failure**, and it is the literal screenshot this
  scenario exists to make impossible.

### 3. Clicking the mention opens the document at that comment

- **When** the recipient clicks the document mention row
- **Then** the URL is `/w/:ws/p/:project/docs/:docId?comment=<comment_id>`
- **And** the document opens, the comment rail is open, and **that thread is the
  focused one** — not merely "the rail is visible"
- **And** if the mention was on a **reply**, the focused thread is the one
  containing it

### 4. Clicking the bell notification does the same

- **When** the recipient clicks the «mentioned you on: …» entry in the bell
- **Then** the same document opens. (Before this change the entry was inert:
  the handler navigated only when `metadata.task_id` was present, and a document
  notification has no task.)

### 5. Task mentions are not broken — regression

- **When** the author `@`-mentions the recipient in a **task** comment
- **Then** the row appears in the same list, and clicking it opens the task
- **And** the row is marked seen against `/me/mentions`, not the document inbox

### 6. The empty state is a claim, not a default

- **When** the recipient has no mentions of either kind
- **Then** «No mentions yet» is shown
- **When** one of the two inboxes fails (simulate: block
  `/api/v1/me/document-mentions` at the network layer)
- **Then** the page says the list is incomplete and does **not** say
  «No mentions yet». This is the negative control for the whole fix: an
  implementation that swallowed the failed request would pass runs 1-5 and
  rebuild the original defect one layer down.

### Every run

- No `console.error` / `pageerror` (F1 fixture)
- No HTTP 5xx (F1 fixture)
- Anything the run created — document, comments, task comment — is removed at the
  end, pass or fail

## Deep-verify notes (§1c)

- **The assertion is which thread is focused**, not that the rail rendered. The
  rail is open by default (`docs-layout-storage.ts:83`), so "the rail is visible"
  passes without the feature existing. The jsdom test for this collapses the rail
  first for exactly that reason; the browser run should too.
- **HTTP 200 proves nothing.** Both endpoints answered 200 throughout the
  original defect — one of them was simply never called.
- **Run 6 is not optional.** Without it, "the tab shows mentions" is satisfied by
  any code path that happens to have data, including one that would go silent
  again the next time a source errors.

## Sequencing — why the spec is not in the same PR as the feature

`Authed E2E` in `ci.yml` runs `npx playwright test` against **`APP_BASE_URL`,
which defaults to production**. A spec for a feature that is not deployed yet
fails on its own PR and blocks the merge that would deploy it. Same order as
`docs-link-from-task.md` and `docs-paragraph-link.md`: feature merges and
deploys → spec follows, green against a prod that has the feature.

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts`
- Creates a run-unique document title so two concurrent runs cannot match each
  other's rows
- Deletes the document and the task comment in teardown

## Out of scope

- Email / push / Telegram delivery of the same mention — separate channels with
  their own preference rows; this scenario is about the two in-app surfaces that
  contradicted each other.
- Merging the two inboxes server-side into one `/me/mentions` response. That was
  considered and declined: it needs a nullable `task_id` on a shape three screens
  read as non-null. The merge is on the client, and this scenario is what proves
  the client did it.
- Mobile viewport (separate visual scenario per §1k).
