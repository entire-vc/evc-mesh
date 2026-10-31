package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/examples/external-extension/refext"
	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Contract tests between Mesh's event delivery to extension subscriptions and
// the reference extension (examples/external-extension), written from
// docs/webhooks.md only. Dispatch and the signed delivery with its retries are
// the real code; the subscription and secret tables are in memory. A failure
// here breaks every partner receiver built the same way.
//
// Run all of them with: go test ./internal/... -run RefExt

const refextSecretRef = "EXT_REFERENCE_EXT_SIGNING_SECRET"

type refextReceiver struct {
	ext    *refext.Server
	mu     sync.Mutex
	got    []refext.Event
	failOn int32 // OnEvent fails while this is > 0, counting down
}

func (r *refextReceiver) events() []refext.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]refext.Event(nil), r.got...)
}

// newRefextDelivery wires the real webhook service to a reference extension
// subscribed to events, with the workspace already bound to secret.
func newRefextDelivery(t *testing.T, events []string, wrap func(http.Handler) http.Handler) (*extFixture, *refextReceiver) {
	t.Helper()
	recv := &refextReceiver{ext: &refext.Server{ExtensionID: "reference-ext"}}
	recv.ext.OnEvent = func(_ context.Context, ev refext.Event) error {
		if atomic.AddInt32(&recv.failOn, -1) >= 0 {
			return assert.AnError
		}
		recv.mu.Lock()
		recv.got = append(recv.got, ev)
		recv.mu.Unlock()
		return nil
	}
	var h http.Handler = recv.ext
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	const secret = "reference-signing-secret"
	ws, connID := uuid.New(), uuid.New()
	subs := &fakeExtSubs{}
	for _, ev := range events {
		subs.subs = append(subs.subs, domain.ExtensionEventSubscription{
			ID: uuid.New(), ConnectionID: connID, Event: ev, Delivery: domain.ExtensionDeliveryWebhook,
			Target: srv.URL + refext.PathEvents, SecretRef: refextSecretRef,
		})
	}
	repo := &recordingWebhookRepo{}
	svc := &webhookService{
		repo:     repo,
		client:   newWebhookHTTPClient(func(net.IP) bool { return true }),
		backoffs: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
		ext: &extensionDelivery{
			subs:         subs,
			integrations: &fakeExtIntegrations{conns: map[uuid.UUID]*domain.IntegrationConfig{connID: {ID: connID, WorkspaceID: ws, IsActive: true}}},
			secrets:      &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: refextSecretRef, Value: secret}}},
		},
	}

	// Bind the workspace the way the handshake does (covered against the real
	// handshake in internal/extensions/external).
	body, _ := json.Marshal(refext.InstallRequest{ExtensionID: "reference-ext", WorkspaceID: ws.String(), SigningSecret: secret})
	req := httptest.NewRequest(http.MethodPost, refext.PathInstall, strings.NewReader(string(body)))
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(refext.HeaderTimestamp, ts)
	req.Header.Set(refext.HeaderSignature, refext.Sign(secret, ts, body))
	rec := httptest.NewRecorder()
	recv.ext.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	return &extFixture{svc: svc, repo: repo, ws: ws, subsRepo: subs}, recv
}

// A status move goes out under its public name task.moved, signed so the
// reference receiver verifies it, with workspace_id in the body.
func TestContract_RefExt_DeliveryVerifies(t *testing.T) {
	f, recv := newRefextDelivery(t, []string{"task.moved", "task.created"}, nil)
	taskID := uuid.New()

	f.svc.Dispatch(context.Background(), f.ws, "task.status_changed", map[string]any{
		"task_id": taskID, "project_id": uuid.New(), "old_status_id": uuid.New(), "new_status_id": uuid.New(),
	})
	d := f.waitDeliveries(t, 1)
	require.True(t, d[0].Success, "the reference receiver refused Mesh's delivery: %v", d[0].ResponseBody)
	assert.Equal(t, 1, d[0].Attempt)

	got := recv.events()
	require.Len(t, got, 1)
	assert.Equal(t, "task.moved", got[0].Name)
	assert.Equal(t, f.ws.String(), got[0].WorkspaceID)
	assert.Equal(t, d[0].ID.String(), got[0].DeliveryID, "X-Mesh-Delivery is the recorded delivery id")
	var gotTask string
	require.NoError(t, json.Unmarshal(got[0].Data["task_id"], &gotTask))
	assert.Equal(t, taskID.String(), gotTask)
}

// The red control: the same delivery signed with another workspace secret is
// refused by the receiver, so the pass above is a real verification.
func TestContract_RefExt_DeliveryWrongSecretRefused(t *testing.T) {
	f, recv := newRefextDelivery(t, []string{"task.created"}, nil)
	f.svc.ext.secrets = &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: refextSecretRef, Value: "not-the-bound-secret"}}}

	f.svc.Dispatch(context.Background(), f.ws, "task.created", map[string]any{"task_id": uuid.New()})
	d := f.waitDeliveries(t, 1)
	assert.False(t, d[0].Success)
	require.NotNil(t, d[0].ResponseStatus)
	assert.Equal(t, http.StatusUnauthorized, *d[0].ResponseStatus)
	assert.Empty(t, recv.events())
}

// A receiver that fails is retried with the same delivery id and processes the
// event once it can.
func TestContract_RefExt_RetryAfterFailureProcessesOnce(t *testing.T) {
	f, recv := newRefextDelivery(t, []string{"task.created"}, nil)
	recv.failOn = 1

	f.svc.Dispatch(context.Background(), f.ws, "task.created", map[string]any{"task_id": uuid.New()})
	d := f.waitDeliveries(t, 1)
	assert.True(t, d[0].Success)
	assert.Equal(t, 2, d[0].Attempt)
	require.Len(t, recv.events(), 1)
}

// The answer to a processed delivery is lost on the way back: Mesh retries the
// same delivery id and the receiver acknowledges it without running it again.
func TestContract_RefExt_LostAnswerRetryNotRerun(t *testing.T) {
	var calls int32
	lose := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&calls, 1) == 1 {
				next.ServeHTTP(httptest.NewRecorder(), r) // processed, answer dropped
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	f, recv := newRefextDelivery(t, []string{"task.created"}, lose)

	f.svc.Dispatch(context.Background(), f.ws, "task.created", map[string]any{"task_id": uuid.New()})
	d := f.waitDeliveries(t, 1)
	assert.True(t, d[0].Success)
	assert.Equal(t, 2, d[0].Attempt, "Mesh retried after the lost answer")
	assert.Len(t, recv.events(), 1, "the retry carried the same X-Mesh-Delivery and was not run again")
}
