package runner_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
)

type conns struct {
	rows map[string]*extensions.Connection
}

func (c conns) Connection(_ context.Context, _ uuid.UUID, id string) (*extensions.Connection, error) {
	return c.rows[id], nil
}

type inst struct{ s *extensions.InstanceSetting }

func (i inst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.s, nil
}

var inventory = map[string]any{"pools": map[string]any{"secret-pool": 1}}

func TestResolve_UnderEitherNameAndNeverDisclosesTheCatalog(t *testing.T) {
	for _, name := range []string{runner.ID, runner.LegacyID} {
		p := runner.New(extensions.Deps{Conns: conns{rows: map[string]*extensions.Connection{name: {IsActive: true, Settings: inventory}}}})
		r, err := p.Resolve(context.Background(), uuid.New())
		require.NoError(t, err)
		require.True(t, r.Enabled, name)
		require.Equal(t, extensions.SourceWorkspace, r.Source)
		require.Equal(t, "runner", r.ExtensionID)
		require.Empty(t, r.Settings, "the resource inventory must not leave through the status path")
	}
}

func TestResolve_NewNameWinsAndInactiveRowSwitchesOff(t *testing.T) {
	p := runner.New(extensions.Deps{Conns: conns{rows: map[string]*extensions.Connection{
		runner.ID:       {IsActive: false},
		runner.LegacyID: {IsActive: true},
	}}})
	r, err := p.Resolve(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, r.Enabled)

	r, err = runner.New(extensions.Deps{Conns: conns{}, Inst: inst{}}).Resolve(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, r.Enabled)
	require.Equal(t, extensions.SourceNone, r.Source)
}

func TestCanonical(t *testing.T) {
	require.Equal(t, "runner", runner.Canonical("agent_runtime"))
	require.Equal(t, "runner", runner.Canonical("runner"))
	require.Equal(t, "spark", runner.Canonical("spark"))
}

// Strictness of the old special branches is kept: a broken catalog is refused
// as invalid, a well-formed one is still refused for a generic write.
func TestValidate_InvalidPoolsAreRefusedAndValidOnesGoThroughTheVersionedAPI(t *testing.T) {
	p := runner.New(extensions.Deps{})
	err := p.Validate(extensions.LevelWorkspace, map[string]any{"pools": "not-an-object"})
	require.EqualError(t, err, "invalid runtime catalog")

	err = p.Validate(extensions.LevelWorkspace, map[string]any{
		"schema_version": 2, "controllers": map[string]any{}, "accounts": map[string]any{},
		"pools": map[string]any{}, "profiles": map[string]any{}, "bindings": map[string]any{},
	})
	require.ErrorIs(t, err, runner.ErrVersionedAPI)

	require.NoError(t, p.Validate(extensions.LevelInstance, map[string]any{}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{"x": 1}))
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{}))
}

func validate(t *testing.T, raw []byte) error {
	t.Helper()
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
	return compiled.Validate(inst)
}

func TestManifest_ValidAgainstSchemaV1_BrokenOneFails(t *testing.T) {
	raw, err := os.ReadFile("mesh-extension.json")
	require.NoError(t, err)
	require.NoError(t, validate(t, raw))
	broken := bytes.Replace(raw, []byte(`"kind": "plugin"`), []byte(`"kind": "gizmo"`), 1)
	require.NotEqual(t, raw, broken)
	require.Error(t, validate(t, broken))
	require.Equal(t, "runner", runner.New(extensions.Deps{}).Manifest().ID)
}
