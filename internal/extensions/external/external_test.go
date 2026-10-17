package external

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const manifestJSON = `{"schema":1,"id":"hello","name":"Hello","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated","not.public"],"ui":[]},
 "external":{"base_url_setting":"hello_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}`

// svc is a stand-in external service: install keeps the secret, a different one
// needs the old signature; health answers healthStatus.
type svc struct {
	mu           sync.Mutex
	secret       string
	installs     int
	healthStatus int
	installCode  int
	srv          *httptest.Server
}

func newSvc(t *testing.T, preset string) *svc {
	t.Helper()
	s := &svc{secret: preset, healthStatus: 200}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *svc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(s.healthStatus)
	case "/mesh/install":
		var in installRequest
		_ = json.Unmarshal(body, &in)
		if s.installCode != 0 {
			w.WriteHeader(s.installCode)
			return
		}
		if s.secret != "" && in.SigningSecret != s.secret &&
			r.Header.Get("X-Mesh-Signature") != Sign(s.secret, r.Header.Get("X-Mesh-Timestamp"), body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if _, err := strconv.ParseInt(r.Header.Get("X-Mesh-Timestamp"), 10, 64); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.secret = in.SigningSecret
		s.installs++
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type fakeConns struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.IntegrationConfig
	err  error
}

func (f *fakeConns) GetByProvider(_ context.Context, ws uuid.UUID, p domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	for _, r := range f.rows {
		if r.WorkspaceID == ws && r.Provider == p {
			return r, nil
		}
	}
	return nil, nil
}

func (f *fakeConns) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[id], nil
}

func (f *fakeConns) Connection(ctx context.Context, ws uuid.UUID, ext string) (*extensions.Connection, error) {
	r, err := f.GetByProvider(ctx, ws, domain.IntegrationProvider(ext))
	if err != nil || r == nil {
		return nil, err
	}
	var settings map[string]any
	_ = json.Unmarshal(r.Config, &settings)
	return &extensions.Connection{Settings: settings, IsActive: r.IsActive}, nil
}

type fakeSecrets struct {
	mu        sync.Mutex
	vals      map[string]string
	createErr error
	readErr   error
	expired   bool
	hide      bool // Create succeeds but the value never reads back
}

func (s *fakeSecrets) Create(_ context.Context, in domain.CreateSecretInput) (domain.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return domain.Secret{}, s.createErr
	}
	if s.vals == nil {
		s.vals = map[string]string{}
	}
	s.vals[in.Name] = in.Value
	return domain.Secret{Name: in.Name}, nil
}

func (s *fakeSecrets) ResolveCurrentValues(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.hide {
		return nil, nil
	}
	var out []domain.MaterializedSecret
	for k, v := range s.vals {
		out = append(out, domain.MaterializedSecret{Name: k, Value: v, Expired: s.expired})
	}
	return out, nil
}

type fakeSubs struct {
	mu   sync.Mutex
	subs []domain.ExtensionEventSubscription
	err  error
}

func (f *fakeSubs) Create(_ context.Context, s *domain.ExtensionEventSubscription) (*domain.ExtensionEventSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.subs = append(f.subs, *s)
	return s, nil
}

