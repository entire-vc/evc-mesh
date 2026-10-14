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
