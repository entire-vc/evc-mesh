package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestCatalogFiltersDeprecatedManifestDB(t *testing.T) {
	db := extAuthorizeDB(t)
	id := "keep-" + uuid.New().String()[:8]
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(repoManifest(t, "1.0.0")), &doc))
	doc["id"] = id
	doc["capabilities"] = map[string]any{"api": []string{"tasks:read", "agent_keys:read", "secrets:write"}, "ui": []string{"command", "workspace.settings"}, "webhooks_in": []string{"inbound"}}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	// Only the Git host is external; installation, authorization and storage
	// use their real database implementations.
	env := &extAuthzEnv{db: db}
	user := env.user(t)
	ws := env.workspace(t, user)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, user)
	})
	admins := postgres.NewInstanceExtensionSettingRepo(db)
	_, adminIDs, err := admins.InstanceAdminTargets(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, adminIDs)
	sha := strings.Repeat("a", 40)
	git := &repoGitLab{manifests: map[string]string{sha: string(raw)}, shas: map[string]string{"v1": sha}}
	host := httptest.NewServer(git)
	t.Cleanup(host.Close)
	installer := &repoinstall.Service{Client: &repoinstall.Client{HTTP: host.Client(), AllowInsecure: true}, Repo: postgres.NewRepoExtensionRepo(db), Installed: postgres.NewInstalledExtensionRepo(db), Policy: postgres.NewInstanceExtensionPolicyRepo(db)}
	install := NewExtensionRepoInstallHandler(installer, admins)
	body := `{"source":"repo","repo_url":"` + host.URL + `/grp/hello","ref":"v1"}`
	c, installedResponse := adminCtx(http.MethodPost, body, "", false)
	c.Set(middleware.ContextKeyUserID, adminIDs[0])
	require.NoError(t, install.Install(c))
	require.Equal(t, http.StatusCreated, installedResponse.Code, installedResponse.Body.String())
	var receipt struct {
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal(installedResponse.Body.Bytes(), &receipt))
	require.Len(t, receipt.Warnings, 5)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id=$1`, id) })
	h := NewExtensionHandler(postgres.NewInstalledExtensionRepo(db), nil)
	rec := doExt(t, h.Catalog, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Extensions []extensionSummaryResponse `json:"extensions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	found := false
	for _, row := range out.Extensions {
		if row.ID != id {
			continue
		}
		found = true
		// Decode names explicitly: the response is what D3's consent form reads.
		var data map[string]any
		require.NoError(t, json.Unmarshal(row.Manifest, &data))
		caps := data["capabilities"].(map[string]any)
		require.Equal(t, []any{"tasks:read"}, caps["api"])
		require.Empty(t, caps["ui"])
		require.Empty(t, caps["webhooks_in"])
		require.Len(t, row.Warnings, 5)
	}
	require.True(t, found)
	var saved string
	require.NoError(t, db.Get(&saved, `SELECT manifest::text FROM installed_extensions WHERE extension_id=$1`, id))
	require.JSONEq(t, string(raw), saved)
}
