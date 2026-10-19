# Iframe screens of external extensions

An external extension (`runtime: external`) can show its own screens inside Mesh:

| Slot | Where | Size |
|---|---|---|
| `sidebar.item` | a navigation item in the sidebar, opens `/w/<workspace>/x/<extension id>` | the whole content area |
| `task.panel` | a tab in the task view | the tab body |

The screen is a page of **your service**. Mesh puts it in an `<iframe>` and tells it who opened it with a short-lived signed token. Mesh never sends your extension's own `ext_` token to the browser.

## Manifest

```json
{
  "capabilities": {
    "ui": ["sidebar.item", "task.panel"],
    "network": { "allowed_domains": ["hello.example.com"] }
  },
  "external": {
    "base_url_setting": "hello_url",
    "health_path": "/healthz",
    "install_path": "/mesh/install",
    "events_path": "/mesh/events",
    "ui": {
      "sidebar.item": { "path": "/mesh/ui/hello", "title": "Hello" },
      "task.panel":   { "path": "/mesh/ui/task",  "title": "Hello" }
    }
  }
}
```

A slot shows only if it is listed in `capabilities.ui` **and** has an entry in `external.ui`. The address of a screen is the workspace's `base_url_setting` value plus `path`.

Rules Mesh enforces before it opens a frame:

- the service address is `https` (plain `http` only for `localhost`);
- its host is listed in `capabilities.network.allowed_domains` (an exact host, or `*.example.com` for subdomains). Otherwise the screen is not opened (`409`);
- the extension is connected and enabled in the workspace;
- the person is a member of the workspace.

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
| `project_id` | project, for `task.panel` (omitted otherwise) |
| `task_id` | task, for `task.panel` (omitted otherwise) |
| `roles` | the user's workspace role, e.g. `["admin"]` |
| `iat`, `nbf`, `exp`, `jti` | standard; `exp - iat ≤ 60` |

Verify it on every request to a screen:

1. Fetch the key set from `GET <Mesh origin>/api/v1/.well-known/jwks.json` (public, no auth). Cache it for a few minutes; refetch when a token carries an unknown `kid`.
2. Pick the key by the token's `kid` header; accept only `ES256`.
3. Check the signature, `iss` equals your Mesh origin, `aud` equals your extension id, and `exp` has not passed. Answer `401` otherwise.

Keys rotate (every 30 days) with an overlap of 24 hours: the old key stays in the set, so a token signed just before a rotation still verifies.

## postMessage protocol

All messages are plain objects with a `type`. Version: `1`.

Mesh → screen (to your origin only):

```json
{ "type": "mesh.context", "version": 1, "token": "<JWT>", "expires_at": "2026-10-17T12:01:00Z",
  "workspace_id": "…", "workspace_slug": "acme", "project_id": null, "task_id": null, "locale": "en" }
```

Sent when the frame has loaded and again on every `mesh.ready`.

Screen → Mesh (`window.parent.postMessage(msg, "<Mesh origin>")`):

| Message | Effect |
|---|---|
| `{ "type": "mesh.ready" }` | Mesh replies with a fresh `mesh.context` (use it to renew the token after 60 s) |
| `{ "type": "mesh.resize", "height": 480 }` | `task.panel`: minimum height in px, clamped to 120–4000. Ignored for `sidebar.item` |
| `{ "type": "mesh.navigate", "path": "/w/acme/dashboard" }` | Opens a place **inside Mesh**. Only a path starting with a single `/`; anything else is dropped |
| `{ "type": "mesh.toast", "level": "success", "message": "Saved" }` | Shows a toast. `level`: `info` (default), `success`, `warning`, `error`; text up to 300 characters |

Mesh acts on a message only if it came from the iframe's own window **and** its `origin` is exactly the origin of your service. Anything else is ignored silently. Unknown types and malformed payloads are ignored too.

## States

- While the token is requested and the page loads, Mesh shows a placeholder.
- If the server refuses a token (extension off, host not allowed, not a member) or the page does not load within 10 seconds, Mesh shows "unavailable" with a retry button. The task view and the sidebar keep working.

### Limits of v1

- A finished `load` of the iframe is **not** proof of a successful response. The browser fires `load` for a rendered HTTP error page too, and Mesh cannot read the status of a cross-origin response, so a 404 or 500 page inside the frame is shown as is and is not detected. Make sure the screen path in the manifest exists.
- `mesh.ready` is **optional**. A frame that never sends it is a valid frame: it still receives `mesh.context` on load and the token in `?token=`. The message is reserved for a stricter handshake in a later version of the protocol; do not rely on Mesh treating a silent frame as unavailable, or on it treating a `mesh.ready` as a health signal.

## Instance key

The key pair is created on first use and stored encrypted with the instance encryption key (`MESH_INTEGRATION_ENCRYPTION_KEY`). Without that key the instance cannot issue tokens: `ui-context` and the JWKS answer `503`.

## API

`POST /api/v1/ext/{extension_id}/ui-context` (signed-in user)

```json
{ "workspace_id": "…", "slot": "sidebar.item | task.panel", "task_id": "…", "project_id": "…" }
```

`task_id` and `project_id` are optional; a task of another workspace answers `404`. Response `200`:

```json
{ "token": "<JWT>", "expires_at": "…", "frame_url": "https://hello.example.com/mesh/ui/hello", "origin": "https://hello.example.com", "title": "Hello" }
```

`GET /api/v1/.well-known/jwks.json` — the public key set (`application/jwk-set+json`).
