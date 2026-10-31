# Webhooks

Mesh can send HTTP callbacks to external services when events occur. Webhooks use HMAC-SHA256 signatures for payload verification.

> **Looking for the inbound GitHub receiver** (GitHub → Mesh, auto-moves a task
> when its PR merges)? That is a different surface — see
> [`integrations/github-webhook.md`](./integrations/github-webhook.md). This file
> covers **outbound** webhooks (Mesh → your service).

## Overview

Webhooks are configured per workspace and deliver events to a specified URL. Each webhook can subscribe to specific event types or receive all events.

## Creating a Webhook

### Via REST API

```bash
curl -X POST http://localhost:8005/api/v1/workspaces/{ws_id}/webhooks \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://your-service.example.com/hooks/mesh",
    "secret": "your-webhook-secret",
    "events": ["task.created", "task.status_changed", "task.assigned"],
    "active": true
  }'
```

### Via the Web UI

1. Navigate to **Workspace Settings > Integrations**
2. Click **Add Webhook**
3. Enter the URL, select events, and save
4. Copy the generated secret for signature verification

## Event Types

| Event | When Fired |
|-------|-----------|
| `task.created` | A new task is created |
| `task.updated` | A task's fields are modified |
| `task.deleted` | A task is deleted |
| `task.status_changed` | A task is moved to a different status |
| `task.assigned` | A task is assigned or reassigned |
| `comment.created` | A new comment is added |
| `project.created` | A new project is created |
| `project.updated` | A project is modified |

## Payload Format

Mesh sends `POST` requests with a JSON object body: the event's own fields plus
`event_id`. Example for `task.status_changed`: Extension subscriptions also get `workspace_id`.

```json
{
  "event_id": "0192f5c1-7a3e-7b44-9c1d-5e2f8a6b4c3d",
  "workspace_id": "550e8400-...",
  "task_id": "7c9e6679-...",
  "project_id": "6ba7b810-...",
  "old_status_id": "1f0e2d3c-...",
  "new_status_id": "4b5a6978-..."
}
```

### Event id

`event_id` (UUID v7, time-ordered) names one event. It is minted once when the
event happens and is the same:

- on every retry of a delivery;
- for every subscriber of the event (each webhook and each extension
  subscription gets the same value);
- on every transport: the `event_id` body field, the `X-Mesh-Event-Id` header
  and the `event_id` field of the NATS envelope (`delivery=nats`).

Deduplicate on `event_id`: a receiver that already processed it should answer
`2xx` and do nothing. `X-Mesh-Delivery` names one delivery to one endpoint (it
is what Mesh logs in the delivery history); `event_id` is the stable one.

## Headers

Every webhook request includes these headers:

| Header | Description |
|--------|-------------|
| `Content-Type` | `application/json` |
| `X-Mesh-Event` | Event type (e.g., `task.created`) |
| `X-Mesh-Signature` | HMAC-SHA256 signature of the body |
| `X-Mesh-Event-Id` | Event id (UUID), the same as the body's `event_id`; stable across retries and subscribers |
| `X-Mesh-Delivery` | Delivery id (UUID) of this delivery to this endpoint, kept across its retries |
| `X-Mesh-Timestamp` | Unix timestamp of the event |
| `User-Agent` | `evc-mesh-webhook/1.0` |

## Signature Verification

Every webhook payload is signed with HMAC-SHA256 using your webhook secret. **Always verify signatures** to ensure payloads are authentic.

### How Signing Works

1. Mesh concatenates the timestamp and the raw JSON body: `{timestamp}.{body}`
2. Computes HMAC-SHA256 using your secret
3. Sends the hex-encoded signature in `X-Mesh-Signature`

### Verification Examples

**Go:**

```go
import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "io"
    "net/http"
)

func verifyWebhook(r *http.Request, secret string) ([]byte, error) {
    body, _ := io.ReadAll(r.Body)
    timestamp := r.Header.Get("X-Mesh-Timestamp")
    signature := r.Header.Get("X-Mesh-Signature")

    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(fmt.Sprintf("%s.%s", timestamp, body)))
    expected := hex.EncodeToString(mac.Sum(nil))

    if !hmac.Equal([]byte(signature), []byte(expected)) {
        return nil, fmt.Errorf("invalid signature")
    }
    return body, nil
}
```

**Python:**

