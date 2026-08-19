# Scenario: Docs — comment on selected text, and what happens when the text changes

**Feature:** Docs — inline comments (epic Docs, unit D7)
**Scenario ID:** docs-text-comments
**Owner:** Garfield
**Created:** 2026-08-19
**Spec file:** `web/e2e/docs-text-comments.spec.ts` — **follows the deploy, see "Sequencing"**

---

## Given

- The user is logged in via Casdoor as an agent user with access to at least one project
- A document exists in that project with at least three distinguishable paragraphs

## When / Then

### 1. Comment on a selection

- **When** the reader selects a phrase in the viewer and clicks *Comment*, types a body and submits
- **Then** the thread appears in the list under the document, quoting **that phrase**
- **Then** the phrase is highlighted in the document body
- **Then** reloading the page shows the same thread against the same phrase — the anchor is stored, not held in memory

### 2. Reply, edit, resolve, reopen

- **When** the reader replies in the thread → **then** the reply appears under the root, and reloading keeps it there
- **When** the author edits their own comment → **then** the new text is shown; the **quote does not change** (editing a note does not move where it points)
- **When** the thread is resolved → **then** it leaves the open list, its highlight disappears, and it is reachable under *Show N resolved*
- **When** it is reopened → **then** it returns to the open list and is highlighted again

### 3. The text AROUND the comment is edited — the comment must survive

- **When** a paragraph is inserted above the commented phrase and the document is saved
- **Then** the thread still quotes the same phrase and the highlight is still **on that phrase**
- **Then** no "edited" or "no longer in this document" notice appears
- This is the criterion an offset-only anchor fails, and fails *silently*

### 4. The COMMENTED text itself is edited — say so, and stop highlighting

- **When** the commented phrase is rewritten and the document is saved
- **Then** the thread is still listed, still answerable and still resolvable
- **Then** it shows *"The commented text has been edited since."*
- **Then** **nothing is highlighted for that thread** — asserted by counting highlighted ranges, not by eye
- **Then** its quote is not a jump target (the control is disabled)

### 5. The commented text is deleted along with its surroundings

- **Then** the notice reads *"The commented text is no longer in this document."*
- **Then** again, no highlight, and no jump target

### Every run

- No `console.error` / `pageerror` (F1 fixture)
- No HTTP 5xx (F1 fixture)
- The document and its comments created by the run are deleted at the end, pass or fail

## Deep-verify notes (§1c)

- The load-bearing assertion in runs 3-5 is about **which text is highlighted**, not that a highlight exists. A thread that quietly moved onto neighbouring words would satisfy "a highlight exists" perfectly — that is the defect the anchor scheme exists to prevent, so the assert has to name the words.
- Asserting the **absence** of the notice in runs 1-3 is as load-bearing as its presence in 4-5: a component that always warns would otherwise pass half the scenario.
- Highlights are painted with the CSS Custom Highlight API, so they are **not in the DOM**. A Playwright assert must read them through `CSS.highlights` in page context (`Array.from(CSS.highlights.get('mesh-doc-comment') ?? []).map(r => r.toString())`), not by querying for elements. A selector-based assert would report "no highlight" in every run, including the ones where highlighting is correct — a check that always passes for the wrong reason.

## Sequencing — why the spec is not in the same PR as the feature

`Authed E2E` in `ci.yml` runs `npx playwright test` against **`APP_BASE_URL`, which defaults to production**. A spec for a feature that is not deployed yet fails on its own PR and blocks the merge that would deploy it. Same constraint, and the same order, as `docs-paragraph-link.md`: feature PR merges and deploys → spec PR follows, green against a prod that now has the feature.

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts` (login once)
- Creates its own document with run-unique phrases, so two concurrent runs cannot resolve each other's anchors
- Deletes that document in teardown

## Out of scope

- Comments on task descriptions (a different table and a different unit)
- Mobile viewport (separate visual scenario per §1k)
- Notifications for a comment or a reply (not part of D7)
