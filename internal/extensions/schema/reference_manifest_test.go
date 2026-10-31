package schema_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The manifest of the reference external extension is what partners copy: it
// must stay valid against the published schema.
func TestManifestV1_ReferenceExtensionValid(t *testing.T) {
	sch := compileSchema(t)
	require.NoError(t, validate(t, sch, filepath.Join("..", "..", "..", "examples", "external-extension", "mesh-extension.json")))
}
