# Webhooks

Mesh can send HTTP callbacks to external services when events occur. Webhooks use HMAC-SHA256 signatures for payload verification.

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

Mesh sends `POST` requests with a JSON body:

```json
{
  "id": "evt_a1b2c3d4-...",
  "event": "task.status_changed",
  "workspace_id": "550e8400-...",
  "project_id": "6ba7b810-...",
  "timestamp": "2026-03-11T10:30:00Z",
  "data": {
    "task_id": "7c9e6679-...",
    "title": "Implement auth middleware",
    "old_status": "todo",
    "new_status": "in_progress",
    "actor_id": "550e8400-...",
    "actor_type": "user"
  }
}
```

## Headers

Every webhook request includes these headers:

| Header | Description |
|--------|-------------|
| `Content-Type` | `application/json` |
| `X-Mesh-Event` | Event type (e.g., `task.created`) |
| `X-Mesh-Signature` | HMAC-SHA256 signature of the body |
| `X-Mesh-Delivery` | Unique delivery ID (UUID) |
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
3. **Handle duplicates** — use the `X-Mesh-Delivery` header to deduplicate (retries send the same delivery ID)
4. **Use HTTPS** — always use HTTPS endpoints in production
5. **Rotate secrets** — periodically rotate your webhook secret via the API
6. **Monitor failures** — check the webhook status in Workspace Settings to catch deactivated webhooks
