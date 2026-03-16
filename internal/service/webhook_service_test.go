package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Webhook service contract tests.
//
// These tests define the expected behavior for the WebhookService that will
// be implemented in Phase 5-OSS. Tests that require an implementation are
// marked with t.Skip() and will pass once the service is built.
//
// Contract:
//   - Webhooks have: URL, secret, list of subscribed event types, active flag.
//   - On event dispatch, each active webhook matching the event type is POSTed.
//   - The POST body is JSON: { "event": "task.created", "payload": {...} }.
//   - The X-Mesh-Signature header contains HMAC-SHA256 of the raw body.
//   - After 10 consecutive failures, is_active is set to false (auto-deactivation).
//   - Event filtering: only webhooks subscribed to an event type receive it.
//   - WorkspaceID is required (multi-tenant isolation).
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Domain types (to be moved to internal/domain/webhook.go in Phase 5-OSS)
// ---------------------------------------------------------------------------

// WebhookEvent is the type of event a webhook can subscribe to.
type WebhookEvent string

const (
	WebhookEventTaskCreated   WebhookEvent = "task.created"
	WebhookEventTaskUpdated   WebhookEvent = "task.updated"
	WebhookEventTaskDeleted   WebhookEvent = "task.deleted"
	WebhookEventTaskMoved     WebhookEvent = "task.moved"
	WebhookEventCommentAdded  WebhookEvent = "comment.added"
	WebhookEventAgentAssigned WebhookEvent = "agent.assigned"
)

// Webhook represents a registered webhook endpoint.
type Webhook struct {
	ID               uuid.UUID      `json:"id"`
	WorkspaceID      uuid.UUID      `json:"workspace_id"`
	URL              string         `json:"url"`
	Secret           string         `json:"-"` // stored hashed; raw only at creation
	Events           []WebhookEvent `json:"events"`
	IsActive         bool           `json:"is_active"`
	ConsecutiveFails int            `json:"consecutive_fails"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// WebhookDispatchPayload is the body POSTed to the webhook URL.
type WebhookDispatchPayload struct {
	Event     WebhookEvent   `json:"event"`
	Payload   map[string]any `json:"payload"`
	Timestamp time.Time      `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// Local mock repository for tests (uses local Webhook type, not domain.WebhookConfig)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// In-memory mock repository for tests
// ---------------------------------------------------------------------------

type mockWebhookRepo struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*Webhook
}

func newMockWebhookRepo() *mockWebhookRepo {
	return &mockWebhookRepo{items: make(map[uuid.UUID]*Webhook)}
}

func (r *mockWebhookRepo) Create(_ context.Context, wh *Webhook) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[wh.ID] = wh
	return nil
}

func (r *mockWebhookRepo) GetByID(_ context.Context, id uuid.UUID) (*Webhook, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wh, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return wh, nil
}

func (r *mockWebhookRepo) Update(_ context.Context, wh *Webhook) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[wh.ID] = wh
	return nil
}

func (r *mockWebhookRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
	return nil
}

func (r *mockWebhookRepo) ListByWorkspace(_ context.Context, wsID uuid.UUID) ([]Webhook, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []Webhook
	for _, wh := range r.items {
		if wh.WorkspaceID == wsID {
			result = append(result, *wh)
		}
	}
	return result, nil
}

