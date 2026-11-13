//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The catalog scope is stored with the row, carried into the
// rollback snapshot (inside its seal) and restored by a rollback; the move
// writes the scope of its target. Existing rows read as 'default'.
func TestInstalledExtensionRepo_CatalogScopeRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewInstalledExtensionRepo(db)
	const pinned, plain, moved = "ext-scope-pinned-test", "ext-scope-plain-test", "ext-scope-moved-test"
	t.Cleanup(func() {
		for _, id := range []string{pinned, plain, moved} {
			db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id)
		}
	})
	sha := func(c string) string { return strings.Repeat(c, 40) }
	mk := func(id, v, commit, scope string) *domain.InstalledExtension {
		return &domain.InstalledExtension{ExtensionID: id, Version: v, Manifest: []byte(`{"id":"` + id + `","version":"` + v + `"}`),
			CommitSHA: commit, ArtifactSHA256: "d-" + v, TrustLevel: "community", CatalogScope: scope}
	}

	require.NoError(t, repo.InsertCatalog(ctx, mk(plain, "1.0.0", sha("1"), "")))
	got, err := repo.Get(ctx, plain)
	require.NoError(t, err)
	assert.Equal(t, "default", got.CatalogScope, "no scope given: default")

	require.NoError(t, repo.InsertCatalog(ctx, mk(pinned, "0.4.0", sha("a"), "scoped")))
	got, err = repo.Get(ctx, pinned)
	require.NoError(t, err)
	assert.Equal(t, "scoped", got.CatalogScope)

	// An admin chooses a default-catalogue version: the row unpins, the
	// snapshot keeps the pin inside its seal.
	require.NoError(t, repo.UpdateCatalog(ctx, mk(pinned, "0.9.0", sha("f"), "default"),
		domain.CatalogUpdateGuard{FromVersion: "0.4.0", FromCommitSHA: sha("a")}))
	got, err = repo.Get(ctx, pinned)
	require.NoError(t, err)
	assert.Equal(t, "default", got.CatalogScope)
	snap, err := repo.GetCatalogSnapshot(ctx, pinned)
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, "scoped", snap.CatalogScope)
	assert.True(t, snap.Intact(), "the scope survives the JSONB round trip inside the seal")

	require.NoError(t, repo.RollbackCatalog(ctx, pinned,
		domain.CatalogRollbackGuard{Version: "0.9.0", CommitSHA: sha("f"), SnapshotDigest: snap.Digest}, *snap,
		&domain.InstanceExtensionJournalEntry{ExtensionID: pinned, Action: domain.InstanceJournalRolledBack, Details: []byte(`{}`)}))
	got, err = repo.Get(ctx, pinned)
	require.NoError(t, err)
	assert.Equal(t, "0.4.0", got.Version)
	assert.Equal(t, "scoped", got.CatalogScope, "rollback restores the pin")

	// The move writes its target's scope.
	_, err = db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, repo_url, repo_ref, commit_sha, trust_level)
		VALUES ($1, '0.4.0', '{}', 'repo', 'https://gitlab.example/g/p', 'v0.4.0', $2, 'custom')`, moved, sha("4"))
	require.NoError(t, err)
	require.NoError(t, repo.MoveRepoToCatalog(ctx, mk(moved, "0.4.0", sha("a"), "scoped"),
		domain.ExtensionStateGuard{Source: domain.ExtensionSourceRepo, Version: "0.4.0", CommitSHA: sha("4")},
		&domain.InstanceExtensionJournalEntry{ExtensionID: moved, Action: domain.InstanceJournalMovedToCatalog, Details: []byte(`{}`)}))
	got, err = repo.Get(ctx, moved)
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionSourceCatalog, got.Source)
	assert.Equal(t, "scoped", got.CatalogScope)

	_, err = db.Exec(`UPDATE installed_extensions SET catalog_scope = 'pinned' WHERE extension_id = $1`, plain)
	require.Error(t, err, "only default and scoped are stored")
}
