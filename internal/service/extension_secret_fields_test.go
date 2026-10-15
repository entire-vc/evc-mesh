package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// The manifest schema lets a level point at "#/schemas/<name>"; the secrets of
// such a level must be masked the same as inline ones.
func TestSecretFields_ResolvesReferencedWorkspaceSchema(t *testing.T) {
	m, err := extensions.ParseManifest([]byte(`{"id":"abc","version":"1","runtime":"builtin",
		"settings":{"workspace":{"$ref":"#/schemas/bot"}},
		"schemas":{"bot":{"properties":{"bot_token":{"x-secret":true},"bot_username":{}}}}}`))
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"bot_token": true}, secretFields(m))

	inline, err := extensions.ParseManifest([]byte(`{"id":"abc","version":"1","runtime":"builtin",
		"settings":{"workspace":{"properties":{"k":{"x-secret":true}}}}}`))
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"k": true}, secretFields(inline))
}