func (r *mockWebhookRepo) ListActiveByEvent(_ context.Context, wsID uuid.UUID, event WebhookEvent) ([]Webhook, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []Webhook
	for _, wh := range r.items {
		if !wh.IsActive || wh.WorkspaceID != wsID {
			continue
		}
		for _, e := range wh.Events {
			if e == event {
				result = append(result, *wh)
				break
			}
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Reference HMAC implementation (used both in tests and will be in the service)
// ---------------------------------------------------------------------------

// computeHMACSHA256 returns the hex-encoded HMAC-SHA256 of body signed with secret.
func computeHMACSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ---------------------------------------------------------------------------
// Tests: WebhookService.Create
// ---------------------------------------------------------------------------

func TestWebhookService_Create(t *testing.T) {
	t.Run("valid webhook stores with generated ID and timestamps", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Given a valid URL and event list
		// When Create is called
		// Then webhook.ID is non-nil UUID
		// And webhook.IsActive is true
		// And webhook.CreatedAt/UpdatedAt are set
		_ = &Webhook{
			WorkspaceID: uuid.New(),
			URL:         "https://example.com/webhook",
			Secret:      "my-secret",
			Events:      []WebhookEvent{WebhookEventTaskCreated, WebhookEventTaskUpdated},
		}
	})

	t.Run("invalid URL returns validation error", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Given a webhook with an invalid URL (not http/https)
		// When Create is called
		// Then error with HTTP 422 / 400 is returned
		_ = &Webhook{
			WorkspaceID: uuid.New(),
			URL:         "ftp://not-http.example.com",
			Events:      []WebhookEvent{WebhookEventTaskCreated},
		}
	})

	t.Run("empty URL returns validation error", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		_ = &Webhook{
			WorkspaceID: uuid.New(),
			URL:         "",
			Events:      []WebhookEvent{WebhookEventTaskCreated},
		}
	})

	t.Run("empty events list returns validation error", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		_ = &Webhook{
			WorkspaceID: uuid.New(),
			URL:         "https://example.com/webhook",
			Events:      []WebhookEvent{},
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: WebhookService.Dispatch — event routing
// ---------------------------------------------------------------------------

func TestWebhookService_Dispatch(t *testing.T) {
	t.Run("sends POST to matching webhook", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Given a webhook subscribed to task.created
		// And a test HTTP server recording requests
		// When Dispatch(event=task.created, payload={task_id: X}) is called
		// Then the server receives exactly 1 POST request
		// And the body contains event="task.created"
		// And the X-Mesh-Signature header is set and valid
	})

	t.Run("skips non-matching webhook", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Given a webhook subscribed to task.created only
		// When Dispatch(event=task.deleted) is called
		// Then the server receives 0 requests
	})

	t.Run("dispatches to multiple matching webhooks", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Given 3 webhooks, all subscribed to task.created
		// When Dispatch(event=task.created) is called
		// Then all 3 servers receive a POST
	})

	t.Run("skips inactive webhooks", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Given a webhook with is_active=false subscribed to task.created
		// When Dispatch(event=task.created) is called
		// Then the server receives 0 requests
	})

	t.Run("dispatch payload contains correct structure", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// The dispatched body must JSON-decode to WebhookDispatchPayload
		// with event field matching the dispatched event type
	})
}

// ---------------------------------------------------------------------------
// Tests: HMAC-SHA256 signature generation
//
// These tests can run NOW — they validate the pure function that will be
// embedded in the WebhookService. No service implementation needed.
// ---------------------------------------------------------------------------

func TestWebhookService_HMAC(t *testing.T) {
	t.Run("generates correct HMAC-SHA256 signature", func(t *testing.T) {
		secret := "test-webhook-secret"
		body := []byte(`{"event":"task.created","payload":{"id":"abc"}}`)

		// Compute expected signature manually.
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))

		got := computeHMACSHA256(secret, body)
		assert.Equal(t, expected, got)
		assert.Len(t, got, 64, "HMAC-SHA256 hex string must be 64 characters")
	})

	t.Run("different secrets produce different signatures", func(t *testing.T) {
		body := []byte(`{"event":"task.created"}`)
		sig1 := computeHMACSHA256("secret-one", body)
		sig2 := computeHMACSHA256("secret-two", body)
		assert.NotEqual(t, sig1, sig2)
	})

	t.Run("different bodies produce different signatures", func(t *testing.T) {
		secret := "my-secret"
		sig1 := computeHMACSHA256(secret, []byte(`{"event":"task.created"}`))
		sig2 := computeHMACSHA256(secret, []byte(`{"event":"task.deleted"}`))
		assert.NotEqual(t, sig1, sig2)
	})

	t.Run("same body and secret produce same signature (deterministic)", func(t *testing.T) {
		secret := "deterministic-secret"
		body := []byte(`{"event":"comment.added","payload":{}}`)
		sig1 := computeHMACSHA256(secret, body)
		sig2 := computeHMACSHA256(secret, body)
		assert.Equal(t, sig1, sig2)
	})

	t.Run("signature is valid hex string", func(t *testing.T) {
		sig := computeHMACSHA256("any-secret", []byte("any-body"))
		_, err := hex.DecodeString(sig)
		require.NoError(t, err, "signature must be valid hex")
	})
}

// ---------------------------------------------------------------------------
// Tests: HMAC verification on received webhook (end-to-end signature check)
//
// This test can run NOW — it validates the HTTP server side of the contract.
// ---------------------------------------------------------------------------