```python
import hmac
import hashlib

def verify_webhook(body: bytes, timestamp: str, signature: str, secret: str) -> bool:
    message = f"{timestamp}.{body.decode()}"
    expected = hmac.new(
        secret.encode(),
        message.encode(),
        hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(signature, expected)
```

**Node.js:**

```javascript
const crypto = require('crypto');

function verifyWebhook(body, timestamp, signature, secret) {
  const message = `${timestamp}.${body}`;
  const expected = crypto
    .createHmac('sha256', secret)
    .update(message)
    .digest('hex');
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(expected)
  );
}
```

## Extension Signature v2

Applies to event deliveries to extension subscriptions only; ordinary webhook configs keep `sha256=` + HMAC(body) and are not affected.

In v1 the HMAC covers `{timestamp}.{body}`. `X-Mesh-Delivery` and `X-Mesh-Event` are not authenticated, so a captured request can be replayed under a new delivery id or event header with its signature intact. v2 closes that: the signature covers timestamp, delivery id, event type and raw body.

### Negotiation

Opt-in per extension, in the manifest:

```json
{ "external": { "signature_version": 2 } }
```

- Absent or `1`: v1, byte-for-byte as before.
- `2`: Mesh sends `"signature_version": 2` in the install body (omitted for v1, so a v1 install body is unchanged) and signs every later delivery with v2. Changing the manifest repeats the install handshake.
- The service **pins** the version it was told at install. A pinned v2 receiver refuses anything that is not v2: no fallback to v1, so stripping the v2 marks does not downgrade it.
- A v2 delivery carries `X-Mesh-Signature-Version: 2` and `X-Mesh-Signature: v2=<hex>`. The `v2=` prefix makes a v1 receiver (bare-hex comparison) fail instead of accepting a message it cannot check. Mesh sends no v1 signature for such a connection, so there is none to capture and re-send.

### Canonical bytes

```
mesh-ext-sig-v2\n
{X-Mesh-Timestamp}\n
{X-Mesh-Delivery}\n
{X-Mesh-Event}\n
{raw body}
```

`\n` is a single line feed. Timestamp is Unix seconds. The signature is `"v2=" + hex(HMAC-SHA256(signing_secret, canonical bytes))`. The first four fields cannot contain a line feed (a receiver refuses a header that does), and the body comes last, so two different inputs never produce the same bytes. The leading label separates v2 from any other use of the secret.

### Verification

Go (`internal/extensions/extsig` is the reference implementation Mesh itself signs with):

```go
v := extsig.Verifier{Secret: signingSecret, MaxSkew: 5 * time.Minute}
d, err := v.Verify(r.Header, rawBody) // version, prefix, signature, window
if err != nil { /* 401 */ }
_, err = once.Do(d.EventID, func() error { return handle(rawBody) }) // atomic; var once extsig.Once
if errors.Is(err, extsig.ErrInProgress) { /* 503: Mesh retries */ } else if err != nil { /* 500 */ }
```

Any language: refuse unless `X-Mesh-Signature-Version` is `2` and the signature starts with `v2=`; rebuild the canonical bytes from the headers and the raw body (not re-serialised JSON); compare in constant time; check the timestamp window.

```python
msg = b"mesh-ext-sig-v2\n" + ts.encode() + b"\n" + delivery.encode() + b"\n" + event.encode() + b"\n" + body
expected = "v2=" + hmac.new(secret.encode(), msg, hashlib.sha256).hexdigest()
ok = version == "2" and hmac.compare_digest(signature, expected)
```

### Identity: delivery id vs event id

`X-Mesh-Delivery` identifies one delivery and is the same on every retry of it; the timestamp is new on each attempt. The event's identity is `event_id` in the signed body (the `X-Mesh-Event-Id` header repeats it but is not covered by the signature; take the id from the body) (stable across retries and across subscribers). Run the handler once per `event_id` (fall back to the delivery id when the body has none), claim the id atomically before running it (a concurrent duplicate answers `5xx` and is retried), release the claim if the handler fails, and answer `2xx` for a repeat of a handled event. Remember handled ids only for a bounded time (`extsig.Once` keeps them 24 h, far beyond the retry window), not forever. Two events with equal payloads but different `event_id` are two events.

### Migration and rollback

Consumers of the extension signature today: `mesh-ext-hello` (reference receiver, verifies per this document) and the Keep receiver. Neither changes by itself: neither manifest sets `signature_version`, so both stay on v1.

