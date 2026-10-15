package teamrelay_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	teamrelay "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
)

// conns serves rows only under the legacy key, as integration_configs does,
// and records every key asked.
type conns struct {
	row  *extensions.Connection
	err  error
	keys *[]string
}

func (c conns) Connection(_ context.Context, _ uuid.UUID, id string) (*extensions.Connection, error) {
	if c.keys != nil {
		*c.keys = append(*c.keys, id)
	}
	if id != teamrelay.LegacyID {
		return nil, nil
	}
	return c.row, c.err
}

type inst struct{ s *extensions.InstanceSetting }

func (i inst) InstanceSetting(_ context.Context, id string) (*extensions.InstanceSetting, error) {
	if id != teamrelay.LegacyID {
		return nil, nil
	}
	return i.s, nil
}

func newProvider(c extensions.ConnectionStore, i extensions.InstanceStore) *teamrelay.Provider {
	return teamrelay.New(extensions.Deps{Conns: c, Inst: i})
}

func ws(url string) *extensions.Connection {
	return &extensions.Connection{IsActive: true, Settings: map[string]any{"relay_url": url}}
}

func instance(url string, enabled bool) inst {
	return inst{&extensions.InstanceSetting{Enabled: enabled, Settings: map[string]any{"relay_url": url}}}
}

func resolve(t *testing.T, p *teamrelay.Provider, w uuid.UUID) (relayURL, source string, ok bool) {
	t.Helper()
	return p.ResolveRelayURL(context.Background(), w)
}

func TestResolve_WorkspaceRowWinsOverInstance(t *testing.T) {
	p := newProvider(conns{row: ws("https://ws.example")}, instance("https://inst.example", true))
	u, src, ok := resolve(t, p, uuid.New())
	require.True(t, ok)
	require.Equal(t, "https://ws.example", u)
	require.Equal(t, "workspace", src)
}

func TestResolve_NoRow_UsesInstance(t *testing.T) {
	p := newProvider(conns{}, instance("https://inst.example", true))
	u, src, ok := resolve(t, p, uuid.New())
	require.True(t, ok)
	require.Equal(t, "https://inst.example", u)
	require.Equal(t, "instance", src)
}

// A workspace that switched Team Relay off stays off however the instance
// (env) is set: the bug #33a4bb57 AC2 fixed.
func TestResolve_InactiveRow_InstanceConfigured_StillOff(t *testing.T) {
	p := newProvider(conns{row: &extensions.Connection{IsActive: false}}, instance("https://inst.example", true))
	_, _, ok := resolve(t, p, uuid.New())
	require.False(t, ok)
}

func TestResolve_NeitherRowNorInstance_Off(t *testing.T) {
	_, _, ok := resolve(t, newProvider(conns{}, inst{}), uuid.New())
	require.False(t, ok)
}

// An active row with no relay_url carries nothing; it must not shadow the
// instance level.
func TestResolve_ActivePlaceholderRow_FallsToInstance(t *testing.T) {
	p := newProvider(conns{row: &extensions.Connection{IsActive: true, Settings: map[string]any{}}}, instance("https://inst.example", true))
	u, src, ok := resolve(t, p, uuid.New())
	require.True(t, ok)
	require.Equal(t, "https://inst.example", u)
	require.Equal(t, "instance", src)
}

func TestResolve_InstanceURLWithoutEnabled_Off(t *testing.T) {
	_, _, ok := resolve(t, newProvider(conns{}, instance("https://inst.example", false)), uuid.New())
	require.False(t, ok)
}

func TestResolve_InstanceEnabledWithoutURL_Off(t *testing.T) {
	p := newProvider(conns{}, instance("", true))
	_, _, ok := resolve(t, p, uuid.New())
	require.False(t, ok)
	res, err := p.Resolve(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, res.Enabled)
	require.Equal(t, extensions.SourceNone, res.Source)
}

func TestResolve_ConnectionError_FallsToInstance(t *testing.T) {
	p := newProvider(conns{err: errors.New("db down")}, instance("https://inst.example", true))
	u, src, ok := resolve(t, p, uuid.New())
	require.True(t, ok)
	require.Equal(t, "https://inst.example", u)
	require.Equal(t, "instance", src)
}

