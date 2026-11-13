package external

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/service"
)

func helloRelease(version string, api, events []string) string {
	a, _ := json.Marshal(api)
	e, _ := json.Marshal(events)
	return `{"schema":1,"id":"hello","name":"Hello","version":"` + version + `","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":` + string(a) + `,"events":` + string(e) + `,"ui":[]},
 "external":{"base_url_setting":"hello_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}`
}

var (
	hello03 = helloRelease("0.3.0", []string{"tasks:read"}, []string{"task.updated"})
	hello04 = helloRelease("0.4.0", []string{"tasks:read", "tasks:write"}, []string{"task.created", "task.updated"})
)

// installedReader serves the installed rows the consent transaction also reads.
type installedReader struct{ c *exttest.MemConsents }

func (r installedReader) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	return r.c.Installed[id], nil
}

func repoRow(manifest, version, commit string) *domain.InstalledExtension {
	return &domain.InstalledExtension{ExtensionID: "hello", Version: version, Manifest: json.RawMessage(manifest),
		Source: domain.ExtensionSourceRepo, CommitSHA: commit}
}

func subscribed(f *fakeSubs) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.subs {
		out = append(out, s.Event)
	}
	slices.Sort(out)
	return out
}

func announced(s *svc) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var in installRequest
	_ = json.Unmarshal(s.lastBody, &in)
	slices.Sort(in.Events)
	return in.Events
}