1. Ship the v2 verifier in the receiver, still accepting v1 while unpinned (it is told nothing yet).
2. Set `external.signature_version: 2` in the extension manifest and publish it. Mesh repeats the install handshake with `signature_version: 2`; the receiver pins v2 for that workspace.
3. Verify a delivery: the delivery log shows success and the receiver saw `X-Mesh-Signature-Version: 2`.

Rollback: remove the field (or set `1`) in the manifest and publish; the next handshake omits `signature_version` and Mesh signs v1 again. The receiver must drop its pin when an install arrives without `signature_version`. If the receiver was not updated yet, deliveries fail on its side and are retried by Mesh; rolling the manifest back restores them.

## Extension Install Handshake

An external extension (`runtime: external`) whose manifest sets `external.install_path` receives one signed `POST {base_url}{install_path}` when its connection is saved, and again whenever the address, version, event list, signing secret or install code changes:

```json
{
  "extension_id": "hello",
  "version": "0.1.0",
  "workspace_id": "6f1c2a3b-0000-4000-8000-000000000001",
  "signing_secret": "<hex>",
  "events": ["task.updated"],
  "events_path": "/mesh/events",
  "install_code": "BIND-7Q2X-91LK"
}
```

The request carries `X-Mesh-Timestamp` and `X-Mesh-Signature` computed exactly as above, with `signing_secret` as the key. The service keeps `signing_secret` and verifies every later event with it.

### `install_code`

`signing_secret` travels in the same body, so its signature alone only shows that the sender knows that secret, not which Mesh workspace sent it. To bind an installation to a workspace, the service can issue a one-time code and the workspace admin (or an agent) stores it as the workspace secret `EXT_<ID>_INSTALL_CODE`, where `<ID>` is the extension id upper-cased with `-` and `.` replaced by `_` (for `keep-notes`: `EXT_KEEP_NOTES_INSTALL_CODE`). Set it through the workspace secrets API or UI; it is not a connection setting and never appears in the connection's settings, and secret listings show only its fingerprint.

- **When it is sent:** only while that secret exists. Without it the field is omitted and the body is byte-for-byte what earlier Mesh versions sent. An expired secret fails the handshake (the error names the secret, never its value).
- **Signature:** the field is part of the signed body, so it is covered by the same `X-Mesh-Signature`; a changed code invalidates the signature.
- **Re-handshake:** setting or changing the code makes Mesh repeat the handshake.
- **Service side:** check `install_code` only on the first binding of a workspace (when you do not yet hold a signing secret for it), then consume the code. Later handshakes for an already bound workspace are verified with the stored signing secret; a stale or absent code there is not an error.

### Actor token delivery (`token_path`)

An extension whose manifest sets `external.token_path` gets its actor token (`ext_…`) from Mesh directly; the token never passes through a person. Mesh sends a signed `POST {base_url}{token_path}`, without an `Authorization` header:

```json
{
  "extension_id": "hello",
  "workspace_id": "6f1c2a3b-0000-4000-8000-000000000001",
  "extension_token": "ext_…"
}
```

- **Signature:** `X-Mesh-Timestamp` and `X-Mesh-Signature` as above, keyed with the connection's signing secret (the one sent at install).
- **Timestamps only go up:** per connection every delivery is signed strictly later than the previous one, even within one second, so the service may refuse a timestamp not newer than the last it accepted.
- **Success** is `200` with `{"ok":true}`. Anything else is a failed delivery.
- **When:** once after a successful install handshake, if the current token was not delivered yet (a repeated handshake does not rotate), and on every `POST /api/v1/integrations/{id}/rotate-token`. For such an extension that call answers `{"actor", "previous_valid_for", "delivered": true}` and never the token. If the delivery fails it answers `502`, the new token is withdrawn and the previous one keeps working exactly as before the call.
- `POST /ext/me/token/rotate` (the extension rotating its own token) is unchanged: the new token is in its response.

Without `token_path` nothing changes: `rotate-token` returns the token in its response.

## Extension Event Catch-up and Redelivery

An extension subscribed to events with `delivery: webhook` gets each event with up to four attempts (immediately, then after 1, 5 and 25 seconds). An event whose attempts all failed, or that the service lost after answering `2xx`, can be read back and sent again with the extension's own token. Both routes need no `capabilities.api` entry; they only ever touch the calling connection's own subscriptions.

