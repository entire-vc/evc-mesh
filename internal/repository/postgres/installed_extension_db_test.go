//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestInstalledExtensionRepo_UpsertBuiltin(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewInstalledExtensionRepo(db)
	const id = "ext-upsert-test"
	t.Cleanup(func() { db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id) })

	mk := func(v string) *domain.InstalledExtension {
		return &domain.InstalledExtension{ExtensionID: id, Version: v, Manifest: []byte(`{"id":"` + id + `"}`), TrustLevel: domain.ExtensionTrustBuiltin}
	}
	require.NoError(t, repo.UpsertBuiltin(ctx, mk("1.0.0")))
	require.NoError(t, repo.UpsertBuiltin(ctx, mk("1.1.0")))
	got, err := repo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "1.1.0", got.Version)
	require.Equal(t, "1.0.0", got.PreviousVersion)
	require.Equal(t, domain.ExtensionSourceBuiltin, got.Source)

	// a repo-sourced row with the same id is not overwritten
	_, err = db.Exec(`UPDATE installed_extensions SET source = 'repo' WHERE extension_id = $1`, id)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertBuiltin(ctx, mk("9.9.9")))
	got, err = repo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "1.1.0", got.Version)

	missing, err := repo.Get(ctx, "no-such")
	require.NoError(t, err)
	require.Nil(t, missing)
}

// The unattended pass must not move the Runner row (desired controller version
// follows it); an explicit update of it and the auto update of any other
// catalog extension still apply.
func TestInstalledExtensionRepo_UpdateCatalog_RunnerManualOnly(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewInstalledExtensionRepo(db)
	const other = "ext-auto-control-test"
	ids := []string{domain.RunnerExtensionID, other}
	clearInstalledRunner(t, db)
	t.Cleanup(func() {
		for _, id := range ids {
			db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id)
		}
	})
	for _, id := range ids {
		_, err := db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level, commit_sha)
			VALUES ($1,'0.4.4','{}','catalog','verified','c1')`, id)
		require.NoError(t, err)
	}
	next := func(id string) *domain.InstalledExtension {
		return &domain.InstalledExtension{ExtensionID: id, Version: "0.4.5", Manifest: []byte(`{}`), CommitSHA: "c2", ArtifactSHA256: "a", TrustLevel: "verified"}
	}
	guard := domain.CatalogUpdateGuard{FromVersion: "0.4.4", FromCommitSHA: "c1", Auto: true}

	require.Error(t, repo.UpdateCatalog(ctx, next(domain.RunnerExtensionID), guard))
	got, err := repo.Get(ctx, domain.RunnerExtensionID)
	require.NoError(t, err)
	require.Equal(t, "0.4.4", got.Version, "auto write left the runner alone")

	require.NoError(t, repo.UpdateCatalog(ctx, next(other), guard), "control: auto update of another extension works")
	got, err = repo.Get(ctx, other)
	require.NoError(t, err)
	require.Equal(t, "0.4.5", got.Version)

	guard.Auto = false
	require.NoError(t, repo.UpdateCatalog(ctx, next(domain.RunnerExtensionID), guard), "explicit update still applies")
	got, err = repo.Get(ctx, domain.RunnerExtensionID)
	require.NoError(t, err)
	require.Equal(t, "0.4.5", got.Version)
}