// 0.3 -> 0.4 of an external extension without a disconnect, end to end over the
// real workspace service, provider, hook and handshaker:
//   - after the instance update the handshake re-runs, but the event 0.4 added
//     is neither announced nor subscribed (the hole: it used to be);
//   - the connection view lists exactly the added rights for 0.4 / its commit;
//   - the consent is recorded even though the service is down at that moment,
//     the actor snapshot becomes 0.4's declared set, and nothing else of the
//     connection changes;
//   - the next health check retries the handshake and only then subscribes the
//     added event.
func TestReconsent_ExternalUpdateEndToEnd(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t, "")
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	subs := &fakeSubs{}
	secrets := &fakeSecrets{}
	client := &http.Client{Timeout: 3 * time.Second}
	hs := &Handshaker{Client: client, Conns: f.Integ, Secrets: secrets, Reader: secrets, Subs: subs, Consents: f.Consents}
	deps := extensions.Deps{Conns: extensions.IntegrationConnections{Repo: f.Integ}, Allow: f.Allow}
	provider := func(raw string) *Provider {
		m, perr := extensions.ParseManifest([]byte(raw))
		require.NoError(t, perr)
		p, perr := NewProvider(m, deps, client, hs)
		require.NoError(t, perr)
		return p
	}
	require.NoError(t, f.Registry.Register(provider(hello03)))
	f.Consents.Installed = map[string]*domain.InstalledExtension{"hello": repoRow(hello03, "0.3.0", "c3")}
	ws := newRig(t, s.srv.URL).ws
	svcWS := service.NewExtensionWorkspaceService(f.Registry, f.Integ, f.Consents).
		WithExternalHook(&Hook{Reg: f.Registry, Conns: f.Integ, HS: hs}).
		WithInstalledReleases(installedReader{c: f.Consents})

	v, err := svcWS.Connect(ctx, ws, "hello", nil, map[string]any{"hello_url": s.srv.URL}, []string{"api:tasks:read", "events:task.updated"})
	require.NoError(t, err)
	require.False(t, v.Connection.NeedsConsent)
	require.Nil(t, v.Connection.PendingConsent)
	assert.Equal(t, "0.3.0", v.Connection.ConsentedVersion)
	assert.Equal(t, "c3", v.Connection.ConsentedCommit)
	require.Equal(t, []string{"task.updated"}, subscribed(subs))
	row := f.Integ.Find(ws, "hello")
	require.NotNil(t, row)
	connID, cfgBefore := row.ID, string(row.Config)

	// The instance updates to 0.4 (row first, then the runtime replaces the
	// provider) and the health check re-runs the handshake for the new version.
	f.Consents.Installed["hello"] = repoRow(hello04, "0.4.0", "c4")
	p04 := provider(hello04)
	require.NoError(t, f.Registry.Replace(p04))
	installsBefore := s.installs
	require.NoError(t, hs.Ensure(ctx, p04, ws, map[string]any{"hello_url": s.srv.URL}))
	require.Greater(t, s.installs, installsBefore, "the new version is handshaken")
	assert.Equal(t, []string{"task.updated"}, announced(s), "the added event is not announced before consent")
	assert.Equal(t, []string{"task.updated"}, subscribed(subs), "nor subscribed")

	list, err := svcWS.List(ctx, ws)
	require.NoError(t, err)
	var conn *service.ExtensionConnectionView
	for i := range list {
		if list[i].ID == "hello" {
			conn = list[i].Connection
		}
	}
	require.NotNil(t, conn)
	require.True(t, conn.NeedsConsent)
	require.NotNil(t, conn.PendingConsent)
	assert.Equal(t, service.PendingConsentView{Version: "0.4.0", Commit: "c4",
		AddedCapabilities: []string{"api:tasks:write", "events:task.created"}, AddedEvents: []string{"task.created"}}, *conn.PendingConsent)

	// The service is down when the admin agrees.
	s.mu.Lock()
	s.installCode = http.StatusServiceUnavailable
	s.mu.Unlock()
	v, err = svcWS.Reconsent(ctx, ws, "hello", nil, "0.4.0", "c4")
	require.NoError(t, err, "a failed handshake does not undo the consent")
	assert.False(t, v.Connection.NeedsConsent)
	assert.Equal(t, "0.4.0", v.Connection.ConsentedVersion)
	assert.Equal(t, "c4", v.Connection.ConsentedCommit)
	assert.Equal(t, []string{"api:tasks:read", "api:tasks:write", "events:task.created", "events:task.updated"}, v.Connection.ConsentedCapabilities)
	var snap map[string][]string
	require.NoError(t, json.Unmarshal(f.Consents.ActorCaps[connID], &snap))
	assert.Equal(t, map[string][]string{"api": {"tasks:read", "tasks:write"}, "events": {"task.created", "task.updated"}}, snap)
	assert.Equal(t, []string{"task.updated"}, subscribed(subs), "the failed handshake subscribed nothing new")
	after := f.Integ.Find(ws, "hello")
	assert.Equal(t, connID, after.ID, "same connection")
	assert.Equal(t, cfgBefore, string(after.Config), "settings untouched")
	assert.True(t, after.IsActive)
	assert.Equal(t, row.ProjectScope, after.ProjectScope, "project scope untouched")

	// The service is back: the next health check retries and subscribes.
	s.mu.Lock()
	s.installCode = 0
	s.mu.Unlock()
	require.NoError(t, hs.Ensure(ctx, p04, ws, map[string]any{"hello_url": s.srv.URL}))
	assert.Equal(t, []string{"task.created", "task.updated"}, announced(s))
	assert.Equal(t, []string{"task.created", "task.updated"}, subscribed(subs))
}

// A connection with no consent record is announced and subscribed to nothing,
// and a consent narrows the subscriptions to what it covers.
func TestHandshake_SubscribesOnlyConsentedEvents(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t, "")
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	subs := &fakeSubs{}
	secrets := &fakeSecrets{}
	client := &http.Client{Timeout: 3 * time.Second}
	hs := &Handshaker{Client: client, Conns: f.Integ, Secrets: secrets, Reader: secrets, Subs: subs, Consents: f.Consents}
	m, err := extensions.ParseManifest([]byte(hello04))
	require.NoError(t, err)
	p, err := NewProvider(m, extensions.Deps{Conns: extensions.IntegrationConnections{Repo: f.Integ}}, client, hs)
	require.NoError(t, err)
	ws := newRig(t, s.srv.URL).ws
	cfg, _ := json.Marshal(map[string]any{"hello_url": s.srv.URL})
	created, err := f.Integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: ws, WorkspaceID: ws, Provider: "hello", Config: cfg, IsActive: true})
	require.NoError(t, err)
	require.True(t, created)
	settings := map[string]any{"hello_url": s.srv.URL}

	require.NoError(t, hs.Run(ctx, p, ws, settings))
	assert.Empty(t, subscribed(subs), "no consent record: no events")
	assert.Empty(t, announced(s))

	f.Consents.ByID[ws] = domain.ExtensionConsent{Capabilities: []string{"api:tasks:read", "events:task.updated"}}
	require.NoError(t, hs.Ensure(ctx, p, ws, settings), "a changed consent changes the fingerprint")
	assert.Equal(t, []string{"task.updated"}, subscribed(subs))

	f.Consents.ByID[ws] = domain.ExtensionConsent{Capabilities: []string{"api:tasks:read", "events:task.created", "events:task.updated"}}
	require.NoError(t, hs.Ensure(ctx, p, ws, settings))
	assert.Equal(t, []string{"task.created", "task.updated"}, subscribed(subs))
	assert.True(t, strings.Contains(string(s.lastBody), `"task.created"`))
}

