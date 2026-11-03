package external

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

type fakeInst struct {
	set *extensions.InstanceSetting
	err error
}

func (f fakeInst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return f.set, f.err
}

// manifestFor is the shape of a catalog external extension whose only setting
// is the service address; keyName is its base_url_setting.
func manifestFor(id, keyName string) string {
	return manifestWith(id, keyName, "")
}

// manifestWith adds a workspace settings block (extra) next to the address key;
// empty extra is the shape with no workspace fields at all.
func manifestWith(id, keyName, extra string) string {
	return `{"schema":1,"id":"` + id + `","name":"` + id + `","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":true,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":[],"ui":[]},
 ` + extra + `"external":{"base_url_setting":"` + keyName + `","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}`
}

// TestInheritInstance_ConnectMakesHandshakeSucceed: an extension configured only
// at instance level reports "not connected" (red), and after the workspace
// connect with settings inherited from the instance it is healthy for both
// shapes (keep: own key name; rotator: no workspace fields at all).
func TestInheritInstance_ConnectMakesHandshakeSucceed(t *testing.T) {
	// keep: own address key and a workspace settings block (own-credentials flag);
	// rotator: another address key and no workspace fields at all.
	keepSettings := `"settings":{"workspace":{"type":"object","properties":{"own_credentials":{"type":"boolean"}}}},`
	for _, tc := range []struct{ id, key, extra string }{
		{"keep", "keep_url", keepSettings},
		{"keep-rotator", "rotator_url", ""},
	} {
		t.Run(tc.id, func(t *testing.T) {
			s := newSvc(t, "")
			client := &http.Client{Timeout: 3 * time.Second}
			conns := &fakeConns{rows: map[uuid.UUID]*domain.IntegrationConfig{}}
			secrets := &fakeSecrets{}
			hs := &Handshaker{Client: client, Conns: conns, Secrets: secrets, Reader: secrets, Subs: &fakeSubs{}}
			m, err := extensions.ParseManifest([]byte(manifestWith(tc.id, tc.key, tc.extra)))
			require.NoError(t, err)
			inst := fakeInst{set: &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{tc.key: s.srv.URL, "admin_token": "CANARY-instance-only"}}}
			p, err := NewProvider(m, extensions.Deps{Conns: conns, Inst: inst}, client, hs)
			require.NoError(t, err)
			ws, ctx := uuid.New(), context.Background()

			// instance-only: resolves from the instance level, handshake refuses (no connection)
			res, err := p.Resolve(ctx, ws)
			require.NoError(t, err)
			res.WorkspaceID = ws
			rep := p.Health(ctx, res)
			assert.False(t, rep.OK)
			assert.Contains(t, rep.Message, "not connected in this workspace")

			// the normal connect: empty submitted settings, inherited from the instance
			settings := map[string]any{}
			require.NoError(t, p.InheritInstance(ctx, settings))
			require.NoError(t, p.Validate(extensions.LevelWorkspace, settings))
			assert.Equal(t, s.srv.URL, settings[tc.key])
			assert.NotContains(t, settings, "admin_token", "only the service address is inherited")
			raw, _ := json.Marshal(settings)
			id := uuid.New()
			conns.rows[id] = &domain.IntegrationConfig{ID: id, WorkspaceID: ws, Provider: domain.IntegrationProvider(tc.id), Config: raw, IsActive: true}

			res, err = p.Resolve(ctx, ws)
			require.NoError(t, err)
			res.WorkspaceID = ws
			assert.Equal(t, extensions.SourceWorkspace, res.Source)
			rep = p.Health(ctx, res)
			assert.True(t, rep.OK, rep.Message)
			assert.Empty(t, rep.Message)
			conn, err := conns.GetByProvider(ctx, ws, domain.IntegrationProvider(tc.id))
			require.NoError(t, err)
			require.NotNil(t, conn)
			assert.True(t, conn.IsActive)
			assert.Equal(t, 1, s.installs)
			assert.False(t, strings.Contains(string(s.lastBody), "CANARY-instance-only"), "instance-only value never reaches the service")
		})
	}
}