func (f *fakeSubs) ListByConnection(_ context.Context, id uuid.UUID) ([]domain.ExtensionEventSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []domain.ExtensionEventSubscription
	for _, s := range f.subs {
		if s.ConnectionID == id {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSubs) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for i, s := range f.subs {
		if s.ID == id {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
		}
	}
	return nil
}

type rig struct {
	conns   *fakeConns
	secrets *fakeSecrets
	subs    *fakeSubs
	hs      *Handshaker
	p       *Provider
	ws      uuid.UUID
	connID  uuid.UUID
}

func newRig(t *testing.T, url string) *rig {
	t.Helper()
	r := &rig{conns: &fakeConns{rows: map[uuid.UUID]*domain.IntegrationConfig{}}, secrets: &fakeSecrets{}, subs: &fakeSubs{}, ws: uuid.New(), connID: uuid.New()}
	cfg, _ := json.Marshal(map[string]any{"hello_url": url})
	r.conns.rows[r.connID] = &domain.IntegrationConfig{ID: r.connID, WorkspaceID: r.ws, Provider: "hello", Config: cfg, IsActive: true}
	client := &http.Client{Timeout: 3 * time.Second}
	r.hs = &Handshaker{Client: client, Conns: r.conns, Secrets: r.secrets, Reader: r.secrets, Subs: r.subs}
	m, err := extensions.ParseManifest([]byte(manifestJSON))
	require.NoError(t, err)
	r.p, err = NewProvider(m, extensions.Deps{Conns: r.conns}, client, r.hs)
	require.NoError(t, err)
	return r
}

func (r *rig) settings(url string) map[string]any { return map[string]any{"hello_url": url} }

func TestHandshake_InstallsAndSubscribesPublicEventsOnly(t *testing.T) {
	s := newSvc(t, "")
	r := newRig(t, s.srv.URL)
	ctx := context.Background()

	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	assert.Equal(t, 1, s.installs)
	assert.Equal(t, r.secrets.vals[SecretName("hello")], s.secret, "the service holds the stored signing secret")
	require.Len(t, r.subs.subs, 1, "the non-public event is not subscribed")
	assert.Equal(t, "task.updated", r.subs.subs[0].Event)
	assert.Equal(t, s.srv.URL+"/mesh/events", r.subs.subs[0].Target)
	assert.Equal(t, SecretName("hello"), r.subs.subs[0].SecretRef)

	// Ensure is memoised: no second install; Forget makes it talk again, with the same secret and no duplicate subscription.
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	assert.Equal(t, 1, s.installs)
	r.hs.Forget(r.ws, "hello")
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	assert.Equal(t, 2, s.installs)
	assert.Len(t, r.subs.subs, 1)
}

func TestHandshake_SecretRotationForcesNewHandshake(t *testing.T) {
	s := newSvc(t, "")
	r := newRig(t, s.srv.URL)
	ctx := context.Background()
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	require.Equal(t, 1, s.installs)

	r.secrets.mu.Lock()
	r.secrets.vals[SecretName("hello")] = "rotated-secret-value"
	r.secrets.mu.Unlock()
	s.mu.Lock()
	s.secret = "" // the service accepts a re-key
	s.mu.Unlock()

	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	assert.Equal(t, 2, s.installs, "a rotated secret is sent to the service again")
	assert.Equal(t, "rotated-secret-value", s.secret)
}

func TestHandshake_AddressChangeReplacesStaleSubscription(t *testing.T) {
	a, b := newSvc(t, ""), newSvc(t, "")
	r := newRig(t, a.srv.URL)
	ctx := context.Background()
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(a.srv.URL)))
	// Another subscription of the same connection that is not ours stays.
	r.subs.subs = append(r.subs.subs, domain.ExtensionEventSubscription{ID: uuid.New(), ConnectionID: r.connID, Event: "task.created", Delivery: domain.ExtensionDeliveryNATS})

	b.secret = r.secrets.vals[SecretName("hello")]
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(b.srv.URL)))
	var targets []string
	for _, sub := range r.subs.subs {
		if sub.Delivery == domain.ExtensionDeliveryWebhook {
			targets = append(targets, sub.Target)
		}
	}
	assert.Equal(t, []string{b.srv.URL + "/mesh/events"}, targets)
	assert.Len(t, r.subs.subs, 2, "the unrelated nats subscription is kept")
}

func TestHandshake_Failures(t *testing.T) {
	ctx := context.Background()
	t.Run("service holds another secret", func(t *testing.T) {
		s := newSvc(t, "other")
		r := newRig(t, s.srv.URL)
		err := r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
		assert.Empty(t, r.subs.subs)
		assert.Error(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)), "a failed handshake is retried, not memoised")
	})
	t.Run("service unreachable", func(t *testing.T) {
		s := newSvc(t, "")
		r := newRig(t, s.srv.URL)
		s.srv.Close()
		err := r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unreachable")
	})
	t.Run("bad address", func(t *testing.T) {
		r := newRig(t, "http://x")
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings("ftp://x")))
		assert.Error(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings("")))
	})
	t.Run("connection missing or off", func(t *testing.T) {
		s := newSvc(t, "")
		r := newRig(t, s.srv.URL)
		r.conns.rows[r.connID].IsActive = false
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
		r.conns.rows = map[uuid.UUID]*domain.IntegrationConfig{}
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
		r.conns.err = errors.New("db down")
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	})
	t.Run("secret store problems", func(t *testing.T) {
		s := newSvc(t, "")
		r := newRig(t, s.srv.URL)
		r.secrets.readErr = errors.New("boom")
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
		r.secrets.readErr = nil
		r.secrets.createErr = errors.New("boom")
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
		r.secrets.createErr = apierror.Conflict("exists")
		r.secrets.hide = true
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)), "created but not readable back")
		r.secrets.createErr, r.secrets.hide = nil, false
		r.secrets.vals = map[string]string{SecretName("hello"): "v"}
		r.secrets.expired = true
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)), "an expired secret is not used")
	})
	t.Run("install endpoint answers 500", func(t *testing.T) {
		s := newSvc(t, "")
		s.installCode = 500
		r := newRig(t, s.srv.URL)
		err := r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})
	t.Run("subscription store fails", func(t *testing.T) {
		s := newSvc(t, "")
		r := newRig(t, s.srv.URL)
		r.subs.err = errors.New("db down")
		assert.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	})
}

