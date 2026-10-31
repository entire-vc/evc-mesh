# Iframe screens of external extensions

An external extension (`runtime: external`) can show its own screens inside Mesh:

| Slot | Where | Size |
|---|---|---|
| `sidebar.item` | a navigation item in the sidebar, opens `/w/<workspace>/x/<extension id>` | the whole content area |
| `task.panel` | a tab in the task view | the tab body |
| `project.view` | a tab of the project strip, opens `/w/<workspace>/p/<project>/x/<extension id>` | the whole content area |

The screen is a page of **your service**. Mesh puts it in an `<iframe>` and tells it who opened it with a short-lived signed token. Mesh never sends your extension's own `ext_` token to the browser.

## Manifest

```json
{
  "levels": { "instance": false, "workspace": true, "project": true },
  "capabilities": {
    "ui": ["sidebar.item", "task.panel", "project.view"],
    "network": { "allowed_domains": ["hello.example.com"] }
  },
  "external": {
    "base_url_setting": "hello_url",
    "health_path": "/healthz",
    "install_path": "/mesh/install",
    "events_path": "/mesh/events",
    "ui": {
      "sidebar.item": { "path": "/mesh/ui/hello", "title": "Hello" },
      "task.panel":   { "path": "/mesh/ui/task",  "title": "Hello" },
      "project.view": { "path": "/mesh/ui/project", "title": "Hello" }
    }
  },
  "project_view": { "icon": "Puzzle", "title": "Hello" }
}
```

A slot shows only if it is listed in `capabilities.ui` **and** has an entry in `external.ui`. The address of a screen is the workspace's `base_url_setting` value plus `path`.

`project.view` also needs the `project_view` block and `levels.project: true` (the manifest schema enforces both), and its tab shows only in projects where the extension is enabled. A per-project extension without a `project.view` screen gets a project tab with its `sidebar.item` screen instead; that token carries the `project_id` and takes the same checks as `project.view` (the extension is enabled in the project, `409` otherwise).

Only these three slots take an iframe screen. Any other key in `external.ui` (for example `task.badge`, `task.actions`, `project.panel`) is accepted in the manifest but **ignored**: nothing is rendered for it, and `ui-context` refuses a token for it with `400` (`slot must be sidebar.item, task.panel or project.view`). New iframe slots will be listed here when they exist.

Rules Mesh enforces before it opens a frame:

- the service address is `https` (plain `http` only for `localhost`);
- its host is listed in `capabilities.network.allowed_domains` (an exact host, `*.example.com` for subdomains, or `{settings.<key>}` for the host the workspace entered in that setting; an empty setting allows nothing). Otherwise the screen is not opened (`409`);
- the extension is connected and enabled in the workspace;
- the person is a member of the workspace;
- for `project.view`, and for `sidebar.item` whenever its token names a project (`project_id`, or the project of the `task_id`): the project is in that workspace and the extension is enabled in it (`409` otherwise).

The iframe is created with `sandbox="allow-scripts allow-forms allow-same-origin"`, `referrerpolicy="no-referrer"` and no `allow` permissions. It runs on your origin, so cookies and storage are yours.

Because scripts and same-origin are both allowed, the service must live on its own host: Mesh refuses to issue a context (`409`) when the service host equals a host Mesh itself is served from (the request host, `MESH_BASE_URL`, a CORS origin). Never put a screen under the Mesh domain.

Your service should answer with `Content-Security-Policy: frame-ancestors <your Mesh origin>` so the page cannot be framed anywhere else.

## Identity token

Mesh opens the frame at `<screen address>?token=<JWT>`. The token is signed with the instance key (ES256) and lives **60 seconds**.

| Claim | Meaning |
|---|---|
| `iss` | public origin of this Mesh instance |
| `aud` | your extension id |
| `sub` | Mesh user id |
| `email` | the user's email |
| `workspace_id` | workspace the screen was opened in |
| `project_id` | project, for `task.panel` and `project.view` (omitted otherwise) |
| `task_id` | task, for `task.panel` (omitted otherwise) |
| `roles` | the user's workspace role, e.g. `["admin"]` |
| `iat`, `nbf`, `exp`, `jti` | standard; `exp - iat ≤ 60` |

