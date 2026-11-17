package schema_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// manifestV1SHA256 is the digest of manifest.v1.json fixed by the extensions
// manifest ADR. The Spark side pins the same value.
const manifestV1SHA256 = "1d4c5aec4b4785b73db7a95c8d3802d5425b6c6d19c448b5fbb01122292294c3"

const schemaFile = "manifest.v1.json"

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(schemaFile)
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)

	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	require.NoError(t, c.AddResource("manifest.v1.json", doc))
	sch, err := c.Compile("manifest.v1.json")
	require.NoError(t, err)
	return sch
}

func fixtures(t *testing.T, kind string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("fixtures", kind, "*.json"))
	require.NoError(t, err)
	return files
}

func validate(t *testing.T, sch *jsonschema.Schema, file string) error {
	t.Helper()
	f, err := os.Open(file)
	require.NoError(t, err)
	defer f.Close()
	inst, err := jsonschema.UnmarshalJSON(f)
	require.NoError(t, err)
	return sch.Validate(inst)
}

func TestManifestV1_ValidFixturesPass(t *testing.T) {
	sch := compileSchema(t)
	files := fixtures(t, "valid")
	require.Len(t, files, 10, "expected 10 valid fixtures")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			require.NoError(t, validate(t, sch, f))
		})
	}
}

func TestManifestV1_InvalidFixturesRejected(t *testing.T) {
	sch := compileSchema(t)
	files := fixtures(t, "invalid")
	require.Len(t, files, 20, "expected 20 invalid fixtures")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			require.Error(t, validate(t, sch, f))
		})
	}
}

// settingsField validates mesh-ext-hello with one extra workspace settings field.
func settingsField(t *testing.T, sch *jsonschema.Schema, field map[string]any) error {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", "valid", "mesh-ext-hello.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	props := doc["settings"].(map[string]any)["workspace"].(map[string]any)["properties"].(map[string]any)
	props["extra"] = field
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
	require.NoError(t, err)
	return sch.Validate(inst)
}

func TestManifestV1_ThemeSettingsWidgets(t *testing.T) {
	sch := compileSchema(t)
	for _, w := range []string{"color", "font-select", "image", "field-picker", "widget-picker", "layout-picker", "order-list", "grant-picker", "workspace-picker"} {
		require.NoError(t, settingsField(t, sch, map[string]any{"type": "string", "x-widget": w}), w)
	}
	require.NoError(t, settingsField(t, sch, map[string]any{"type": "string", "x-token": "primary", "x-options": map[string]any{"wide": "Wide"}}))
	require.Error(t, settingsField(t, sch, map[string]any{"type": "string", "x-widget": "no-such-widget"}), "control: an unknown widget is still refused")
	require.Error(t, settingsField(t, sch, map[string]any{"type": "string", "x-options": []string{"wide"}}), "x-options labels are an object")
}

func TestManifestV1_SchemaSHA256(t *testing.T) {
	raw, err := os.ReadFile(schemaFile)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, manifestV1SHA256, hex.EncodeToString(sum[:]))
}

// TestManifestV1_ActionCapabilities pins the two non-read/write API rights that
// extension_auth.go maps routes to: each must be declarable on its own, and
// near-misses must not slip through the pattern.
func TestManifestV1_ActionCapabilities(t *testing.T) {
	sch := compileSchema(t)
	base, err := os.ReadFile(filepath.Join("fixtures", "valid", "keep-rotation.json"))
	require.NoError(t, err)

	with := func(cap string) error {
		// Keep only the capability under test next to a plain read right.
		raw := bytes.Replace(base, []byte(`"agent_key:rotate",`), nil, 1)
		raw = bytes.Replace(raw, []byte(`"ext_token:self_rotate"`), []byte(`"`+cap+`"`), 1)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		require.NoError(t, err)
		return sch.Validate(doc)
	}
	for _, ok := range []string{"agent_key:rotate", "ext_token:self_rotate", "members:authorize"} {
		require.NoError(t, with(ok), ok)
	}
	for _, bad := range []string{"agent_key:read", "agent_keys:rotate", "ext_token:rotate", "ext_token:self_rotate:x", "agent_key:rotate ", "members:authorise", "users:authorize"} {
		require.Error(t, with(bad), bad)
	}
}