func TestHandshake_LostCreateRaceUsesStoredSecret(t *testing.T) {
	s := newSvc(t, "")
	r := newRig(t, s.srv.URL)
	r.secrets.createErr = apierror.Conflict("exists")
	// The race winner's value shows up on read-back.
	r.secrets.vals = map[string]string{}
	// First read is empty (not stored yet), create conflicts, then the winner's value is there.
	r.secrets.vals[SecretName("hello")] = "winner"
	require.NoError(t, r.hs.Run(context.Background(), r.p, r.ws, r.settings(s.srv.URL)))
	assert.Equal(t, "winner", s.secret)
}

func TestProvider_ValidateAndHealth(t *testing.T) {
	s := newSvc(t, "")
	r := newRig(t, s.srv.URL)
	ctx := context.Background()

	assert.NoError(t, r.p.Validate(extensions.LevelWorkspace, r.settings(s.srv.URL)))
	assert.Error(t, r.p.Validate(extensions.LevelWorkspace, map[string]any{}))
	assert.Error(t, r.p.Validate(extensions.LevelWorkspace, r.settings("https://user:pw@host")))
	assert.Error(t, r.p.Validate(extensions.LevelInstance, r.settings(s.srv.URL)))
	assert.True(t, r.p.IsExternal())

	res := extensions.Resolved{ExtensionID: "hello", Enabled: true, WorkspaceID: r.ws, Settings: r.settings(s.srv.URL)}
	assert.True(t, r.p.Health(ctx, res).OK)
	assert.Equal(t, 1, s.installs, "health completed the handshake")

	s.mu.Lock()
	s.healthStatus = 503
	s.mu.Unlock()
	rep := r.p.Health(ctx, res)
	assert.False(t, rep.OK)
	assert.Contains(t, rep.Message, "503")

	assert.False(t, r.p.Health(ctx, extensions.Resolved{}).OK, "disabled: nothing to probe")
	rep = r.p.Health(ctx, extensions.Resolved{Enabled: true, Settings: map[string]any{}})
	assert.False(t, rep.OK)
	s.srv.Close()
	rep = r.p.Health(ctx, extensions.Resolved{Enabled: true, Settings: r.settings(s.srv.URL)})
	assert.Contains(t, rep.Message, "unreachable")
}

func TestNewProvider_RejectsNonExternal(t *testing.T) {
	m, _ := extensions.ParseManifest([]byte(`{"id":"x","version":"1","runtime":"builtin"}`))
	_, err := NewProvider(m, extensions.Deps{}, http.DefaultClient, nil)
	assert.Error(t, err)
	m, _ = extensions.ParseManifest([]byte(`{"id":"x","version":"1","runtime":"external","external":{}}`))
	_, err = NewProvider(m, extensions.Deps{}, http.DefaultClient, nil)
	assert.Error(t, err)
}

func TestHook_SavedRunsHandshakeOnlyForActiveExternal(t *testing.T) {
	s := newSvc(t, "")
	r := newRig(t, s.srv.URL)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(r.p))
	h := &Hook{Reg: reg, Conns: r.conns, HS: r.hs}
	ctx := context.Background()

	require.NoError(t, h.Prepare(ctx, nil, nil))
	h.Saved(ctx, uuid.New()) // unknown connection: nothing
	assert.Zero(t, s.installs)
	h.Saved(ctx, r.connID)
	assert.Equal(t, 1, s.installs)

	r.conns.rows[r.connID].IsActive = false
	h.Saved(ctx, r.connID)
	assert.Equal(t, 1, s.installs, "a disabled connection is not installed")
	r.conns.rows[r.connID].IsActive = true

	r.conns.rows[r.connID].Config = json.RawMessage(`{`)
	h.Saved(ctx, r.connID)
	r.conns.rows[r.connID].Config = json.RawMessage(`{"hello_url":"ftp://x"}`)
	h.Saved(ctx, r.connID) // handshake error is logged, not fatal
	assert.Equal(t, 1, s.installs)

	reg.Unregister("hello")
	h.Saved(ctx, r.connID)
	h.Removed(ctx, r.connID)
	assert.Equal(t, 1, s.installs)
}