Verify it on every request to a screen:

1. Fetch the key set from `GET <Mesh origin>/api/v1/.well-known/jwks.json` (public, no auth). Cache it for a few minutes; refetch when a token carries an unknown `kid`.
2. Pick the key by the token's `kid` header; accept only `ES256`.
3. Check the signature, `iss` equals your Mesh origin, `aud` equals your extension id, and `exp` has not passed. Answer `401` otherwise.

Keys rotate (every 30 days) with an overlap of 24 hours: the old key stays in the set, so a token signed just before a rotation still verifies.

## postMessage protocol

All messages are plain objects with a `type`. Version: `1`. The lifecycle messages below (`mesh.context.error`, `mesh.revoked`, `mesh.app-ready`, `mesh.app-error`) were added to version 1 without changing it: a screen that ignores unknown types keeps working, and Mesh ignores nothing a v1 screen used to send.

Mesh → screen (to your origin only):

```json
{ "type": "mesh.context", "version": 1, "token": "<JWT>", "expires_at": "2026-10-17T12:01:00Z",
  "workspace_id": "…", "workspace_slug": "acme", "project_id": null, "task_id": null, "locale": "en",
  "path": "/memory/search#recent" }
```

Sent when the frame has loaded, again on every `mesh.ready`, and on its own when Mesh renews the token: at about 80% of its lifetime (≈48 s for a 60 s token), so a screen that keeps `token` from the latest `mesh.context` always holds a valid one without asking. It is also sent when the Mesh address moves to another section without the screen asking (back/forward, a pasted link): then go to `path`. `path` is the section (see below), `""` for the start of the screen.

```json
{ "type": "mesh.context.error", "version": 1, "status": 409, "reason": "conflict" }
```

Sent when a renewal (proactive or on `mesh.ready`) is refused. Keep using the last token until it expires; Mesh tries again.

| `status` | `reason` | Meaning |
|---|---|---|
| `401` | `unauthorized` | the person's Mesh session has ended |
| `403` | `forbidden` | the person is no longer a member of the workspace |
| `404` | `not_found` | the extension, the screen or the task is gone |
| `409` | `conflict` | the extension was switched off, or its host is no longer allowed |
| `5xx` | `unavailable` | Mesh could not issue a token right now |
| `null` | `network` | the request did not get an answer |

```json
{ "type": "mesh.revoked", "version": 1, "reason": "conflict" }
```

Sent right after a `mesh.context.error` with status `403`, `404` or `409`: access to the screen is gone. Mesh then removes the frame and shows "switched off or access removed" with a retry button. Stop work and drop the token on receipt; delivery is best effort because the frame is closed right after, so the server side must not depend on it (it also stops getting valid tokens). A switched-off extension is noticed at the next renewal, within about 50 seconds.

Screen → Mesh (`window.parent.postMessage(msg, "<Mesh origin>")`):

| Message | Effect |
|---|---|
| `{ "type": "mesh.ready" }` | Mesh replies with a fresh `mesh.context` (use it to renew the token after 60 s) |
| `{ "type": "mesh.resize", "height": 480 }` | `task.panel`: minimum height in px, clamped to 120–4000. Ignored for `sidebar.item` |
| `{ "type": "mesh.navigate", "path": "/w/acme/dashboard" }` | Opens a place **inside Mesh**. Only a path starting with a single `/`; anything else is dropped |
| `{ "type": "mesh.toast", "level": "success", "message": "Saved" }` | Shows a toast. `level`: `info` (default), `success`, `warning`, `error`; text up to 300 characters |
| `{ "type": "mesh.state", "path": "/memory/search#recent", "replace": false }` | The screen moved to a section: Mesh puts it into its own address, a new history entry (`replace: true` replaces the current one). The frame is not reloaded |
| `{ "type": "mesh.app-ready" }` | Optional. The screen has finished its own start-up. Mesh records it; it changes nothing on screen and is not required |
| `{ "type": "mesh.app-error", "message": "Backend is down" }` | Optional. The screen cannot work. Mesh replaces the frame with "<name> reported an error on this screen", your `message` (plain text, up to 300 characters, may be empty) and a retry button that opens the screen anew |

