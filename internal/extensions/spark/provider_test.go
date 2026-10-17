package spark_test

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
	"github.com/entire-vc/evc-mesh/internal/extensions/spark"
)

type conns struct {
	row *extensions.Connection
	err error
}

func (c conns) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return c.row, c.err
}

type inst struct{ s *extensions.InstanceSetting }

func (i inst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.s, nil
}

func newProvider(c extensions.ConnectionStore, i extensions.InstanceStore) *spark.Provider {
	return spark.New(extensions.Deps{Conns: c, Inst: i})
}

func ws(url string) *extensions.Connection {
	return &extensions.Connection{IsActive: true, Settings: map[string]any{"spark_url": url}}
}

func instance(url string, enabled bool) inst {
	return inst{&extensions.InstanceSetting{Enabled: enabled, Settings: map[string]any{"spark_url": url}}}
}

func resolve(t *testing.T, p *spark.Provider, w uuid.UUID) (sparkURL, source string, ok bool) {
	t.Helper()
	return p.ResolveSparkURL(context.Background(), w)
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

// A workspace that switched Spark off stays off however the instance is set.
func TestResolve_InactiveRow_InstanceConfigured_StillOff(t *testing.T) {
	p := newProvider(conns{row: &extensions.Connection{IsActive: false}}, instance("https://inst.example", true))
	_, _, ok := resolve(t, p, uuid.New())
	require.False(t, ok)
}

func TestResolve_NeitherRowNorInstance_Off(t *testing.T) {
	_, _, ok := resolve(t, newProvider(conns{}, inst{}), uuid.New())
	require.False(t, ok)
}

// An active row with no URL carries nothing; it must not shadow the instance.
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

func TestResolve_NilConnections_And_NilWorkspace_UseInstance(t *testing.T) {
	u, src, ok := resolve(t, newProvider(nil, instance("https://inst.example", true)), uuid.Nil)
	require.True(t, ok)
	require.Equal(t, "https://inst.example", u)
	require.Equal(t, "instance", src)
}

// Rows written before the manifest fixed spark_url keep working.
func TestResolve_LegacySettingKeys(t *testing.T) {
	for key, want := range map[string]string{"url": "https://old-inst.example", "base_url": "https://old-ws.example"} {
		row := &extensions.Connection{IsActive: true, Settings: map[string]any{key: want}}
		u, _, ok := resolve(t, newProvider(conns{row: row}, nil), uuid.New())
		require.True(t, ok, key)
		require.Equal(t, want, u, key)
	}
	i := inst{&extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"url": "https://old-inst.example"}}}
	u, _, ok := resolve(t, newProvider(nil, i), uuid.Nil)
	require.True(t, ok)
	require.Equal(t, "https://old-inst.example", u)
}

func TestInstanceAvailable(t *testing.T) {
	require.True(t, newProvider(conns{row: &extensions.Connection{IsActive: false}}, instance("https://i", true)).InstanceAvailable(context.Background()),
		"a workspace row does not change what the instance alone would run")
	require.False(t, newProvider(nil, instance("https://i", false)).InstanceAvailable(context.Background()))
	require.False(t, newProvider(nil, inst{}).InstanceAvailable(context.Background()))
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
	r := extensions.Resolved{Enabled: true, Settings: map[string]any{"spark_url": srv.URL + "/"}}

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
	require.NoError(t, p.Validate(extensions.LevelInstance, map[string]any{"spark_url": "https://spark.entire.vc"}))
	require.NoError(t, p.Validate(extensions.LevelWorkspace, map[string]any{"spark_url": "http://localhost:8080"}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{"spark_url": "ftp://x"}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{"spark_url": "not a url"}))
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"spark_url": "https://x"}))
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
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, compiled.Validate(inst))

	m := newProvider(nil, nil).Manifest()
	require.Equal(t, "spark", m.ID)
	require.Equal(t, "integration", m.Kind)
	require.Equal(t, "builtin", m.Runtime)
	require.True(t, m.Levels.Instance)
	require.True(t, m.Levels.Workspace)
}
