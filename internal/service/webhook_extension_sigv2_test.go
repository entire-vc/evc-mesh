package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/extsig"
)

const sigSecret = "whsec-sig-v2"

// newSigFixture is newExtFixture for an extension "hello" whose manifest
// selects signature version v (the lookup is by the connection's provider).
func newSigFixture(t *testing.T, target string, v int) *extFixture {
	t.Helper()
	f := newExtFixture(t, target, []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: sigSecret}})
	for _, c := range f.svc.ext.integrations.(*fakeExtIntegrations).conns {
		c.Provider = "hello"
	}
	f.svc.ext.signatureVersion = func(id string) int {
		if id == "hello" {
			return v
		}
		return 1
	}
	return f
}

type capturedRequest struct {
	header http.Header
	body   []byte
}

// replay posts a copy of a captured request, after mutate changed it, to url.
func replay(t *testing.T, url string, c capturedRequest, mutate func(h http.Header, body *[]byte)) int {
	t.Helper()
	h, body := c.header.Clone(), append([]byte(nil), c.body...)
	mutate(h, &body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header = h
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// receiver verifies v2 like an extension would. With verify=false it is the
// red control: the same receiver with the check switched off.
func receiver(verify bool, got chan<- capturedRequest) http.Handler {
	v := extsig.Verifier{Secret: sigSecret}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if verify {
			if _, err := v.Verify(r.Header, b); err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		if got != nil {
			select {
			case got <- capturedRequest{header: r.Header.Clone(), body: b}:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// A real v2 delivery passes a real receiver; a changed delivery id, event type,
// timestamp or body is refused, and the receiver without the check accepts the
// same tampering (red control: the refusals come from the signature).
func TestContract_ExtensionSignatureV2(t *testing.T) {
	got := make(chan capturedRequest, 1)
	good := httptest.NewServer(receiver(true, got))
	defer good.Close()
	open := httptest.NewServer(receiver(false, nil))
	defer open.Close()

	f := newSigFixture(t, good.URL, 2)
	f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1", "event_id": "ev-1"})
	ds := f.waitDeliveries(t, 1)
	require.True(t, ds[0].Success, "a valid v2 delivery passes the receiver")
	c := <-got
	assert.Equal(t, "2", c.header.Get("X-Mesh-Signature-Version"))
	assert.Contains(t, c.header.Get("X-Mesh-Signature"), "v2=")
	assert.Equal(t, ds[0].ID.String(), c.header.Get("X-Mesh-Delivery"))
	assert.Equal(t, "task.updated", c.header.Get("X-Mesh-Event"))

	tamper := map[string]func(h http.Header, b *[]byte){
		"delivery id": func(h http.Header, _ *[]byte) { h.Set("X-Mesh-Delivery", "00000000-0000-0000-0000-000000000001") },
		"event type":  func(h http.Header, _ *[]byte) { h.Set("X-Mesh-Event", "task.deleted") },
		"timestamp": func(h http.Header, _ *[]byte) {
			ts, _ := strconv.ParseInt(h.Get("X-Mesh-Timestamp"), 10, 64)
			h.Set("X-Mesh-Timestamp", strconv.FormatInt(ts+1, 10))
		},
		"body": func(_ http.Header, b *[]byte) { *b = append(*b, ' ') },
	}
	same := func(http.Header, *[]byte) {}
	assert.Equal(t, http.StatusOK, replay(t, good.URL, c, same), "an untouched replay inside the window verifies")
	for name, m := range tamper {
		assert.Equal(t, http.StatusUnauthorized, replay(t, good.URL, c, m), "changed %s is refused", name)
		assert.Equal(t, http.StatusOK, replay(t, open.URL, c, m), "red control: changed %s passes without the check", name)
	}

	stale := extsig.Verifier{Secret: sigSecret, Now: func() time.Time { return time.Now().Add(time.Hour) }}
	_, err := stale.Verify(c.header, c.body)
	assert.Error(t, err, "a correctly signed but old delivery is outside the window")
}

// v1 keeps working for an extension that did not opt in; a receiver pinned to
// v2 accepts no v1 or stripped request; retries of one event run the handler
// once, another event_id with an equal payload runs it again.
func TestContract_ExtensionSignatureCompatibility(t *testing.T) {
	t.Run("v1 unchanged", func(t *testing.T) {
		var seen atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if r.Header.Get("X-Mesh-Signature") != validSignature(sigSecret, r.Header.Get("X-Mesh-Timestamp"), b) ||
				r.Header.Get("X-Mesh-Signature-Version") != "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			seen.Store(true)
		}))
		defer srv.Close()
		f := newSigFixture(t, srv.URL, 1)
		f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1"})
		require.True(t, f.waitDeliveries(t, 1)[0].Success)
		assert.True(t, seen.Load())
	})

	t.Run("no downgrade", func(t *testing.T) {
		v1 := make(chan capturedRequest, 1)
		srv1 := httptest.NewServer(receiver(false, v1))
		defer srv1.Close()
		f1 := newSigFixture(t, srv1.URL, 1)
		f1.svc.Dispatch(context.Background(), f1.ws, "task.updated", map[string]any{"task_id": "t1"})
		f1.waitDeliveries(t, 1)
		c1 := <-v1

		v2 := make(chan capturedRequest, 1)
		srv2 := httptest.NewServer(receiver(false, v2))
		defer srv2.Close()
		f2 := newSigFixture(t, srv2.URL, 2)
		f2.svc.Dispatch(context.Background(), f2.ws, "task.updated", map[string]any{"task_id": "t1"})
		f2.waitDeliveries(t, 1)
		c2 := <-v2

		pinned := extsig.Verifier{Secret: sigSecret}
		_, err := pinned.Verify(c2.header, c2.body)
		require.NoError(t, err, "the v2 delivery verifies")
		_, err = pinned.Verify(c1.header, c1.body)
		assert.Error(t, err, "a v1-signed request is refused by a receiver pinned to v2")

		strip := c2.header.Clone()
		strip.Del("X-Mesh-Signature-Version")
		_, err = pinned.Verify(strip, c2.body)
		assert.Error(t, err, "dropping the version header is not a downgrade path")
		old := c2.header.Clone()
		old.Set("X-Mesh-Signature-Version", "1")
		_, err = pinned.Verify(old, c2.body)
		assert.Error(t, err, "claiming version 1 is refused")
		bare := c2.header.Clone()
		bare.Set("X-Mesh-Signature", bare.Get("X-Mesh-Signature")[len(extsig.Prefix):])
		_, err = pinned.Verify(bare, c2.body)
		assert.Error(t, err, "a bare hex signature is not a v2 signature")
	})

	t.Run("handler once per event", func(t *testing.T) {
		var once extsig.Once
		var mu sync.Mutex
		var runs int
		var hits atomic.Int32
		v := extsig.Verifier{Secret: sigSecret}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			d, err := v.Verify(r.Header, b)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = once.Do(d.EventID, func() error {
				mu.Lock()
				runs++
				mu.Unlock()
				return nil
			})
			// The first answer of the first event is lost: Mesh retries it.
			if hits.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		f := newSigFixture(t, srv.URL, 2)
		f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1", "event_id": "ev-a"})
		f.waitDeliveries(t, 1)
		mu.Lock()
		assert.Equal(t, 1, runs, "a retry of the same event runs the handler once")
		mu.Unlock()
		assert.GreaterOrEqual(t, int(hits.Load()), 2, "the event was delivered more than once")

		f.svc.Dispatch(context.Background(), f.ws, "task.updated", map[string]any{"task_id": "t1", "event_id": "ev-b"})
		f.waitDeliveries(t, 2)
		mu.Lock()
		assert.Equal(t, 2, runs, "another event_id with an equal payload runs it again")
		mu.Unlock()
	})
}

// Concurrent deliveries of one event run the handler once; a failed handler
// releases the claim; a held claim answers ErrInProgress (red control: a
// check-then-act pair would run it twice).
func TestContract_ExtensionSignatureOnceIsAtomic(t *testing.T) {
	var once extsig.Once
	var runs atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = once.Do("ev", func() error { runs.Add(1); close(started); <-release; return nil })
	}()
	<-started
	ran, err := once.Do("ev", func() error { runs.Add(1); return nil })
	assert.False(t, ran)
	assert.ErrorIs(t, err, extsig.ErrInProgress)
	close(release)
	wg.Wait()
	ran, err = once.Do("ev", func() error { runs.Add(1); return nil })
	assert.False(t, ran)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, runs.Load())

	_, err = once.Do("bad", func() error { return assert.AnError })
	assert.Error(t, err)
	ran, err = once.Do("bad", func() error { return nil })
	assert.True(t, ran, "a failed handler is retried")
	assert.NoError(t, err)
}

// The option wires the manifest lookup into the delivery; a connection whose
// extension selects v2 gets it, any other stays v1, and a header value that
// cannot be signed fails the send instead of going out unsigned.
func TestExtensionSignatureV2_OptionAndSignFailure(t *testing.T) {
	svc := &webhookService{ext: &extensionDelivery{}}
	WithExtensionSignatureVersion(func(id string) int {
		if id == "hello" {
			return 2
		}
		return 1
	})(svc)
	assert.Equal(t, sigExtensionV2, svc.ext.scheme(&domain.IntegrationConfig{Provider: "hello"}))
	assert.Equal(t, sigExtensionV1, svc.ext.scheme(&domain.IntegrationConfig{Provider: "other"}))
	assert.Equal(t, sigExtensionV1, svc.ext.scheme(nil))
	assert.Equal(t, sigExtensionV1, (&extensionDelivery{}).scheme(&domain.IntegrationConfig{Provider: "hello"}))
	WithExtensionSignatureVersion(nil)(&webhookService{})

	_, _, _, err := svc.sendSignedWith(sigExtensionV2, "http://127.0.0.1:1/x", "s", "task\nupdated", uuid.New(), uuid.New(), []byte("{}"))
	assert.ErrorContains(t, err, "sign")
}