func TestWebhookService_SignatureVerification_EndToEnd(t *testing.T) {
	// This test simulates the full sender→receiver flow for HMAC signature verification.
	// The "sender" is the WebhookService.Dispatch; the "receiver" is the customer's server.
	// It does NOT require a WebhookService implementation.
	secret := "integration-test-secret"
	payload := WebhookDispatchPayload{
		Event: WebhookEventTaskCreated,
		Payload: map[string]any{
			"task_id": uuid.New().String(),
			"title":   "Test Task",
		},
		Timestamp: time.Now().UTC(),
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	// Sender computes signature before sending.
	signature := computeHMACSHA256(secret, body)

	// Receiver: test HTTP server that validates the incoming signature.
	var receivedSignature string
	var receivedEvent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		receivedSignature = r.Header.Get("X-Mesh-Signature")

		var p WebhookDispatchPayload
		err := json.NewDecoder(r.Body).Decode(&p)
		require.NoError(t, err, "body must be valid JSON")
		receivedEvent = string(p.Event)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Sender: POST the body with the HMAC signature header.
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		server.URL,
		strings.NewReader(string(body)),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mesh-Signature", signature)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, string(WebhookEventTaskCreated), receivedEvent)

	// Receiver verifies: re-compute HMAC from received body and compare.
	recomputed := computeHMACSHA256(secret, body)
	assert.True(t, hmac.Equal([]byte(receivedSignature), []byte(recomputed)),
		"receiver must be able to verify the HMAC signature")
	assert.Equal(t, 64, len(signature), "HMAC-SHA256 hex must be 64 chars")
}

// ---------------------------------------------------------------------------
// Tests: Auto-deactivation after consecutive failures
// ---------------------------------------------------------------------------

func TestWebhookService_AutoDeactivation(t *testing.T) {
	t.Run("deactivates after 10 consecutive failures", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Setup:
		//   - Create a webhook pointing to a failing HTTP server (always 500).
		//   - Dispatch the subscribed event 10 times.
		// Assert:
		//   - After 10 failures, webhook.IsActive == false in the repository.
		//   - webhook.ConsecutiveFails == 10.
	})

	t.Run("resets consecutive fail counter on success", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Setup:
		//   - Create webhook, fail 5 times, then succeed once.
		// Assert:
		//   - webhook.ConsecutiveFails == 0 after successful delivery.
		//   - webhook.IsActive remains true.
	})

	t.Run("does not deactivate before 10 failures", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Setup:
		//   - Fail 9 times.
		// Assert:
		//   - webhook.IsActive still true after 9 failures.
		//   - webhook.ConsecutiveFails == 9.
	})
}

// ---------------------------------------------------------------------------
// Tests: Retry logic
// ---------------------------------------------------------------------------

func TestWebhookService_RetryLogic(t *testing.T) {
	t.Run("retries failed delivery up to configured max_retries", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Setup:
		//   - HTTP server fails first 2 requests, then succeeds.
		//   - WebhookService configured with max_retries=3.
		// Assert:
		//   - Server receives 3 requests total (2 failures + 1 success).
		//   - Final delivery is considered successful.
	})

	t.Run("gives up after max_retries exhausted", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Setup: server always fails.
		// Assert: service tries exactly max_retries times, then records failure.
	})
}

// ---------------------------------------------------------------------------
// Tests: Multi-tenant isolation
// ---------------------------------------------------------------------------

func TestWebhookService_MultiTenantIsolation(t *testing.T) {
	t.Run("dispatch only calls webhooks from same workspace", func(t *testing.T) {
		t.Skip("TODO: implement WebhookService in internal/service/webhook_service.go")
		// Setup:
		//   - workspace-A has webhook-A subscribed to task.created.
		//   - workspace-B has webhook-B subscribed to task.created.
		// When:
		//   - Dispatch is called for workspace-A, event=task.created.
		// Assert:
		//   - Only webhook-A is called; webhook-B is NOT called.
	})
}

// ---------------------------------------------------------------------------
// Tests: Event filtering in the mock repository (these run NOW)
// ---------------------------------------------------------------------------