func TestInheritInstance_Negative(t *testing.T) {
	m, err := extensions.ParseManifest([]byte(manifestFor("keep", "keep_url")))
	require.NoError(t, err)
	mk := func(inst extensions.InstanceStore) *Provider {
		p, perr := NewProvider(m, extensions.Deps{Inst: inst}, &http.Client{}, nil)
		require.NoError(t, perr)
		return p
	}
	ctx := context.Background()
	on := &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": "https://keep.example.test"}}

	got := map[string]any{"keep_url": "https://own.example.test"}
	require.NoError(t, mk(fakeInst{set: on}).InheritInstance(ctx, got))
	assert.Equal(t, "https://own.example.test", got["keep_url"], "a submitted address wins")

	for name, p := range map[string]*Provider{
		"disabled instance": mk(fakeInst{set: &extensions.InstanceSetting{Enabled: false, Settings: on.Settings}}),
		"no instance row":   mk(fakeInst{}),
		"no store":          mk(nil),
	} {
		s := map[string]any{}
		require.NoError(t, p.InheritInstance(ctx, s), name)
		assert.Error(t, p.Validate(extensions.LevelWorkspace, s), name+": connect still needs an address")
	}

	bad := map[string]any{}
	assert.Error(t, mk(fakeInst{err: errors.New("db down")}).InheritInstance(ctx, bad))

	httpInst := map[string]any{}
	require.NoError(t, mk(fakeInst{set: &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": "http://evil.example.test"}}}).InheritInstance(ctx, httpInst))
	assert.Error(t, mk(nil).Validate(extensions.LevelWorkspace, httpInst), "an inherited address passes the same https check")
}

// TestInheritInstance_InvalidAddressAndTypes: inheritance never repairs an address the
// workspace-level validation refuses.
func TestInheritInstance_InvalidAddressAndTypes(t *testing.T) {
	m, err := extensions.ParseManifest([]byte(manifestFor("keep", "keep_url")))
	require.NoError(t, err)
	on := fakeInst{set: &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": "https://keep.example.test"}}}
	p, err := NewProvider(m, extensions.Deps{Inst: on}, &http.Client{}, nil)
	require.NoError(t, err)
	ctx := context.Background()

	// a submitted address that fails validation stays as sent and is refused
	for _, bad := range []string{"http://evil.example.test", "https://user:pw@host.example.test", "ftp://x.example.test"} {
		s := map[string]any{"keep_url": bad}
		require.NoError(t, p.InheritInstance(ctx, s))
		assert.Equal(t, bad, s["keep_url"], "submitted value is never replaced")
		assert.Error(t, p.Validate(extensions.LevelWorkspace, s), bad)
	}

	// a malformed submitted address is never replaced by the instance one
	for name, v := range map[string]any{"number": 5, "object": map[string]any{"a": 1}, "bool": true} {
		p, perr := NewProvider(m, extensions.Deps{Inst: on}, &http.Client{}, nil)
		require.NoError(t, perr)
		s := map[string]any{"keep_url": v}
		require.NoError(t, p.InheritInstance(ctx, s), name)
		assert.Equal(t, v, s["keep_url"], name+": submitted value kept")
		assert.Error(t, p.Validate(extensions.LevelWorkspace, s), name)
	}
	// null and blank submissions do inherit
	for name, v := range map[string]any{"null": nil, "blank": "  "} {
		p, perr := NewProvider(m, extensions.Deps{Inst: on}, &http.Client{}, nil)
		require.NoError(t, perr)
		s := map[string]any{"keep_url": v}
		require.NoError(t, p.InheritInstance(ctx, s), name)
		assert.Equal(t, "https://keep.example.test", s["keep_url"], name)
	}

	// a wrongly typed or empty instance value is not copied, so connect is refused
	for name, v := range map[string]any{"number": 5, "empty": "", "object": map[string]any{"a": 1}} {
		q, qerr := NewProvider(m, extensions.Deps{Inst: fakeInst{set: &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"keep_url": v}}}}, &http.Client{}, nil)
		require.NoError(t, qerr)
		s := map[string]any{}
		require.NoError(t, q.InheritInstance(ctx, s), name)
		assert.NotContains(t, s, "keep_url", name)
		assert.Error(t, q.Validate(extensions.LevelWorkspace, s), name)
	}
}