Mesh acts on a message only if it came from the iframe's own window **and** its `origin` is exactly the origin of your service. Anything else is ignored silently. Unknown types and malformed payloads are ignored too.

## Sections (deep links)

Everything after the extension id in the Mesh address, plus the hash, is the **section** of the screen:

```
/w/acme/p/core/x/mem/memory/search#recent   →   https://mem.example.com/mesh/ui/project/memory/search?token=<JWT>#recent
```

The frame opens there (the section is appended to the screen `path`, the token stays in the query), and `mesh.context` carries it as `path`. Send `mesh.state` whenever the user moves between sections, so a reload or a copied link reopens the same place. Mesh does not echo a `mesh.state` back.

A section is `/segment/segment…` with an optional `#hash`, at most 512 characters: path characters of RFC 3986 (the hash too) and well-formed percent-escapes only (`%` plus two hex digits), no query (`?`, also in the hash), no `//`, no `.`/`..` segment (also percent-encoded), no encoded `/` or `\` (`%2F`, `%5C`, also in the hash), no backslash, spaces or control characters. A trailing `/` is dropped. Anything else is ignored: a `mesh.state` that is not a section is dropped, and such an address opens the start of the screen. Serve every section path under the screen `path` (a single-page app answers its index for all of them).

## States

- While the token is requested and the page loads, Mesh shows a placeholder.
- If the server refuses a token (extension off, host not allowed, not a member) or the page does not load within 10 seconds, Mesh shows "unavailable" with a retry button. The task view and the sidebar keep working.
- If a later renewal is refused with `403`/`404`/`409`, Mesh sends `mesh.context.error` and `mesh.revoked` and shows "switched off or access removed" with a retry button. Other refusals only send `mesh.context.error`; the frame stays.
- If the screen sends `mesh.app-error`, Mesh shows the screen's error with a retry button.

### Limits of v1

- A finished `load` of the iframe is **not** proof of a successful response. The browser fires `load` for a rendered HTTP error page too, and Mesh cannot read the status of a cross-origin response, so a 404 or 500 page inside the frame is shown as is and is not detected. Make sure the screen path in the manifest exists.
- An HTTP error page is still not detected by itself; a screen that knows it failed should say so with `mesh.app-error`.
- `mesh.ready` is **optional**. A frame that never sends it is a valid frame: it still receives `mesh.context` on load and the token in `?token=`. The message is reserved for a stricter handshake in a later version of the protocol; do not rely on Mesh treating a silent frame as unavailable, or on it treating a `mesh.ready` as a health signal.

## Instance key

The key pair is created on first use and stored encrypted with the instance encryption key (`MESH_INTEGRATION_ENCRYPTION_KEY`). Without that key the instance cannot issue tokens: `ui-context` and the JWKS answer `503`.

## API

`POST /api/v1/ext/{extension_id}/ui-context` (signed-in user)

```json
{ "workspace_id": "…", "slot": "sidebar.item | task.panel | project.view", "task_id": "…", "project_id": "…" }
```

`task_id` and `project_id` are optional; a task of another workspace answers `404`. For `project.view`, `project_id` is required and `task_id` is refused (`400`). Response `200`:

```json
{ "token": "<JWT>", "expires_at": "…", "frame_url": "https://hello.example.com/mesh/ui/hello", "origin": "https://hello.example.com", "title": "Hello" }
```

`GET /api/v1/.well-known/jwks.json` — the public key set (`application/jwk-set+json`).
