package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestDeprecatedKeepCapabilitiesPreserveConsentDB(t *testing.T) {
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "keep consent", Slug: "keep-consent-" + uuid.New().String()[:8], OwnerID: agentKeyWsReuseOwner(t, db)}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	id := "keep-" + uuid.New().String()[:8]
	raw := strings.Replace(reconsentManifest(id, "1.0.0", []string{"tasks:read", "agent_key:rotate", "agent_keys:read", "secrets:write"}, nil), `"runtime":"builtin"`, `"runtime":"external"`, 1)
	raw = strings.Replace(raw, `"levels":`, `"external":{"base_url_setting":"base_url","health_path":"/healthz"},"levels":`, 1)
	m, err := extensions.ParseManifest([]byte(raw))
	require.NoError(t, err)
	require.Equal(t, []string{"tasks:read", "agent_key:rotate"}, m.Capabilities.API)
	integ := postgres.NewIntegrationRepo(db)
	consents := postgres.NewIntegrationConsentRepo(db)
	reg := extensions.NewRegistry()
	// Only the external extension's HTTP health endpoint is substituted.
	keep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(keep.Close)
	provider, err := external.NewProvider(m, extensions.Deps{Conns: extensions.IntegrationConnections{Repo: integ}}, keep.Client(), nil)
	require.NoError(t, err)
	require.NoError(t, reg.Register(provider))
	installRepoRelease(t, db, id, "1.0.0", "unchanged", raw)
	settings, err := json.Marshal(map[string]string{"base_url": keep.URL})
	require.NoError(t, err)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProvider(id), Config: settings, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, conn))
	old := []string{"api:agent_key:rotate", "api:agent_keys:read", "api:secrets:write", "api:tasks:read"}
	require.NoError(t, consents.SetConsent(ctx, conn.ID, domain.ExtensionConsent{Capabilities: old, Version: "1.0.0", Commit: "unchanged", At: time.Now()}))
	svc := NewExtensionWorkspaceService(reg, integ, consents).WithInstalledReleases(postgres.NewInstalledExtensionRepo(db))
	check := func() {
		views, listErr := svc.List(ctx, ws.ID)
		require.NoError(t, listErr)
		require.Len(t, views, 1)
		require.NotNil(t, views[0].Connection)
		require.False(t, views[0].Connection.NeedsConsent, "removal is not a new grant")
		require.True(t, views[0].Status.Enabled)
		require.Nil(t, views[0].Connection.PendingConsent)
		after, getErr := integ.GetByID(ctx, conn.ID)
		require.NoError(t, getErr)
		require.True(t, after.IsActive)
		require.JSONEq(t, string(conn.Config), string(after.Config))
	}
	check()
	declared := old
	actors := WithDeclaredCapabilities(WithConsents(NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, postgres.NewWorkspaceRepo(db)), consents), func(string) ([]string, bool) { return declared, true })
	issued, err := actors.Rotate(ctx, ws.ID, conn.ID)
	require.NoError(t, err)
	declared = extensions.DeclaredCapabilities(m.Capabilities)
	principal, err := actors.Authenticate(ctx, issued.Token)
	require.NoError(t, err)
	require.True(t, principal.HasAPI("tasks:read"))
	require.True(t, principal.HasAPI("agent_key:rotate"))
	require.False(t, principal.HasAPI("agent_keys:read"))
	require.False(t, principal.HasAPI("secrets:write"))
	stored, err := consents.GetConsent(ctx, ws.ID, id)
	require.NoError(t, err)
	require.Equal(t, old, stored.Capabilities)
	installed, err := postgres.NewInstalledExtensionRepo(db).Get(ctx, id)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(installed.Manifest), "saved manifest is not rewritten")
	// A real addition remains a positive control for the consent gate.
	m.Capabilities.API = append(m.Capabilities.API, "tasks:write")
	next, err := json.Marshal(map[string]any{"id": id, "version": "1.1.0", "runtime": "external", "capabilities": m.Capabilities})
	require.NoError(t, err)
	installRepoRelease(t, db, id, "1.1.0", "added", string(next))
	views, err := svc.List(ctx, ws.ID)
	require.NoError(t, err)
	require.True(t, views[0].Connection.NeedsConsent)
	require.Equal(t, []string{"api:tasks:write"}, views[0].Connection.PendingConsent.AddedCapabilities)
}
