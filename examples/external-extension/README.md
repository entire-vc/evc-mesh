# Reference external extension

A minimal external extension (`runtime: external`) for Entire VC Mesh. It shows
every call Mesh makes to an external service and how to answer it:

| Mesh calls | Path (manifest `external`) | What the service does |
|---|---|---|
| install handshake | `install_path` `/mesh/install` | stores the workspace signing secret; checks the install code |
| token delivery | `token_path` `/mesh/token` | stores the extension token, answers `{"ok":true}` |
| events | `events_path` `/mesh/events` | verifies the signature and the timestamp, skips a repeat of a body already processed in this process within one hour (see "Deduplicate deliveries") |
| health | `health_path` `/healthz` | answers 200 |
| screens | `external.ui` `/mesh/ui/hello`, `/mesh/ui/task` | verifies the 60-second context token, renders a page |

And one call back: on the first event it sees for a task (picked from `task_id` in the signed body, not from the unsigned event header) it posts a comment with its extension
token (`POST /api/v1/tasks/{id}/comments`, capability `comments:write`).

The code is in [`refext/`](./refext) and uses the Go standard library plus
`golang-jwt`. It does not import Mesh: it is written from the public documents
([webhooks](../../docs/webhooks.md), [iframe screens](../../docs/extensions/ui-iframe.md),
[extension actor contract](../../docs/extensions/extension-actor-contract.md)).
Mesh's own test suite drives it with the real Mesh senders, so the example is
guaranteed to match what Mesh sends:

```bash
go test ./internal/... -run RefExt        # Mesh senders against this extension
go test ./examples/external-extension/... # the extension's own rules
```

## Run it

```bash
export MESH_URL=https://mesh.example.com   # your Mesh origin
export INSTALL_CODE=BIND-...               # required, see below
go run ./examples/external-extension       # listens on :8090 (LISTEN_ADDR)
```

The service must be reachable from Mesh over `https` (plain `http` only for
`localhost`) on a host of its own, not under the Mesh domain.

1. Install the extension on the Mesh instance from a repository holding
   [`mesh-extension.json`](./mesh-extension.json) at its root (instance admin,
   `POST /api/v1/extensions/install` with `{"source":"repo","repo_url":…,"ref":…}`,
   see [the extensions API](../../docs/extensions/api.md)).
2. Connect it in a workspace and set **Service address** (`service_url`) to the
   address of this service. Mesh performs the install handshake, subscribes the
   events the manifest lists and delivers the extension token.
3. Create a task: the service comments on it. Open the **Reference** tab of the
   task or the sidebar item to see the screens.

## What to keep when you build your own

- **Verify every request.** `X-Mesh-Signature` is hex HMAC-SHA256 of
  `"{X-Mesh-Timestamp}.{raw body}"` with the workspace signing secret. Refuse a
  timestamp more than a few minutes off. Pick the secret by `workspace_id` from
  the body, and trust nothing else in the body before the signature checks out.
- **Bind with an install code.** The install request is signed with the secret
  it carries, which proves the body is whole, not who sent it. Issue a one-time
  code to the workspace admin; they store it in Mesh as the workspace secret
  `EXT_<ID>_INSTALL_CODE` (`EXT_REFERENCE_EXT_INSTALL_CODE` here), and Mesh sends
  it in `install_code`. A code binds once and is then spent, so a leaked used
  code binds nothing; rotating the signing secret takes a fresh code (Mesh
  resends the code it holds on every handshake, and a new code makes it repeat
  the handshake). Without a code (the library allows it, `main.go` does not)
  the first install binds the workspace for good and its secret can never be
  rotated: anyone could otherwise re-key it, since the install signature only
  proves the body is whole.
- **Deduplicate deliveries.** A retry of one delivery carries the same body
  (only the timestamp is re-signed). `X-Mesh-Delivery` is not covered by the v1
  signature, so a replayed request could carry a fresh one: do not trust it as
  a key. This example keys on the `event_id` in the verified signed body, which is
  stable across retries and differs between events, so events that are equal in
  everything else are processed separately. A body without `event_id` (an older
  Mesh) falls back to the SHA-256 of the whole body. That fallback is a limit of
  the example, not an exactly-once guarantee: byte-identical bodies of one
  workspace are collapsed into one within `seenFor` (one hour), so it does not
  suit a handler that must tell such events apart. Acknowledge a repeat
  with 2xx and do not run it again. Reserve the key before you start, so a copy
  arriving while the first is still running is refused with a non-2xx (409 here)
  and comes back later. Answer 5xx only when you want the delivery retried
  (Mesh retries after 1, 5 and 25 seconds).
- **Headers are not signed.** The v1 signature covers the timestamp and the
  body only. `X-Mesh-Event` and `X-Mesh-Delivery` can be altered on a replayed
  request, so treat them as hints: this example only accepts a bounded token,
  logs them quoted, and takes the dedupe key and the side effect's target from the signed body. A handler
  with a side effect should confirm from the signed body what it is acting on.
- **Store state durably.** This example keeps everything in memory: a restart
  forgets the bound workspaces (install again) and the hashes of processed
  bodies, so a retry that arrives after a restart runs again. Keep the signing
  secret, the token and the dedupe keys in a database, and record a key in the
  same transaction as the work it caused.
- **Keep the token server-side.** The extension token (`ext_…`) never goes to a
  browser. A screen gets only the short-lived context token, and asks for a new
  one with `mesh.ready` before it expires.
- **Ask only for what you call.** Every API route you call must be listed for
  extensions and covered by a capability in `capabilities.api`; anything else is
  a 403 and counts as a violation on the connection.