type fakeInstalled struct {
	mu   sync.Mutex
	rows []domain.InstalledExtension
	err  error
}

func (f *fakeInstalled) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeInstalled) UpsertBuiltin(context.Context, *domain.InstalledExtension) error { return nil }
func (f *fakeInstalled) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return nil, nil
}
func (f *fakeInstalled) List(context.Context) ([]domain.InstalledExtension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.InstalledExtension(nil), f.rows...), f.err
}

func TestRuntime_SyncAndRun(t *testing.T) {
	ctx := context.Background()
	inst := &fakeInstalled{}
	reg := extensions.NewRegistry()
	rt := &Runtime{Reg: reg, Installed: inst, Client: http.DefaultClient}
	row := func(id, manifest string, blocked bool, src domain.ExtensionSource) domain.InstalledExtension {
		return domain.InstalledExtension{ExtensionID: id, Manifest: json.RawMessage(manifest), Source: src, Blocked: blocked}
	}

	inst.rows = []domain.InstalledExtension{
		row("hello", manifestJSON, false, domain.ExtensionSourceRepo),
		row("bad", `{`, false, domain.ExtensionSourceRepo),
		row("builtinish", `{"id":"b","version":"1","runtime":"builtin"}`, false, domain.ExtensionSourceRepo),
		row("noext", `{"id":"noext","version":"1","runtime":"external"}`, false, domain.ExtensionSourceRepo),
		row("cat", manifestJSON, false, domain.ExtensionSourceCatalog),
	}
	require.NoError(t, rt.Sync(ctx))
	ids := []string{}
	for _, p := range reg.List() {
		ids = append(ids, p.Manifest().ID)
	}
	assert.Equal(t, []string{"hello"}, ids, "only a valid external repo row is registered")

	require.NoError(t, rt.Sync(ctx), "unchanged manifest: nothing re-registered")
	inst.rows[0].Manifest = json.RawMessage(`{"schema":1,"id":"hello","name":"Hello","version":"0.2.0","kind":"plugin","runtime":"external","mesh_api":">=1","publisher":"e","category":"development","levels":{"workspace":true},"capabilities":{"events":["task.updated"]},"external":{"base_url_setting":"hello_url","health_path":"/healthz"}}`)
	require.NoError(t, rt.Sync(ctx))
	p, _ := reg.Get("hello")
	assert.Equal(t, "0.2.0", p.Manifest().Version)

	inst.err = errors.New("db down")
	assert.Error(t, rt.Sync(ctx))
	inst.err = nil
	inst.rows[0].Blocked = true
	require.NoError(t, rt.Sync(ctx))
	_, ok := reg.Get("hello")
	assert.False(t, ok)

	inst.rows[0].Blocked = false
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { rt.Run(runCtx, 10*time.Millisecond); close(done) }()
	require.Eventually(t, func() bool { _, ok := reg.Get("hello"); return ok }, 2*time.Second, 5*time.Millisecond, "Run picks the row up")
	inst.setErr(errors.New("db down"))
	time.Sleep(30 * time.Millisecond) // a failing tick is logged, the loop goes on
	cancel()
	<-done
}

func TestCleanBaseURL_HTTPSOrLoopback(t *testing.T) {
	for _, ok := range []string{
		"https://example.com", "https://example.com/", "http://127.0.0.1:8080", "http://localhost:9000",
		"http://[::1]:7000", "http://127.9.9.9", "HTTP://LOCALHOST:1",
	} {
		_, err := cleanBaseURL(ok)
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{
		"http://example.com", "http://localhost.evil.com", "http://127.0.0.1@evil.com", "http://127.0.0.1.evil.com",
		"http://203.0.113.7", "http://[fd00::5]", "http://[::2]", "ftp://localhost", "https://u:p@example.com", "https://", "", "localhost:8080",
	} {
		_, err := cleanBaseURL(bad)
		assert.Error(t, err, bad)
	}
}

// secret-36533 and secret-103289 share the first 4 bytes of their SHA-256: a
// truncated digest in the fingerprint would treat them as the same secret.
func TestFingerprint_DistinguishesSecretsWithCollidingShortDigest(t *testing.T) {
	m := extensions.Manifest{Version: "0.1.0"}
	a := fingerprint(m, "https://svc.example", "secret-36533")
	b := fingerprint(m, "https://svc.example", "secret-103289")
	assert.NotEqual(t, a, b)
	assert.NotContains(t, a, "secret-36533", "the fingerprint never carries the secret")
}
