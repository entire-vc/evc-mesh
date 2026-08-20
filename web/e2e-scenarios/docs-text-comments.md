# Scenario: Docs — comment on a passage, resolve it, and survive an edit around it

**Feature:** Docs — inline comments on document text (epic Docs, unit D7)
**Scenario ID:** docs-text-comments
**Owner:** Bill
**Created:** 2026-08-20
**Spec file:** `web/e2e/docs-text-comments.spec.ts` — **lands in a follow-up PR, see "Sequencing" below**

---

## Given

- The user is signed in through the app's own login (`POST /api/v1/auth/login`) —
  **not** Casdoor. Mesh has no OAuth callback route; a scenario written against a
  Casdoor flow here does not fail, it silently runs signed-out.
- The project's Docs tab is reachable at `/w/:wsSlug/p/:projectSlug/docs`
- A throwaway document exists whose body contains **Cyrillic** text and at least
  one passage wrapped in inline markup (`**bold**` inside the sentence)

### Why Cyrillic is a Given and not a detail

`anchor.start` / `anchor.end` are UTF-8 **byte** offsets into the markdown; the
browser counts UTF-16 code units. On ASCII the two numbers are equal, so a
scenario written on English text passes against a client that never converts
between them and against one that does. It discriminates nothing. Every run
below is on Cyrillic for that reason, and a future edit that "simplifies" the
fixture to English removes the only thing separating a correct client from a
broken one.

The inline markup matters for the second reason: the quote comes from the
*rendered* text, so a selection crossing `**...**` produces a quote that is not
a substring of the source. A scenario whose selection avoids markup never
exercises the markup-tolerant locator.

## When / Then — five runs

### 1. A comment is made on a selection

- **When** the reader selects a phrase in the viewer, the **Comment** affordance
  appears at the end of the selection, and they write a comment and submit
- **Then** the thread appears in the rail **and** in the tree under the document
  — both, because "the tree at the bottom shows the same thing" is the
  acceptance criterion, not a nicety
- **Then** the quoted phrase shown on the thread is **the phrase that was
  selected**, asserted by its text. A thread that renders with *some* quote is
  not evidence: the failure mode here is an anchor that stores a valid-looking
  offset pointing at different words
- **Then** the passage is highlighted in the document

⚠️ **The highlight lives in `CSS.highlights`, not in the DOM.** A selector assert
(`.mesh-doc-comment-highlight`) returns "no highlight" on **every** run,
including the correct ones. Assert through `CSS.highlights.get(...)` and the
range's text, or do not assert the highlight at all.

### 2. Resolve, then unresolve

- **When** the reader chooses **Resolve** on the thread
- **Then** the thread is marked resolved and disappears from the default view in
  **both** surfaces; the *Show resolved (1)* toggle appears
- **Then** the highlight is gone from the document — a resolved thread is not
  painted
- **When** the toggle is switched on and **Unresolve** is chosen
- **Then** the thread is back in the open list in both surfaces and the highlight
  returns

### 3. The comment is edited

- **When** the author edits their comment body and saves
- **Then** the new text is shown, marked `· edited`, in both surfaces
- **Then** the quote and the highlight are unchanged — editing what you said is
  not re-anchoring where you said it

### 4. NEGATIVE CONTROL — the document is edited AROUND the comment

The criterion the unit exists for. It is a negative control on the anchor, not a
smoke test.

- **When** a new paragraph is inserted **above** the commented passage and the
  document is saved
- **Then** the thread is still attached to **the same words** — asserted by
  reading the text the highlight range covers, **not** by re-reading the stored
  offset. Reading back the offset would pass against a client that never moved
  the anchor at all: it would return whatever now sits at that position
- **Then** no "no longer attached" notice is shown — nothing was lost

### 5. NEGATIVE CONTROL — the commented passage itself is deleted

- **When** the commented sentence is deleted and the document is saved
- **Then** the thread is still listed in both surfaces, carrying *"No longer
  attached — this text is not in the page any more"*
- **Then** **nothing is highlighted.** Silently sliding onto whatever text now
  occupies those offsets is the single failure this unit must not have, and it is
  the outcome a position-first anchor produces
- **Then** the thread is still usable — reply and resolve still work, or a
  detached thread would hang around forever with no way to close it

### Every run

- No `console.error` / `pageerror`
- No failed `/api/v1/*` request, no HTTP 5xx
- The document created by the run is deleted at the end, pass or fail — **and its
  comments are deleted BEFORE it.** Document deletion is soft, and the comment
  routes resolve the document first, so a comment outliving its document becomes
  unreachable through the API and can only be removed with a write to the
  database. Ten such rows already exist from earlier probe runs.

## Deep-verify notes (§1c)

- **HTTP 200 proves nothing here.** The page is an SPA shell: it returns 200 with
  the comment layer throwing inside it, with an empty rail, and with a thread
  anchored to the wrong words. Every assertion above is on rendered text or on a
  highlight range.
- **"A thread is visible" is not the assert.** The assert is *which words* it
  quotes and *which words* are highlighted. Run 4 and run 5 differ only in that,
  and a presence-only assert passes both while the feature is broken.
- **Both surfaces are asserted in every run**, not just the rail. They share one
  controller today; a future change that gives either its own copy of the state
  is exactly what these paired assertions catch.

## Sequencing

The spec lands **after** the reworked E2E harness (PR #643), not beside it. Until
that lands, `Authed E2E` runs signed-out: `global-setup` writes an empty storage
state when credentials are missing, so a scenario added now would report green
while asserting nothing — the failure this document is meant to prevent, in the
gate meant to prevent it.

Once #643 is in, the spec is written against its shape: one sign-in per run in a
shared context, and **no `storageState`** — refresh tokens rotate one-shot, and
replaying a saved one revokes every session the user has.
