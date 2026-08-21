package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secretsEncryptedValueAllowlist is every file permitted to reference the
// secrets.encrypted_value column or the wire term "encrypted_value". It is
// deliberately short and hand-maintained — the point of this test is that
// growing this list requires a deliberate edit, not that the list itself is
// clever.
//
// Task #a2c330a8 (S6, epic #64e84eb1): the parent task's explicit warning is
// that a test proving only "wrote it, read it back through the same layer"
// proves nothing about the OTHER surfaces — get_task/recall payloads, the
// activity feed, any SSE emission. This test takes the complementary
// approach: instead of enumerating every reader and asserting each one is
// clean (which only proves the readers this test's author thought of are
// clean), it asserts NO reader outside this allowlist exists at all, by
// scanning the whole module for the one column name a leak would have to go
// through. A future handler, MCP tool, or recall/get_task code path that
// starts reading secrets.encrypted_value fails this test immediately,
// wherever it's added — not just in the files this test's author happened
// to check by hand.
var secretsEncryptedValueAllowlist = map[string]bool{
	"migrations/20260820111_create_secrets.sql":                 true,
	"internal/repository/postgres/secret_repo.go":               true,
	"internal/repository/postgres/secret_repo_db_test.go":       true,
	"internal/repository/postgres/secret_repo_sqlmock_test.go":  true,
	"internal/repository/postgres/secret_repo_boundary_test.go": true, // this file's own doc comments
	"internal/handler/secret_handler_test.go":                   true, // NotContains assertion naming the column it must never see
	"tests/integration/secrets_plaintext_boundary_test.go":      true, // S6's own end-to-end negative test
}

// TestSecretsEncryptedValue_OnlyReferencedByAllowlistedFiles is the
// structural half of S6. It does not need a database or a running server —
// it is a fast, always-run regression gate that fires the moment ANY new
// code anywhere in the module starts naming the encrypted column, which is
// the earliest point a plaintext leak could be introduced, before a single
// integration test would even need to be written to catch it.
func TestSecretsEncryptedValue_OnlyReferencedByAllowlistedFiles(t *testing.T) {
	repoRoot := findRepoRoot(t)

	var offenders []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "web", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".sql" {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), "encrypted_value") && !secretsEncryptedValueAllowlist[rel] {
			offenders = append(offenders, rel)
		}
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"encrypted_value is referenced outside the allowlist — a new reader of the secrets "+
			"table's encrypted column was added without updating secretsEncryptedValueAllowlist "+
			"(or genuinely introduces a new leak surface): %v", offenders)
}

// findRepoRoot walks up from the current package directory to the module
// root (the directory containing go.mod), so this test works regardless of
// the working directory `go test` is invoked from.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "reached filesystem root without finding go.mod")
		dir = parent
	}
}
