//go:build integration

package integration

import (
	"testing"
)

// TestWebhookLifecycle tests the full webhook CRUD + dispatch flow.
//
// Scenario:
//  1. Create a webhook subscribed to "task.created" events.
//  2. Create a task — webhook should receive the event.
//  3. Update webhook to also subscribe to "task.updated".
//  4. Update the task — webhook should receive the update event.
//  5. Delete the webhook.
//  6. Create another task — no delivery should occur.
func TestWebhookLifecycle(t *testing.T) {
	t.Skip("TODO: implement webhook feature first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	_ = env
	// 1. Register user and get workspace.

	// 2. Create a webhook for "task.created" events pointing to a local test
	//    HTTP server (httptest.NewServer) that records incoming requests.

	// 3. Create a task via POST /api/v1/projects/:proj_id/tasks.

	// 4. Wait (with timeout) for the webhook listener to receive exactly one
	//    delivery; assert event_type == "task.created" and payload.task_id
	//    matches the created task.

	// 5. PATCH the webhook to add "task.updated" to its event subscriptions.

	// 6. Update the task via PATCH /api/v1/tasks/:task_id.

	// 7. Wait for a second delivery; assert event_type == "task.updated".

	// 8. DELETE /api/v1/webhooks/:webhook_id — expect 204.

	// 9. Create a second task.

	// 10. Wait 1 second — assert no new deliveries arrived after deletion.
}

// TestWebhookHMAC verifies that webhook deliveries include a valid
// HMAC-SHA256 signature in the X-Webhook-Signature header.
//
// Scenario:
//  1. Create a webhook with a known secret string.
//  2. Trigger an event that causes a delivery.
//  3. Compute expected HMAC-SHA256(secret, payload_bytes).
//  4. Assert X-Webhook-Signature matches the computed value.
func TestWebhookHMAC(t *testing.T) {
	t.Skip("TODO: implement webhook feature first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	_ = env
	// 1. Register user, get workspace, create project.

	// 2. Start httptest.NewServer; record the full raw request body and headers.

	// 3. Create webhook with secret "test-hmac-secret".

	// 4. Create a task to trigger the "task.created" delivery.

	// 5. Wait for delivery.

	// 6. Compute HMAC: hex.EncodeToString(hmac.New(sha256.New, []byte(secret),
	//    recorded_body)).

	// 7. Assert header["X-Webhook-Signature"] == "sha256=<computed>".
}

// TestWebhookAutoDeactivation verifies that a webhook is automatically
// deactivated after too many consecutive delivery failures.
//
// Scenario:
//  1. Create a webhook pointing to a URL that always returns 500.
//  2. Trigger enough events to exceed the failure threshold.
//  3. Assert the webhook is now marked inactive (GET /webhooks/:id → active=false).
//  4. Trigger one more event.
//  5. Assert no new delivery attempt is made for the inactive webhook.
func TestWebhookAutoDeactivation(t *testing.T) {
	t.Skip("TODO: implement webhook feature first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	_ = env
	// 1. Register user, get workspace, create project.

	// 2. Start httptest.NewServer that always responds with 500.

	// 3. Create webhook pointing at the failing server.

	// 4. Create 10 tasks to fire 10 consecutive "task.created" events.

	// 5. Poll GET /api/v1/webhooks/:webhook_id until active == false OR
	//    timeout (30 s).

	// 6. Assert active == false (auto-deactivation threshold reached).

	// 7. Reset the delivery counter; create one more task.

	// 8. Assert the 11th event does NOT trigger a new delivery (listener
	//    receives no new request within 2 s).
}
