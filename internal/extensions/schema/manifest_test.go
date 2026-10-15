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
const manifestV1SHA256 = "7eab08c841b6fe18a468a8c187337502edee0b970de34511544b9629d2cfa4ba"

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
	require.Len(t, files, 4, "expected 4 valid fixtures")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			require.NoError(t, validate(t, sch, f))
		})
	}
}

func TestManifestV1_InvalidFixturesRejected(t *testing.T) {
	sch := compileSchema(t)
	files := fixtures(t, "invalid")
	require.Len(t, files, 6, "expected 6 invalid fixtures")
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
