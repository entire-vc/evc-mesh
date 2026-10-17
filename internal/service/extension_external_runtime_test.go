package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// helloManifest is the manifest of mesh-ext-hello 0.1.0 as the runtime sees it
// after a repository install: runtime=external, task.updated, one workspace
// setting holding the service address.
const helloManifest = `{"schema":1,"id":"hello","name":"Hello","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated"],"ui":[]},
 "external":{"base_url_setting":"hello_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"},
 "settings":{"workspace":{"type":"object","properties":{"hello_url":{"type":"string","format":"uri"}},"required":["hello_url"]}}}`

// helloService is mesh-ext-hello in Go: /mesh/install keeps the signing secret
// (a later different secret needs the old signature), /mesh/events verifies
// hex HMAC-SHA256 of "{X-Mesh-Timestamp}.{body}" with a 300 s skew limit,
// /healthz answers 200.
type helloService struct {
	mu        sync.Mutex
	secret    string
	installs  int
	events    []string
	rejected  int
	srv       *httptest.Server
	installed map[string]any
}

func newHelloService(t *testing.T, preset string) *helloService {
	t.Helper()
	h := &helloService{secret: preset}
	h.srv = httptest.NewServer(h)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *helloService) verify(r *http.Request, body []byte, secret string) bool {
	ts := r.Header.Get("X-Mesh-Timestamp")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || secret == "" {
		return false
	}
	if d := time.Now().Unix() - n; d > 300 || d < -300 {
		return false
	}
	return r.Header.Get("X-Mesh-Signature") == external.Sign(secret, ts, body)
}

func (h *helloService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(http.StatusOK)
	case "/mesh/install":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		sent, _ := in["signing_secret"].(string)
		if h.secret != "" && sent != "" && sent != h.secret && !h.verify(r, body, h.secret) {
			h.rejected++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if sent != "" {
			h.secret = sent
		}
		h.installs++
		h.installed = in
		w.WriteHeader(http.StatusOK)
	case "/mesh/events":
		if !h.verify(r, body, h.secret) {
			h.rejected++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.events = append(h.events, r.Header.Get("X-Mesh-Event"))
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *helloService) snapshot() (installs, rejected int, events []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.installs, h.rejected, append([]string(nil), h.events...)
}

// In-memory stores for the runtime wiring.

type extRtInstalled struct {
	repository.InstalledExtensionRepository
	mu   sync.Mutex
	rows map[string]domain.InstalledExtension
}

func (m *extRtInstalled) put(id, manifest string, blocked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]domain.InstalledExtension{}
	}
	m.rows[id] = domain.InstalledExtension{ExtensionID: id, Manifest: json.RawMessage(manifest), Source: domain.ExtensionSourceRepo, Blocked: blocked}
}

func (m *extRtInstalled) drop(id string) { m.mu.Lock(); delete(m.rows, id); m.mu.Unlock() }

func (m *extRtInstalled) List(context.Context) ([]domain.InstalledExtension, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.InstalledExtension
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

type extRtConns struct{ *exttest.MemIntegrations }

func (c extRtConns) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	return c.Rows[id], nil
}

type extRtSecrets struct {
	mu   sync.Mutex
	vals map[string]string
}

func (s *extRtSecrets) Create(_ context.Context, in domain.CreateSecretInput) (domain.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vals == nil {
		s.vals = map[string]string{}
	}
	s.vals[in.Name] = in.Value
	return domain.Secret{Name: in.Name}, nil
}

func (s *extRtSecrets) ResolveCurrentValues(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.MaterializedSecret
	for k, v := range s.vals {
		out = append(out, domain.MaterializedSecret{Name: k, Value: v})
	}
	return out, nil
}

type extRtSubs struct {
	repository.ExtensionEventSubscriptionRepository
	mu   sync.Mutex
	subs []domain.ExtensionEventSubscription
}

func (m *extRtSubs) Create(_ context.Context, s *domain.ExtensionEventSubscription) (*domain.ExtensionEventSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, *s)
	return s, nil
}

