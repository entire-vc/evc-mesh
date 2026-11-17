package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// DELETE /extensions/:ext_id over real rows (D2): a caller who is not
// an instance administrator gets 403 and the row stays; a built-in extension
// gets 409 with the reason and stays; the administrator removes a repository
// install (200) and the sync hook runs. No build tag: skipped without Postgres.
func TestExtensionUninstallHandler_DB(t *testing.T) {
	db := extAuthorizeDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	repoID, builtinID := "uninst-h-"+suffix, "uninst-hb-"+suffix
	t.Cleanup(func() {
		for _, id := range []string{repoID, builtinID} {
			_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id)
			_, _ = db.Exec(`DELETE FROM extension_removals WHERE extension_id = $1`, id)
			_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, id)
		}
	})
	installed := postgres.NewInstalledExtensionRepo(db)
	require.NoError(t, postgres.NewRepoExtensionRepo(db).InsertRepo(ctx, &domain.InstalledExtension{
		ExtensionID: repoID, Version: "1.0.0", Manifest: []byte(`{"id":"` + repoID + `","kind":"integration"}`),
		Source: domain.ExtensionSourceRepo, RepoURL: "https://example.com/x.git", RepoRef: "v1.0.0",
		CommitSHA: strings.Repeat("b", 40), TrustLevel: domain.ExtensionTrustCustom,
	}, "", nil))
	require.NoError(t, installed.UpsertBuiltin(ctx, &domain.InstalledExtension{
		ExtensionID: builtinID, Version: "1.0.0", Manifest: []byte(`{"id":"` + builtinID + `","kind":"integration"}`),
		TrustLevel: domain.ExtensionTrustBuiltin,
	}))
	policy := postgres.NewInstanceExtensionPolicyRepo(db)
	settings := postgres.NewInstanceExtensionSettingRepo(db)
	synced := 0
	mk := func(admins InstanceAdminChecker) *ExtensionInstanceAdminHandler {
		return NewExtensionInstanceAdminHandler(policy, settings, installed, admins).
			WithUninstall(postgres.NewExtensionUninstallRepo(db), func(context.Context) { synced++ })
	}
	stillThere := func(id string) bool {
		row, err := installed.Get(ctx, id)
		require.NoError(t, err)
		return row != nil
	}

	// A fresh user is no instance administrator by the real check: 403.
	c, rec := adminCtx(http.MethodDelete, "", repoID, false)
	require.NoError(t, mk(settings).Uninstall(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.True(t, stillThere(repoID))

	// An agent is refused whatever its rights.
	c, rec = adminCtx(http.MethodDelete, "", repoID, true)
	require.NoError(t, mk(fakeAdmins{ok: true}).Uninstall(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.True(t, stillThere(repoID))

	// Built-in: 409 with the reason; it is switched off, never removed.
	c, rec = adminCtx(http.MethodDelete, "", builtinID, false)
	require.NoError(t, mk(fakeAdmins{ok: true}).Uninstall(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "turn it off")
	assert.True(t, stillThere(builtinID))

	// Not installed: 404.
	c, rec = adminCtx(http.MethodDelete, "", "uninst-none-"+suffix, false)
	require.NoError(t, mk(fakeAdmins{ok: true}).Uninstall(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Zero(t, synced)

	// The administrator removes the repository install.
	c, rec = adminCtx(http.MethodDelete, "", repoID, false)
	uid := c.Get(middleware.ContextKeyUserID).(uuid.UUID)
	require.NoError(t, mk(fakeAdmins{ok: true}).Uninstall(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, repoID, out["extension_id"])
	assert.Equal(t, true, out["data_kept"])
	assert.False(t, stillThere(repoID))
	assert.Equal(t, 1, synced, "registry sync runs after the uninstall")
	var actor uuid.UUID
	require.NoError(t, db.Get(&actor, `SELECT actor_id FROM instance_extension_journal WHERE extension_id = $1 AND action = $2`, repoID, domain.InstanceJournalUninstalled))
	assert.Equal(t, uid, actor)
}