// TestManifestV1_ThemeTokenNames pins the token-name allowlist: a known name is
// accepted, an unknown one is rejected.
func TestManifestV1_ThemeTokenNames(t *testing.T) {
	sch := compileSchema(t)
	base, err := os.ReadFile(filepath.Join("fixtures", "valid", "theme-full.json"))
	require.NoError(t, err)

	with := func(token string) error {
		raw := bytes.Replace(base, []byte(`"chart-1":"12 76% 61%"`), []byte(`"`+token+`":"12 76% 61%"`), 1)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		require.NoError(t, err)
		return sch.Validate(doc)
	}
	require.NoError(t, with("chart-1"))
	require.NoError(t, with("sidebar-border"))
	require.Error(t, with("chart-9"))
	require.Error(t, with("made-up-token"))
}

// externalField validates keep-external with one extra key in its external block.
func externalField(t *testing.T, sch *jsonschema.Schema, key string, value any) error {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", "valid", "keep-external.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["external"].(map[string]any)[key] = value
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
	require.NoError(t, err)
	return sch.Validate(inst)
}

func TestManifestV1_ExternalTokenPath(t *testing.T) {
	sch := compileSchema(t)
	require.NoError(t, externalField(t, sch, "token_path", "/mesh/token"))
	require.Error(t, externalField(t, sch, "token_path", "mesh/token"), "a path starts with /")
	require.Error(t, externalField(t, sch, "token_paths", "/mesh/token"), "control: unknown keys are still refused")
}

func TestManifestV1_ExternalSignatureVersion(t *testing.T) {
	sch := compileSchema(t)
	require.NoError(t, externalField(t, sch, "signature_version", 2))
	require.NoError(t, externalField(t, sch, "signature_version", 1))
	require.Error(t, externalField(t, sch, "signature_version", 3), "only known versions")
	require.Error(t, externalField(t, sch, "signature_version", "2"), "a number, not a string")
}

// TestManifestV1_BuiltinManifestsValid validates the manifest of every builtin
// extension package: a schema change must keep the shipped ones valid.
func TestManifestV1_BuiltinManifestsValid(t *testing.T) {
	sch := compileSchema(t)
	files, err := filepath.Glob(filepath.Join("..", "*", "mesh-extension.json"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 7, "every builtin extension ships a manifest")
	for _, f := range files {
		t.Run(filepath.Base(filepath.Dir(f)), func(t *testing.T) {
			require.NoError(t, validate(t, sch, f))
		})
	}
}

// widgetSettingsField validates widgets-builtin with one extra widget settings field.
func widgetSettingsField(t *testing.T, sch *jsonschema.Schema, field map[string]any) error {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", "valid", "widgets-builtin.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	w := doc["widgets"].([]any)[0].(map[string]any)
	w["settings"].(map[string]any)["properties"].(map[string]any)["extra"] = field
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
	require.NoError(t, err)
	return sch.Validate(inst)
}

func TestManifestV1_WidgetSettingsRefuseSecrets(t *testing.T) {
	sch := compileSchema(t)
	require.NoError(t, widgetSettingsField(t, sch, map[string]any{"type": "string", "x-widget": "project-picker"}), "control: a plain field passes")
	require.Error(t, widgetSettingsField(t, sch, map[string]any{"type": "string", "x-secret": true}))
	require.Error(t, widgetSettingsField(t, sch, map[string]any{"type": "string", "x-secret": false}), "the key itself is refused")
	// Deeper than a direct field: a nested object field, a list of subschemas, the settings root.
	nested := map[string]any{"type": "object", "properties": map[string]any{"token": map[string]any{"type": "string", "x-secret": true}}}
	require.Error(t, widgetSettingsField(t, sch, nested), "a nested field")
	require.Error(t, widgetSettings(t, sch, map[string]any{"type": "object", "allOf": []any{map[string]any{"x-secret": true}}}), "inside a list")
	require.Error(t, widgetSettings(t, sch, map[string]any{"type": "object", "x-secret": true}), "at the root")
	require.NoError(t, widgetSettings(t, sch, map[string]any{"type": "object", "allOf": []any{map[string]any{"required": []any{"project_id"}}}}),
		"control: the same shapes without the key pass")
	// Workspace settings may still hold secrets: the ban is for widget instances only.
	require.NoError(t, settingsField(t, sch, map[string]any{"type": "string", "x-secret": true}))
}

// widgetSettings validates widgets-builtin with its first widget's settings replaced.
func widgetSettings(t *testing.T, sch *jsonschema.Schema, settings map[string]any) error {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", "valid", "widgets-builtin.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["widgets"].([]any)[0].(map[string]any)["settings"] = settings
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
	require.NoError(t, err)
	return sch.Validate(inst)
}
