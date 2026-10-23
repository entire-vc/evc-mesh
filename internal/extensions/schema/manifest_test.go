package schema_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// manifestV1SHA256 is the digest of manifest.v1.json fixed by the extensions
// manifest ADR. The Spark side pins the same value.
const manifestV1SHA256 = "f243313cae6043ae6e3b18fa1c3b636fb82333911fe9f0febc788e9aaa970564"

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
	require.Len(t, files, 8, "expected 8 valid fixtures")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			require.NoError(t, validate(t, sch, f))
		})
	}
}

func TestManifestV1_InvalidFixturesRejected(t *testing.T) {
	sch := compileSchema(t)
	files := fixtures(t, "invalid")
	require.Len(t, files, 15, "expected 15 invalid fixtures")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			require.Error(t, validate(t, sch, f))
		})
	}
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
	for _, ok := range []string{"agent_key:rotate", "ext_token:self_rotate"} {
		require.NoError(t, with(ok), ok)
	}
	for _, bad := range []string{"agent_key:read", "agent_keys:rotate", "ext_token:rotate", "ext_token:self_rotate:x", "agent_key:rotate "} {
		require.Error(t, with(bad), bad)
	}
}
