# Scenario: Docs — find a document by what is written inside it, in the scope you choose

**Feature:** Docs — content search with scope (epic Docs, unit D9)
**Scenario ID:** docs-search-scope
**Owner:** Garfield
**Created:** 2026-08-20
**Spec file:** `web/e2e/docs-search-scope.spec.ts` — **follows the deploy, see "Sequencing"**

---

## Given

- The user is logged in via Casdoor as an agent user with access to at least one project
- That project has a document whose **title does not contain** the search phrase and whose **body does**
- That project has a task

## When / Then

### 1. Find a document by its content

- **When** the user types `[[` and a phrase that appears only inside a document's body
- **Then** that document appears in the menu — this is the criterion the previous unit could not meet, since it matched titles only
- **Then** the row shows the **fragment containing the phrase**, with the phrase marked
- **When** the user picks it → **then** a link to that document is inserted, exactly as in D8

### 2. A snippet that is not the match says so

- **Given** a document whose match lies past the window the server quotes from (a very long body)
- **Then** the row shows the document's opening, rendered as a **preview** (`[data-snippet="preview"]`), not as a match (`[data-snippet="match"]`)
- Presenting the first sentence of a long document as the reason it matched is a small lie that makes the whole result list untrustworthy, so the two are visually and structurally distinct

### 3. The scope switcher

- **Given** a project **without** a Team Relay integration → **then** no scope control is shown at all. A switcher with one option is a control that cannot do anything.
- **Given** a project **with** one → **then** `Docs` and `Team Relay` are offered, `Docs` selected
- **When** the user switches to `Team Relay` → **then** the results come from the Team Relay vault
- **When** they pick a Team Relay result → **then** what is inserted is its `relay://` URL, **not** a `/w/…/docs/…` route: that document has no page in this app, and a link to one would go nowhere

### 4. Tenancy — the negative control

- **When** the search endpoint is called with a project id belonging to **another workspace** (directly, via the API)
- **Then** the response contains **no documents**, even for a query that matches them
- Asserted against a document that provably matches the query when the scope is removed, so the control cannot pass by the query simply finding nothing

### Every run

- No `console.error` / `pageerror` (F1 fixture)
- No HTTP 5xx (F1 fixture)
- Anything the run created is removed at the end, pass or fail

## Deep-verify notes (§1c)

- The load-bearing assertion in run 1 is that a document whose **title lacks the phrase** is returned. A fixture whose title also contains it would pass on the old title-only matcher and prove nothing.
- Run 4 must assert against a document that **does** match the query in its own tenant. "Zero results" is otherwise satisfied by a query that matches nothing anywhere.
- The match markers are private-use codepoints (U+E000/U+E001) written by the API and stripped by the client. A test asserting on marker characters in the rendered page is asserting a bug: they must never reach the DOM.

## Sequencing — why the spec is not in the same PR as the feature

`Authed E2E` in `ci.yml` runs `npx playwright test` against **`APP_BASE_URL`, which defaults to production**. A spec for a feature that is not deployed yet fails on its own PR and blocks the merge that would deploy it. Same order as `docs-paragraph-link.md`, `docs-text-comments.md` and `docs-link-from-task.md`.

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts` (login once)
- Creates its own documents with run-unique phrases so concurrent runs cannot match each other
- Deletes them in teardown

## Out of scope

- Ranking quality beyond "the matching document is in the list"
- Stemming: the index uses the `simple` dictionary, so `runbooks` does not find `runbook`. That is a stated trade-off for mixed Russian/English bodies, not a defect to test around.
- Documents written before this unit shipped are searchable by title until their next save — there is no way to backfill them in SQL, because the text lives in object storage.
- Mobile viewport (separate visual scenario per §1k)