func (m *extRtSubs) ListByConnection(_ context.Context, id uuid.UUID) ([]domain.ExtensionEventSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ExtensionEventSubscription
	for _, s := range m.subs {
		if s.ConnectionID == id {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *extRtSubs) ListByEvent(_ context.Context, ev string) ([]domain.ExtensionEventSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ExtensionEventSubscription
	for _, s := range m.subs {
		if s.Event == ev {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *extRtSubs) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.subs {
		if s.ID == id {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			return nil
		}
	}
	return nil
}

type externalRig struct {
	reg     *extensions.Registry
	rt      *external.Runtime
	inst    *extRtInstalled
	conns   extRtConns
	subs    *extRtSubs
	secrets *extRtSecrets
	wsvc    *ExtensionWorkspaceService
	hook    *external.Hook
	ws      uuid.UUID
}

func newExternalRig(t *testing.T) *externalRig {
	t.Helper()
	r := &externalRig{
		reg: extensions.NewRegistry(), inst: &extRtInstalled{}, subs: &extRtSubs{}, secrets: &extRtSecrets{}, ws: uuid.New(),
		conns: extRtConns{exttest.NewMemIntegrations()},
	}
	hs := &external.Handshaker{Client: &http.Client{Timeout: 3 * time.Second}, Conns: r.conns, Secrets: r.secrets, Reader: r.secrets, Subs: r.subs}
	deps := extensions.Deps{Conns: extensions.IntegrationConnections{Repo: r.conns}}
	r.rt = &external.Runtime{Reg: r.reg, Installed: r.inst, Deps: deps, Client: &http.Client{Timeout: 3 * time.Second}, HS: hs}
	r.hook = &external.Hook{Reg: r.reg, Conns: r.conns, HS: hs}
	consents := &exttest.MemConsents{Integ: r.conns.MemIntegrations, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	r.reg.WithConsentStore(extensions.IntegrationConsents{Repo: consents, Declared: func(string) []string { return nil }})
	r.wsvc = NewExtensionWorkspaceService(r.reg, r.conns, consents).WithExternalHook(r.hook)
	return r
}

func (r *externalRig) connect(t *testing.T, url string) {
	t.Helper()
	p, ok := r.reg.Get("hello")
	require.True(t, ok)
	_, err := r.wsvc.Connect(context.Background(), r.ws, "hello", nil,
		map[string]any{"hello_url": url}, extensions.DeclaredCapabilities(p.Manifest().Capabilities))
	require.NoError(t, err)
}

func (r *externalRig) health(t *testing.T) extensions.Status {
	t.Helper()
	st, err := r.reg.TestConnection(context.Background(), r.ws, "hello")
	require.NoError(t, err)
	return st
}

// dispatcher is the real webhook service over the rig's subscription and
// secret stores; the dial guard is opened because the receiver is on loopback.
func (r *externalRig) dispatcher() (*webhookService, *recordingWebhookRepo) {
	repo := &recordingWebhookRepo{}
	return &webhookService{
		repo:     repo,
		client:   newWebhookHTTPClient(func(net.IP) bool { return true }),
		backoffs: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
		ext:      &extensionDelivery{subs: r.subs, integrations: r.conns, secrets: r.secrets},
	}, repo
}

func extRtWaitDeliveries(t *testing.T, repo *recordingWebhookRepo, n int) []*domain.WebhookDelivery {
	t.Helper()
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.deliveries) >= n
	}, 5*time.Second, 5*time.Millisecond)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return append([]*domain.WebhookDelivery(nil), repo.deliveries...)
}

// Acceptance: install mesh-ext-hello 0.1.0, the handshake passes and a
// task.updated event reaches the service with a valid HMAC.
func TestExternalRuntime_InstallHandshakeAndSignedEvent(t *testing.T) {
	r := newExternalRig(t)
	hello := newHelloService(t, "")

	r.inst.put("hello", helloManifest, false)
	require.NoError(t, r.rt.Sync(context.Background()))
	_, registered := r.reg.Get("hello")
	require.True(t, registered, "an installed repo extension is a registered provider")

	r.connect(t, hello.srv.URL)

	installs, _, _ := hello.snapshot()
	assert.Equal(t, 1, installs, "install handshake reached the service")
	hello.mu.Lock()
	assert.Equal(t, r.ws.String(), hello.installed["workspace_id"])
	assert.Equal(t, "0.1.0", hello.installed["version"])
	assert.NotEmpty(t, hello.installed["signing_secret"])
	hello.mu.Unlock()

	conn := r.conns.Find(r.ws, "hello")
	require.NotNil(t, conn)
	subs, _ := r.subs.ListByConnection(context.Background(), conn.ID)
	require.Len(t, subs, 1)
	assert.Equal(t, "task.updated", subs[0].Event)
	assert.Equal(t, hello.srv.URL+"/mesh/events", subs[0].Target)

	disp, repo := r.dispatcher()
	disp.Dispatch(context.Background(), r.ws, "task.updated", map[string]any{"task_id": "t1"})
	ds := extRtWaitDeliveries(t, repo, 1)
	assert.True(t, ds[0].Success, "service accepted the signed delivery")
	_, rejected, events := hello.snapshot()
	assert.Equal(t, []string{"task.updated"}, events)
	assert.Zero(t, rejected)

	st := r.health(t)
	assert.True(t, st.Health.OK, "healthy while the handshake holds: %q", st.Health.Message)
}

// Red control: a service that already holds another signing secret refuses the
// handshake (a different secret needs the old signature) -> degraded with the
// reason, no subscription is created, so nothing is silently half-wired. Once
// the service accepts, the next health check completes the handshake and
// deliveries flow.
func TestExternalRuntime_WrongSecretIsDegradedAndRecovers(t *testing.T) {
	r := newExternalRig(t)
	hello := newHelloService(t, "someone-elses-secret")
	r.inst.put("hello", helloManifest, false)
	require.NoError(t, r.rt.Sync(context.Background()))

	r.connect(t, hello.srv.URL)

	st := r.health(t)
	assert.False(t, st.Health.OK)
	assert.Contains(t, st.Health.Message, "install handshake failed")
	assert.Contains(t, st.Health.Message, "401")
	conn := r.conns.Find(r.ws, "hello")
	subs, _ := r.subs.ListByConnection(context.Background(), conn.ID)
	assert.Empty(t, subs, "no subscription until the handshake passes")
	_, rejected, _ := hello.snapshot()
	assert.Positive(t, rejected)

	hello.mu.Lock()
	hello.secret = "" // the operator resets the service
	hello.mu.Unlock()
	st, err := r.reg.TestConnection(context.Background(), r.ws, "hello")
	require.NoError(t, err)
	assert.True(t, st.Health.OK, st.Health.Message)
	subs, _ = r.subs.ListByConnection(context.Background(), conn.ID)
	assert.Len(t, subs, 1)
}

// Red control: the service goes away after the handshake. The extension shows
// degraded and the event is recorded as a failed delivery (all attempts), not
// dropped.
func TestExternalRuntime_UnreachableServiceDegradedEventNotLost(t *testing.T) {
	r := newExternalRig(t)
	hello := newHelloService(t, "")
	r.inst.put("hello", helloManifest, false)
	require.NoError(t, r.rt.Sync(context.Background()))
	r.connect(t, hello.srv.URL)
	hello.srv.Close()

	st := r.health(t)
	assert.False(t, st.Health.OK)
	assert.Contains(t, st.Health.Message, "unreachable")

	disp, repo := r.dispatcher()
	disp.Dispatch(context.Background(), r.ws, "task.updated", map[string]any{"task_id": "t2"})
	ds := extRtWaitDeliveries(t, repo, 1)
	assert.False(t, ds[0].Success)
	assert.Equal(t, 4, ds[0].Attempt, "all attempts were made and the failure is on record")
}

// Red control: a receiver that checks the signature rejects a corrupted one,
// so the green test above is not a receiver that accepts anything.
func TestExternalRuntime_ReceiverRejectsBadSignature(t *testing.T) {
	hello := newHelloService(t, "s3cret")
	body := []byte(`{}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	for name, sig := range map[string]string{"wrong secret": external.Sign("other", ts, body), "no timestamp binding": external.Sign("s3cret", "", body)} {
		req, _ := http.NewRequest(http.MethodPost, hello.srv.URL+"/mesh/events", bytes.NewReader(body))
		req.Header.Set("X-Mesh-Timestamp", ts)
		req.Header.Set("X-Mesh-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
	}
}

// Sync follows installed_extensions: a new version replaces the provider, a
// blocked or removed row takes the extension out of the registry.
func TestExternalRuntime_SyncUpdateBlockRemove(t *testing.T) {
	r := newExternalRig(t)
	ctx := context.Background()

	r.inst.put("hello", helloManifest, false)
	require.NoError(t, r.rt.Sync(ctx))
	p, ok := r.reg.Get("hello")
	require.True(t, ok)
	assert.Equal(t, "0.1.0", p.Manifest().Version)

	newer := strings.ReplaceAll(helloManifest, `"version":"0.1.0"`, `"version":"0.2.0"`)
	r.inst.put("hello", newer, false)
	require.NoError(t, r.rt.Sync(ctx))
	p, _ = r.reg.Get("hello")
	assert.Equal(t, "0.2.0", p.Manifest().Version, "update swapped the provider")

	r.inst.put("hello", newer, true)
	require.NoError(t, r.rt.Sync(ctx))
	_, ok = r.reg.Get("hello")
	assert.False(t, ok, "a blocked extension is unregistered")

	r.inst.put("hello", newer, false)
	require.NoError(t, r.rt.Sync(ctx))
	_, ok = r.reg.Get("hello")
	require.True(t, ok)
	r.inst.drop("hello")
	require.NoError(t, r.rt.Sync(ctx))
	_, ok = r.reg.Get("hello")
	assert.False(t, ok, "a removed extension is unregistered")
}

// A repo row cannot take over a builtin id.
func TestExternalRuntime_DoesNotShadowBuiltin(t *testing.T) {
	r := newExternalRig(t)
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	r.reg = f.Registry
	r.rt.Reg = f.Registry
	r.inst.put("demo", strings.ReplaceAll(helloManifest, `"id":"hello"`, `"id":"demo"`), false)
	require.NoError(t, r.rt.Sync(context.Background()))
	p, _ := f.Registry.Get("demo")
	assert.Equal(t, "builtin", p.Manifest().Runtime)
}
