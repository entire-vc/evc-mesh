package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// recordingWebhookRepo is a WebhookRepository that only remembers deliveries
// and failure bookkeeping; everything else is a no-op.
type recordingWebhookRepo struct {
	mu         sync.Mutex
	deliveries []*domain.WebhookDelivery
	failures   int
}

func (r *recordingWebhookRepo) Create(context.Context, *domain.WebhookConfig) error { return nil }
func (r *recordingWebhookRepo) GetByID(context.Context, uuid.UUID) (*domain.WebhookConfig, error) {
	return nil, nil
}
func (r *recordingWebhookRepo) Update(context.Context, uuid.UUID, domain.UpdateWebhookInput) (*domain.WebhookConfig, error) {
	return nil, nil
}
func (r *recordingWebhookRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (r *recordingWebhookRepo) ListByWorkspace(context.Context, uuid.UUID) ([]domain.WebhookConfig, error) {
	return nil, nil
}
func (r *recordingWebhookRepo) ListActiveByEvent(context.Context, uuid.UUID, string) ([]domain.WebhookConfig, error) {
	return nil, nil
}
func (r *recordingWebhookRepo) IncrementFailure(context.Context, uuid.UUID) error {
	r.mu.Lock()
	r.failures++
	r.mu.Unlock()
	return nil
}
func (r *recordingWebhookRepo) ResetFailure(context.Context, uuid.UUID) error { return nil }
func (r *recordingWebhookRepo) Deactivate(context.Context, uuid.UUID) error   { return nil }
func (r *recordingWebhookRepo) CreateDelivery(_ context.Context, d *domain.WebhookDelivery) error {
	r.mu.Lock()
	r.deliveries = append(r.deliveries, d)
	r.mu.Unlock()
	return nil
}
func (r *recordingWebhookRepo) ListDeliveries(context.Context, uuid.UUID, int) ([]domain.WebhookDelivery, error) {
	return nil, nil
}

var _ repository.WebhookRepository = (*recordingWebhookRepo)(nil)

func newTestWebhook(target string) domain.WebhookConfig {
	return domain.WebhookConfig{ID: uuid.New(), WorkspaceID: uuid.New(), URL: target, Secret: "s3cret"}
}

func TestValidateWebhookURL_SharedPredicate(t *testing.T) {
	accepted := []string{
		"https://93.184.216.34/hook",
		"http://93.184.216.34:8080/hook?x=1",
		"https://[2606:2800:220:1:248:1893:25c8:1946]/hook",
	}
	for _, u := range accepted {
		assert.NoError(t, validateWebhookURL(u), "should accept %q", u)
	}

	rejected := []string{
		"",
		"ftp://example.com/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
		"http://10.0.0.5/",
		"http://192.168.1.10/",
		"http://172.17.0.1:5432/",
		"http://0.0.0.0/",
		"http://[fd00::1]/",
		// Ranges the old private-range list did not know about.
		"http://100.64.0.1/",
		"http://[::ffff:127.0.0.1]/",
		"http://[64:ff9b::7f00:1]/",
		"http://[2002:7f00:1::1]/",
		"http://127.0.0.1./",
		// Numeric spellings a libc resolver reads as an IP.
		"http://2130706433/x",
		"http://017700000001/x",
		"http://0x7f000001/x",
		"http://127.1/x",
		"http://localhost:6379/",
		"http://LOCALHOST./",
		"http://redis.localhost/",
	}
	for _, u := range rejected {
		assert.Error(t, validateWebhookURL(u), "should reject %q", u)
	}
}

// The write-time check cannot be the boundary: a hostname is only resolved
// once there. This is the delivery side of the same claim.
func TestWebhookDelivery_RefusesNonPublicAddress(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Positive control: the same request goes through when the predicate admits
	// loopback, so a refusal below is the guard and not a broken fixture.
	open := &webhookService{client: newWebhookHTTPClient(func(net.IP) bool { return true })}
	status, _, _, err := open.sendHTTP(newTestWebhook(srv.URL), "task.created", uuid.New(), []byte(`{}`), 1)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	require.EqualValues(t, 1, hits.Load())

	hits.Store(0)
	prod := NewWebhookService(&recordingWebhookRepo{}).(*webhookService)
	_, _, _, err = prod.sendHTTP(newTestWebhook(srv.URL), "task.created", uuid.New(), []byte(`{}`), 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errDialAddressRefused)
	assert.True(t, isPermanentWebhookError(err))
	assert.Zero(t, hits.Load(), "the request must not reach a loopback receiver")
}

// A name that resolved to something public when the webhook was written and to
// loopback at delivery time (DNS rebinding) is stopped at dial. localhost stands
// in for the rebound name: it is a hostname, so nothing but the dial-time check
// can catch it.
func TestWebhookDelivery_RefusesHostnameResolvingToLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)

	prod := NewWebhookService(&recordingWebhookRepo{}).(*webhookService)
	_, _, _, err = prod.sendHTTP(newTestWebhook("http://localhost:"+port+"/hook"), "task.created", uuid.New(), []byte(`{}`), 1)
	require.Error(t, err)
	// Not just "permanent": NXDOMAIN is too, and would pass on a box without a
	// localhost entry without ever exercising the guard.
	assert.ErrorIs(t, err, errDialAddressRefused)
	assert.Zero(t, hits.Load())
}