type failingConsents struct{ *exttest.MemConsents }

func (failingConsents) GetConsent(context.Context, uuid.UUID, string) (*domain.ExtensionConsent, error) {
	return nil, errors.New("db down")
}

// A failed consent lookup fails the handshake (no event list is guessed), on
// the direct run and on the health-check path alike.
func TestHandshake_ConsentLookupFails(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t, "")
	f, err := exttest.NewFixture(nil, false)
	require.NoError(t, err)
	subs := &fakeSubs{}
	secrets := &fakeSecrets{}
	client := &http.Client{Timeout: 3 * time.Second}
	hs := &Handshaker{Client: client, Conns: f.Integ, Secrets: secrets, Reader: secrets, Subs: subs, Consents: failingConsents{f.Consents}}
	m, err := extensions.ParseManifest([]byte(hello04))
	require.NoError(t, err)
	p, err := NewProvider(m, extensions.Deps{Conns: extensions.IntegrationConnections{Repo: f.Integ}}, client, hs)
	require.NoError(t, err)
	ws := newRig(t, s.srv.URL).ws
	cfg, _ := json.Marshal(map[string]any{"hello_url": s.srv.URL})
	_, err = f.Integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: ws, WorkspaceID: ws, Provider: "hello", Config: cfg, IsActive: true})
	require.NoError(t, err)
	settings := map[string]any{"hello_url": s.srv.URL}

	assert.EqualError(t, hs.Run(ctx, p, ws, settings), "consent lookup failed")
	assert.EqualError(t, hs.Ensure(ctx, p, ws, settings), "consent lookup failed")
	assert.Empty(t, subscribed(subs))
	assert.Zero(t, s.installs, "nothing is announced")
}

// A re-consent never touches the actor token, even when the token was never
// marked delivered (a full handshake would rotate and post it): the hook's
// Reconsented announces and aligns subscriptions only. The first delivery stays
// owed to the next ordinary handshake, as before the consent.
func TestReconsent_HandshakeKeepsUndeliveredToken(t *testing.T) {
	ctx := context.Background()
	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)
	tokens.current = "ext_EXISTING"
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(r.p))
	hook := &Hook{Reg: reg, Conns: r.conns, HS: r.hs}

	hook.Reconsented(ctx, r.connID)
	got, posts, _ := s.got()
	assert.Zero(t, posts, "no token posted")
	assert.Empty(t, got)
	assert.Equal(t, 0, tokens.seq, "not rotated")
	assert.Equal(t, "ext_EXISTING", tokens.current)
	assert.Nil(t, tokens.deliveredAt)
	assert.NotEmpty(t, s.secret, "the install was announced")
	require.Len(t, r.subs.subs, 1, "subscriptions aligned")

	// No memo was kept, so the next health check still owes the first delivery.
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	_, posts, _ = s.got()
	assert.Equal(t, 1, posts)

	// Without token_path nothing is owed: Refresh keeps the memo like Run, and
	// the next health check does not repeat the install.
	plain := newSvc(t, "")
	r2 := newRig(t, plain.srv.URL)
	require.NoError(t, r2.hs.Refresh(ctx, r2.p, r2.ws, r2.settings(plain.srv.URL)))
	require.Equal(t, 1, plain.installs)
	require.NoError(t, r2.hs.Ensure(ctx, r2.p, r2.ws, r2.settings(plain.srv.URL)))
	assert.Equal(t, 1, plain.installs)
}