### Read missed events

```bash
GET /api/v1/ext/me/events?since={cursor-or-time}&limit=100&undelivered=true
Authorization: Bearer ext_…
```

| Parameter | Meaning |
|---|---|
| `since` | Empty: from the oldest event. A `next_cursor` from an earlier page: strictly after it. An RFC 3339 time: events created at or after it. |
| `limit` | 1–500, default 100. |
| `undelivered` | `true` lists only events that no attempt or redelivery got a `2xx` for. |

```json
{
  "items": [
    {
      "delivery_id": "9b1f…",
      "subscription_id": "4c2e…",
      "event": "task.updated",
      "payload": { "task_id": "…", "project_id": "…", "workspace_id": "…" },
      "created_at": "2026-10-29T18:00:00.123456Z",
      "delivered": false,
      "attempts": 4,
      "response_status": 500
    }
  ],
  "next_cursor": "MTc2…",
  "has_more": false
}
```

- Events come oldest first. Store `next_cursor` and pass it as `since` to continue; repeat while `has_more` is `true`. `has_more` is `true` only when another logged event follows the page.
- `delivery_id` is the `X-Mesh-Delivery` your receiver got for that event. Dedupe on it.
- The newest 5 seconds are held back, so a cursor never passes an event still being recorded. An event appears in the feed a few seconds after its last attempt.
- What you see is decided when you read, not when the event was sent: an event the extension no longer consents to in `capabilities.events`, an event of a project the connection may no longer see (or whose `project_id` cannot be read while a project limit applies), and the events of a subscription that was removed are left out. A page may therefore hold fewer items than `limit` while `has_more` is `true`; the cursor still moves past the hidden events.
- Only `delivery: webhook` subscriptions are logged here. `delivery: nats` events are durable in JetStream instead.

### Send an event again

```bash
POST /api/v1/ext/me/events/{delivery_id}/redeliver
Authorization: Bearer ext_…
```

Mesh makes one attempt to the subscription's current target, signed with its current secret, and answers with the result:

```json
{ "id": "e07a…", "delivery_id": "9b1f…", "event": "task.updated", "success": true, "response_status": 200 }
```

- The request carries the original `X-Mesh-Delivery` (`delivery_id`), so a receiver that dedupes on it does the work once however many times the event is sent.
- A receiver that does not answer `2xx` is still `200` from this route, with `success: false` and `response_status` or `error`. Retry on your own schedule.
- `404` for a delivery that does not exist or belongs to another connection; `403` when the connection may no longer see the event (same rules as the feed).
- A successful redelivery marks the event `delivered` in the feed.
- `500` when the attempt was made but could not be recorded; the receiver may have it. Retrying is safe: it carries the same `X-Mesh-Delivery`.

## Retry Policy

If your endpoint returns a non-2xx status code or the request times out:

1. **First retry** — after 10 seconds
2. **Second retry** — after 60 seconds
3. **Third retry** — after 300 seconds (5 minutes)

After 3 failed retries, the delivery is marked as failed.

### Auto-Deactivation

If a webhook accumulates **10 consecutive failures** (across multiple events), it is automatically deactivated. You can reactivate it via the API or UI after fixing the endpoint.

## Managing Webhooks

### List Webhooks

```bash
GET /api/v1/workspaces/{ws_id}/webhooks
```

### Update a Webhook

```bash
PATCH /api/v1/webhooks/{webhook_id}
```

You can update `url`, `events`, `active`, and `secret`.

### Delete a Webhook

```bash
DELETE /api/v1/webhooks/{webhook_id}
```

### Test a Webhook

Send a test event to verify your endpoint:

```bash
POST /api/v1/webhooks/{webhook_id}/test
```

This sends a `webhook.test` event with a sample payload.

## Best Practices

1. **Always verify signatures** — never trust payloads without HMAC verification
2. **Respond quickly** — return a 2xx status within 5 seconds; do heavy processing asynchronously
3. **Handle duplicates** — deduplicate on `event_id` (also in `X-Mesh-Event-Id`); every retry and every subscription of one event carries the same id
4. **Use HTTPS** — always use HTTPS endpoints in production
5. **Rotate secrets** — periodically rotate your webhook secret via the API
6. **Monitor failures** — check the webhook status in Workspace Settings to catch deactivated webhooks