// The registry asks by manifest id; the rows live under the legacy key and
// must be found there. Red control: a provider that passes the manifest id
// through sees no row at all.
func TestResolve_ReadsRowsStoredUnderLegacyKey(t *testing.T) {
	var keys []string
	p := newProvider(conns{row: ws("https://ws.example"), keys: &keys}, nil)
	require.Equal(t, "team-relay", p.Manifest().ID)
	u, _, ok := resolve(t, p, uuid.New())
	require.True(t, ok)
	require.Equal(t, "https://ws.example", u)
	require.Equal(t, []string{"team_relay"}, keys)

	var plain extensions.ConnectionStore = conns{row: ws("https://ws.example")}
	c, err := plain.Connection(context.Background(), uuid.New(), p.Manifest().ID)
	require.NoError(t, err)
	require.Nil(t, c, "stores keyed by the legacy id do not know the manifest id")
}

func TestTransportEnabled(t *testing.T) {
	on := inst{&extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"relay_url": "https://i", "transport_enabled": true}}}
	require.True(t, newProvider(conns{}, on).TransportEnabled(context.Background(), uuid.New()))
	require.False(t, newProvider(conns{}, instance("https://i", true)).TransportEnabled(context.Background(), uuid.New()))
	require.False(t, newProvider(conns{row: &extensions.Connection{IsActive: false}}, on).TransportEnabled(context.Background(), uuid.New()))
}

func TestHealth(t *testing.T) {
	var gotPath string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(status)
	}))
	defer srv.Close()
	p := newProvider(nil, nil)
	r := extensions.Resolved{Enabled: true, Settings: map[string]any{"relay_url": srv.URL + "/"}}

	rep := p.Health(context.Background(), r)
	require.True(t, rep.OK, rep.Message)
	require.Equal(t, "/health", gotPath)

	status = http.StatusInternalServerError
	rep = p.Health(context.Background(), r)
	require.False(t, rep.OK)
	require.Contains(t, rep.Message, "HTTP 500")

	srv.Close()
	rep = p.Health(context.Background(), r)
	require.False(t, rep.OK)
	require.Contains(t, rep.Message, "unreachable")

	require.False(t, p.Health(context.Background(), extensions.Resolved{Enabled: true}).OK)
}

func TestValidate(t *testing.T) {
	p := newProvider(nil, nil)
	share := uuid.NewString()
	require.NoError(t, p.Validate(extensions.LevelInstance, map[string]any{"relay_url": "https://cp.tr.entire.vc"}))
	require.NoError(t, p.Validate(extensions.LevelWorkspace, map[string]any{"relay_url": "http://localhost:8080", "web_base_url": "https://tr.entire.vc", "transport_enabled": true}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{"relay_url": "ftp://x"}))
	require.Error(t, p.Validate(extensions.LevelWorkspace, map[string]any{"relay_url": "https://ok", "web_base_url": "not a url"}))

	require.NoError(t, p.Validate(extensions.LevelProject, map[string]any{"share_id": share, "subfolder": "docs", "key_expires_at": "2027-01-02T03:04:05Z"}))
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"subfolder": "docs"}))
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"share_id": "my-share"}))
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"share_id": share, "key_expires_at": "tomorrow"}))
}

// A rejected project binding must not echo the share key.
func TestValidate_ErrorDoesNotLeakAgentKey(t *testing.T) {
	const key = "sk-test-agent-key-0123456789"
	err := newProvider(nil, nil).Validate(extensions.LevelProject, map[string]any{"share_id": "bad", "agent_key": key})
	require.Error(t, err)
	require.NotContains(t, err.Error(), key)
}

func TestManifest_ValidAgainstSchemaV1(t *testing.T) {
	raw, err := os.ReadFile("mesh-extension.json")
	require.NoError(t, err)
	sch, err := os.ReadFile("../schema/manifest.v1.json")
	require.NoError(t, err)
	sd, err := jsonschema.UnmarshalJSON(bytes.NewReader(sch))
	require.NoError(t, err)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	require.NoError(t, c.AddResource("manifest.v1.json", sd))
	compiled, err := c.Compile("manifest.v1.json")
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, compiled.Validate(doc))

	m := newProvider(nil, nil).Manifest()
	require.Equal(t, "team-relay", m.ID)
	require.Equal(t, "builtin", m.Runtime)
	require.True(t, m.Levels.Instance && m.Levels.Workspace && m.Levels.Project)
	require.Equal(t, []string{"agent_key"}, m.Capabilities.Secrets)
}
