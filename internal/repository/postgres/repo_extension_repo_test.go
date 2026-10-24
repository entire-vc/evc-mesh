package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// Without an encryption key Encrypt returns plaintext; the repo token path must
// refuse instead of storing it. The refusal comes before any database access,
// so a nil db proves nothing was written.
func TestRepoExtensionRepo_InsertRepo_TokenFailsClosedWithoutKey(t *testing.T) {
	t.Setenv(encryption.EnvKey, "")
	encryption.ResetForTest()
	t.Cleanup(encryption.ResetForTest)

	err := NewRepoExtensionRepo(nil).InsertRepo(context.Background(), &domain.InstalledExtension{ExtensionID: "x"}, "glpat-secret", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-secret", nil)
}
