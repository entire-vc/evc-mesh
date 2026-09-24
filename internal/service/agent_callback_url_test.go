package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestValidateAgentCallbackURL(t *testing.T) {
	accepted := []string{
		"",
		"https://hooks.example.com/mesh",
		"http://hooks.example.com:8080/mesh?x=1",
		"https://93.184.216.34/cb",
		"https://[2606:2800:220:1:248:1893:25c8:1946]/cb",
		"https://93.184.216.34./cb",
		"https://123.hooks.example.com/cb",
		"https://hooks.example.com./cb",
		"https://0xdeadbeef.example.com/cb",
	}
	for _, u := range accepted {
		assert.NoError(t, ValidateAgentCallbackURL(u), "should accept %q", u)
	}

	rejected := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
		"http://10.0.0.5/",
		"http://192.168.1.10/",
		"http://172.17.0.1:5432/",
		"http://100.64.0.1/",
		"http://0.0.0.0/",
		"http://[fd00::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://127.0.0.1./",
		"http://2130706433/x",
		"http://017700000001/x",
		"http://0x7f000001/x",
		"http://0X7F000001/x",
		"http://127.1/x",
		"http://10.1/x",
		"http://hooks.example.0x1/",
		"http://localhost:6379/",
		"http://LOCALHOST./",
		"http://redis.localhost/",
		"ftp://hooks.example.com/",
		"file:///etc/passwd",
		"gopher://hooks.example.com/",
		"https://user:pass@hooks.example.com/",
		"https://user@hooks.example.com/",
		"https://hooks.example.com/#frag",
		" https://hooks.example.com/",
		"https:///nohost",
		"hooks.example.com/mesh",
		"https://hooks.example.com/" + strings.Repeat("a", agentCallbackURLMaxLen),
	}
	for _, u := range rejected {
		assert.Error(t, ValidateAgentCallbackURL(u), "should reject %q", u)
	}
}

// countingServer is a loopback httptest server that records how many
// requests reached it.
func countingServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if h != nil {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func allowAnyIP(net.IP) bool { return true }

// Positive control for the tests below: with a guard that admits loopback,
// delivery to the httptest server does land. Without this, "0 hits" could
// mean the harness is broken rather than that the guard refused.
func TestAgentCallback_Delivery_PositiveControl(t *testing.T) {
	srv, hits := countingServer(t, nil)
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(allowAnyIP)}

	s.deliverWithRetry(srv.URL, uuid.New(), "task.assigned", "d1", []byte(`{}`))

	assert.Equal(t, int32(1), hits.Load())
}

// The production client refuses to dial an internal address even when the
// URL gets past write-time checks (e.g. a hostname resolving to 127.0.0.1),
// and gives up at once instead of sleeping through the retry schedule.
func TestAgentCallback_Delivery_InternalAddressRefusedWithoutRetry(t *testing.T) {
	srv, hits := countingServer(t, nil)
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(isPubliclyRoutable)}

	start := time.Now()
	s.deliverWithRetry(srv.URL, uuid.New(), "task.assigned", "d1", []byte(`{}`))

	assert.Equal(t, int32(0), hits.Load(), "a loopback callback must never be reached")
	assert.Less(t, time.Since(start), 5*time.Second, "a guard refusal must not be retried with backoff")
}

// A redirect is never followed, so a public callback cannot bounce the
// request onto an internal target.
func TestAgentCallback_Delivery_RedirectNotFollowed(t *testing.T) {
	inner, innerHits := countingServer(t, nil)
	outer, outerHits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL, http.StatusTemporaryRedirect)
	})
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(allowAnyIP)}

	start := time.Now()
	s.deliverWithRetry(outer.URL, uuid.New(), "task.assigned", "d1", []byte(`{}`))

	assert.Equal(t, int32(1), outerHits.Load(), "one attempt, no retry")
	assert.Equal(t, int32(0), innerHits.Load(), "redirect target must not be requested")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// A value stored before validation existed is re-checked at dispatch and
// skipped. The client here admits loopback, so it is the re-check — not the
// dial guard — that stops the request.
func TestAgentCallback_Dispatch_StoredInternalURLSkipped(t *testing.T) {
	srv, hits := countingServer(t, nil)
	agentID := uuid.New()
	evRepo := newTrackingEventsRepo()
	s := &agentNotifyService{
		agentSvc:        &notifyTestAgentSvc{agent: &domain.Agent{ID: agentID, CallbackURL: srv.URL}},
		rdb:             newSvcMiniredis(t),
		agentEventsRepo: evRepo,
		client:          newAgentCallbackHTTPClient(allowAnyIP),
	}

	s.dispatch(agentID, AgentNotification{EventType: "task.assigned"})

	assert.Equal(t, int32(0), hits.Load())
}

func TestAgentService_Update_ValidatesCallbackURL(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()
	ctx := context.Background()
	id := uuid.New()
	agentRepo.items[id] = &domain.Agent{ID: id, WorkspaceID: ws.ID, Name: "a", CallbackURL: "http://10.0.0.9/legacy"}

	err := svc.Update(ctx, &domain.Agent{ID: id, WorkspaceID: ws.ID, Name: "a", CallbackURL: "http://169.254.169.254/latest/"})
	require.Error(t, err, "a new internal callback_url must be refused")
	assert.Equal(t, "http://10.0.0.9/legacy", agentRepo.items[id].CallbackURL, "refused write must not persist")

	// Unrelated edit on an agent still carrying a pre-validation value: allowed.
	require.NoError(t, svc.Update(ctx, &domain.Agent{ID: id, WorkspaceID: ws.ID, Name: "renamed", CallbackURL: "http://10.0.0.9/legacy"}))

	require.NoError(t, svc.Update(ctx, &domain.Agent{ID: id, WorkspaceID: ws.ID, Name: "renamed", CallbackURL: "https://hooks.example.com/mesh"}))
	require.NoError(t, svc.Update(ctx, &domain.Agent{ID: id, WorkspaceID: ws.ID, Name: "renamed", CallbackURL: ""}), "clearing is always allowed")
}

func TestIsPermanentCallbackError_NXDOMAIN(t *testing.T) {
	nx := &url.Error{Op: "Post", URL: "http://x.invalid", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}}}
	assert.True(t, isPermanentCallbackError(nx), "NXDOMAIN must not be retried")

	tmp := &url.Error{Op: "Post", URL: "http://x.example", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "server misbehaving", Name: "x.example", IsTemporary: true}}}
	assert.False(t, isPermanentCallbackError(tmp), "a temporary resolver failure is retried")

	assert.False(t, isPermanentCallbackError(&url.Error{Op: "Post", URL: "http://x", Err: errors.New("connection reset")}))
}
