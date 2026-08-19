# Scenario: A Team Relay document opens inside Docs, in our editor

**Feature:** Docs — Team Relay reintegration (epic Docs, unit D10)
**Scenario ID:** docs-relay-in-our-editor
**Owner:** Garfield
**Created:** 2026-08-20
**Spec file:** `web/e2e/docs-relay-in-our-editor.spec.ts` — **follows the deploy, see "Sequencing"**

---

## Given

- The user is logged in via Casdoor as an agent user
- A project with a **connected Team Relay integration** and a share containing at least one markdown document
- A task in that project whose description contains a `relay://<slug>/<path>.md` link

## When / Then

### 1. The document is rendered by our editor, not embedded

- **When** the task is opened
- **Then** the linked document's **text** is on the page — the actual sentences from the Team Relay note, not a screenshot of them
- **Then** there is **no `<iframe>` anywhere on the page**
- **Then** the block has no fixed 256px viewport: a long document is as tall as its content, inside the card's own scroll area

### 2. The old links still work

- **Given** a `relay://` link written months ago, in prose, in a task description or a comment
- **Then** it renders as the document. Nothing was migrated and nothing needed to be: the links are a pseudo-scheme in free text with no entity behind them, and only what is *drawn* changed.

### 3. Degrading is honest, and always leaves a way out

For each of: the project has no Team Relay; the link points at **another** share; the link points at a folder; the relay is unreachable —

- **Then** no document is shown, and the **"Open in Team Relay" link is present and correct**
- **Then** no error dialog, no spinner that never ends, and no "Preview not available" dead end

### 4. The integration key never reaches the browser — the negative control

- **When** the page is loaded with the network log captured
- **Then** no request from the browser goes to the Team Relay host at all: the document arrives from `"/api/v1/projects/:id/tr/document"`, which is this app's own origin
- **Then** no response body, and no URL in the log, contains `tr_agent_` or `agent_key=`
- The key is long-lived and does not rotate, so this is the assertion that must never be relaxed

## Deep-verify notes (§1c)

- "The card renders" is not the criterion. **The document's own words must be on the page** — assert on a sentence from the note, because an empty card and a working one look identical to a presence check.
- Asserting **absence of an iframe** is the whole point of the unit and must be checked on both the success and the failure branch. "We deleted the component" and "nothing renders an iframe any more" are different claims.
- The key assertion is over the **whole** captured traffic, not over named fields. A credential leaks through whichever field is added next.

## Sequencing — why the spec is not in the same PR as the feature

`Authed E2E` in `ci.yml` runs `npx playwright test` against **`APP_BASE_URL`, which defaults to production**. A spec for a feature that is not deployed yet fails on its own PR and blocks the merge that would deploy it. Same order as the other Docs scenarios.

## Test data

- Uses the Casdoor `storageState` from `global-setup.ts`
- Needs a project with a real Team Relay integration; if none is configured in the target environment the spec must **skip loudly**, naming what is missing — never pass quietly for lack of a fixture

## Out of scope

- Editing a Team Relay document from Mesh. The API returns a snapshot pushed by the Obsidian plugin, not a live CRDT; two-way editing is a different contract (Yjs over CWT) and a different unit.
- Obsidian-flavored syntax that our editor does not implement (`![[wikilinks]]`, `==highlight==`) renders as literal text. Stated, not tested around.
- Unpublished shares: the read endpoint that serves this takes a slug and serves published shares. An unpublished share needs a read-scoped key and a different endpoint.