// A public receiver that answers 307 to an internal address must not send the
// client there, and the delivery log must show the 307 it actually answered.
func TestWebhookDelivery_RedirectNotFollowed(t *testing.T) {
	var innerHits atomic.Int32
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		innerHits.Add(1)
	}))
	defer inner.Close()
	var outerHits atomic.Int32
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outerHits.Add(1)
		http.Redirect(w, r, inner.URL, http.StatusTemporaryRedirect)
	}))
	defer outer.Close()

	// Both receivers sit on loopback, so admit loopback: what is under test is
	// the redirect handling, not the address check.
	repo := &recordingWebhookRepo{}
	s := &webhookService{repo: repo, client: newWebhookHTTPClient(func(net.IP) bool { return true })}

	status, _, _, err := s.sendHTTP(newTestWebhook(outer.URL), "task.created", uuid.New(), []byte(`{}`), 1)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTemporaryRedirect, status)
	assert.Zero(t, innerHits.Load(), "the redirect target must never be requested")

	// dispatchOne records the 307 as a failed delivery and does not retry it.
	outerHits.Store(0)
	s.dispatchOne(newTestWebhook(outer.URL), "task.created", []byte(`{}`))
	assert.EqualValues(t, 1, outerHits.Load(), "a 30x is not retried")
	assert.Zero(t, innerHits.Load())
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.deliveries, 1)
	assert.False(t, repo.deliveries[0].Success)
	require.NotNil(t, repo.deliveries[0].ResponseStatus)
	assert.Equal(t, http.StatusTemporaryRedirect, *repo.deliveries[0].ResponseStatus)
	assert.Equal(t, 1, repo.deliveries[0].Attempt)
}

func TestIsPermanentWebhookError(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("http post: %w", &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "dial", Err: err}})
	}
	permanent := map[string]error{
		"guard refusal": wrap(fmt.Errorf("%w non-public address 127.0.0.1", errDialAddressRefused)),
		"NXDOMAIN":      wrap(&net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}),
	}
	for name, err := range permanent {
		assert.True(t, isPermanentWebhookError(err), name)
	}
	transient := map[string]error{
		"connection refused": wrap(errors.New("connection refused")),
		"temporary DNS":      wrap(&net.DNSError{Err: "server misbehaving", Name: "x", IsTemporary: true}),
		"timeout":            wrap(context.DeadlineExceeded),
	}
	for name, err := range transient {
		assert.False(t, isPermanentWebhookError(err), name)
	}
}

// A refusal by the guard is final: three attempts would sleep 1s+5s+25s per
// event and hold a goroutine for nothing.
func TestWebhookDispatchOne_GuardRefusalIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	repo := &recordingWebhookRepo{}
	s := NewWebhookService(repo).(*webhookService)

	done := make(chan struct{})
	go func() {
		s.dispatchOne(newTestWebhook(srv.URL), "task.created", []byte(`{}`))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatchOne is still retrying a permanent refusal")
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.deliveries, 1)
	assert.False(t, repo.deliveries[0].Success)
	assert.Equal(t, 1, repo.deliveries[0].Attempt)
	assert.Equal(t, 1, repo.failures, "a refused delivery still counts as a failure")
}

// Ordinary receivers keep working (transient classification is covered by
// TestIsPermanentWebhookError).
func TestWebhookDispatchOne_RegularDeliveryUnchanged(t *testing.T) {
	var hits atomic.Int32
	type seen struct{ sig, event string }
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case got <- seen{r.Header.Get("X-Mesh-Signature"), r.Header.Get("X-Mesh-Event")}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	repo := &recordingWebhookRepo{}
	s := &webhookService{repo: repo, client: newWebhookHTTPClient(func(net.IP) bool { return true })}
	s.dispatchOne(newTestWebhook(srv.URL), "task.created", []byte(`{"a":1}`))

	require.EqualValues(t, 1, hits.Load())
	h := <-got
	assert.True(t, strings.HasPrefix(h.sig, "sha256="))
	assert.Equal(t, "task.created", h.event)
	require.Len(t, repo.deliveries, 1)
	assert.True(t, repo.deliveries[0].Success)
}