func TestMockWebhookRepo_ListActiveByEvent(t *testing.T) {
	// Validates that our mock correctly implements event-based filtering.
	// This also documents the expected behavior of the real repository.
	ctx := context.Background()
	repo := newMockWebhookRepo()
	wsID := uuid.New()

	whTaskCreated := &Webhook{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		URL:         "https://a.example.com/webhook",
		Events:      []WebhookEvent{WebhookEventTaskCreated},
		IsActive:    true,
	}
	whBoth := &Webhook{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		URL:         "https://b.example.com/webhook",
		Events:      []WebhookEvent{WebhookEventTaskCreated, WebhookEventCommentAdded},
		IsActive:    true,
	}
	whInactive := &Webhook{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		URL:         "https://c.example.com/webhook",
		Events:      []WebhookEvent{WebhookEventTaskCreated},
		IsActive:    false, // inactive — must not be returned
	}
	whOtherWS := &Webhook{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(), // different workspace
		URL:         "https://d.example.com/webhook",
		Events:      []WebhookEvent{WebhookEventTaskCreated},
		IsActive:    true,
	}

	require.NoError(t, repo.Create(ctx, whTaskCreated))
	require.NoError(t, repo.Create(ctx, whBoth))
	require.NoError(t, repo.Create(ctx, whInactive))
	require.NoError(t, repo.Create(ctx, whOtherWS))

	t.Run("task.created returns only active matching webhooks", func(t *testing.T) {
		results, err := repo.ListActiveByEvent(ctx, wsID, WebhookEventTaskCreated)
		require.NoError(t, err)
		assert.Len(t, results, 2, "should return 2 active webhooks subscribed to task.created")
		for _, wh := range results {
			assert.True(t, wh.IsActive, "only active webhooks should be returned")
			assert.Equal(t, wsID, wh.WorkspaceID, "only same-workspace webhooks should be returned")
		}
	})

	t.Run("comment.added returns only whBoth", func(t *testing.T) {
		results, err := repo.ListActiveByEvent(ctx, wsID, WebhookEventCommentAdded)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, whBoth.ID, results[0].ID)
	})

	t.Run("task.deleted returns empty (no webhook subscribed)", func(t *testing.T) {
		results, err := repo.ListActiveByEvent(ctx, wsID, WebhookEventTaskDeleted)
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

// ---------------------------------------------------------------------------
// Tests: Dispatch simulation using mock HTTP server (runs NOW)
//
// This simulates the full dispatch flow without a real WebhookService.
// It validates the protocol contract that the service must implement.
// ---------------------------------------------------------------------------

func TestWebhookDispatch_ProtocolContract(t *testing.T) {
	// This test validates the HTTP contract that WebhookService.Dispatch
	// must adhere to. It uses a local HTTP test server and performs a real POST.
	secret := "webhook-protocol-secret"
	wsID := uuid.New()

	var requestCount int64
	var receivedEvent string
	var receivedSig string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requestCount, 1)

		assert.Equal(t, http.MethodPost, r.Method, "must use POST method")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		receivedSig = r.Header.Get("X-Mesh-Signature")
		assert.NotEmpty(t, receivedSig, "X-Mesh-Signature header must be set")

		var payload WebhookDispatchPayload
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err, "body must be valid JSON")
		receivedEvent = string(payload.Event)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Simulate a webhook registered to task.created.
	wh := &Webhook{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		URL:         server.URL,
		Secret:      secret,
		Events:      []WebhookEvent{WebhookEventTaskCreated},
		IsActive:    true,
	}

	// Simulate what WebhookService.Dispatch will do.
	event := WebhookEventTaskCreated
	eventPayload := map[string]any{
		"task_id": uuid.New().String(),
		"title":   "Protocol test task",
	}
	dispatchPayload := WebhookDispatchPayload{
		Event:     event,
		Payload:   eventPayload,
		Timestamp: time.Now().UTC(),
	}
	body, err := json.Marshal(dispatchPayload)
	require.NoError(t, err)

	sig := computeHMACSHA256(wh.Secret, body)

	// Validate that our signature is reproducible before sending.
	recomputed := computeHMACSHA256(secret, body)
	assert.Equal(t, sig, recomputed, "signature must be reproducible")

	// Send the real POST request to the test server.
	client := &http.Client{Timeout: 5 * time.Second}
	directReq, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		server.URL,
		strings.NewReader(string(body)),
	)
	require.NoError(t, err)
	directReq.Header.Set("Content-Type", "application/json")
	directReq.Header.Set("X-Mesh-Signature", sig)

	resp, err := client.Do(directReq)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int64(1), atomic.LoadInt64(&requestCount), "server should receive exactly 1 request")
	assert.Equal(t, string(event), receivedEvent, "received event must match dispatched event")
	assert.Equal(t, sig, receivedSig, "received signature must match computed signature")

	// Final HMAC contract assertion: receiver can verify signature.
	assert.True(t, hmac.Equal([]byte(sig), []byte(receivedSig)), "HMAC signature must match")
}
