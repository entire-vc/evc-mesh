# Scenario: Docs — copy a link to one paragraph, and follow it after edits

**Feature:** Docs — paragraph anchors (epic Docs, unit D6)
**Scenario ID:** docs-paragraph-link
**Owner:** Garfield
**Created:** 2026-08-19
**Spec file:** `web/e2e/docs-paragraph-link.spec.ts` — **lands in a follow-up PR, see "Sequencing" below**

---

## Given

- The user is logged in via Casdoor as an agent user with access to at least one project
- The project's Docs tab is reachable at `/w/:wsSlug/p/:projectSlug/docs`
- A document exists whose body has at least three distinguishable paragraphs

## When / Then — four runs, because the point of the unit is what happens after an edit

### 1. Copy and follow, document unchanged

- **When** the reader right-clicks the second paragraph in the viewer and picks
  *Copy link to this paragraph*, then opens the copied URL
- **Then** the URL is `/w/:ws/p/:project/docs/:docId#p=<...>` — the anchor is in
  the hash, so nothing about it reaches a server
- **Then** the element carrying `.mesh-doc-anchor-hit` is the paragraph the link
  was made from — asserted by its **text**, not by the class existing somewhere
- **Then** no notice is shown: nothing has changed, so there is nothing to say

### 2. The document is edited ABOVE the anchor — the link must still work

- **When** a new paragraph is inserted above the anchored one and the document is saved
- **Then** opening the same link still highlights the paragraph with the **same text**
- **Then** still no notice: the paragraph is intact, it only moved
- This is the criterion an ordinal or byte-offset anchor fails, and fails *silently*

### 3. The ANCHORED paragraph itself is edited — the outcome must be stated

- **When** the anchored paragraph's text is rewritten and the document is saved
- **Then** opening the same link highlights **the same place** (the neighbouring
  paragraphs still identify it)
- **Then** the notice *"This paragraph has been edited since the link was created…"*
  is visible. A silent jump to changed text is the failure this unit exists to prevent

### 4. The anchored paragraph is gone — say so, scroll nowhere

- **When** the anchored paragraph is deleted and its neighbours are edited too
- **Then** no element carries `.mesh-doc-anchor-hit` — nothing is highlighted
- **Then** the notice *"The linked paragraph is no longer in this document…"* is visible

### Every run

- No `console.error` / `pageerror` (F1 fixture)
- No HTTP 5xx (F1 fixture)
- The document created by the run is deleted at the end, pass or fail

## Deep-verify notes (§1c)

- HTTP 200 on the document URL proves nothing here: the anchor is a hash, so the
  server returns the identical SPA shell for a working link and a broken one.
- The behaviour assert is on **which element is highlighted and what its text is**.
  Asserting that `.mesh-doc-anchor-hit` merely exists would pass on the off-by-one
  bug this whole scheme has to avoid — landing the reader one paragraph away.
- Likewise, asserting the notice is *absent* in runs 1-2 is as load-bearing as
  asserting it is *present* in runs 3-4: a component that always warns would
  otherwise pass half the scenario.

## Sequencing — why the spec is not in the same PR as the feature

`Authed E2E` in `ci.yml` runs `npx playwright test` against **`APP_BASE_URL`,
which defaults to production**. A spec for a feature that is not deployed yet
therefore fails on its own PR and blocks the merge that would deploy it. So the
order is: feature PR merges and deploys → spec PR follows, green against a prod
that now has the feature.

This applies to every user-facing spec in this repository, not just this one. The
alternative — a spec that skips itself when the feature is missing — is worse: it
is a gate that reports green for "not measured", which is the failure mode the F1
harness exists to eliminate.

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts` (login once)
- Creates its own document, with a run-unique marker in the paragraph text so two
  concurrent runs cannot resolve each other's anchors
- Deletes that document in teardown

## Out of scope

- Anchors inside the editor (the menu is offered in the viewer only — in edit mode
  the browser's own context menu is what a writer needs)
- Comment ranges over a text selection — that is D7, which reuses this anchor shape
- Mobile viewport (separate visual scenario per §1k)
