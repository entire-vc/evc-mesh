package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	relayext "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Real PostgreSQL stores and provider; only the external relay HTTP boundary is simulated.
func TestTeamRelayRegistryRuntime_DB(t *testing.T) {
	t.Setenv("MESH_TEAMRELAY_RELAY_URL", "")
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "")
	f := newRecurringActorTypeFixture(t)
	ctx := context.Background()
	projects := postgres.NewProjectRepo(f.db)
	integrations := postgres.NewProjectIntegrationRepo(f.db)
	instances := postgres.NewInstanceExtensionSettingRepo(f.db)
	previous, err := instances.Get(ctx, relayext.LegacyID)
	require.NoError(t, err)
	t.Cleanup(func() {
		if previous != nil {
			require.NoError(t, instances.Upsert(ctx, previous, nil))
		} else {
			_, err := f.db.Exec(`DELETE FROM instance_extension_settings WHERE extension_id=$1`, relayext.LegacyID)
			require.NoError(t, err)
		}
	})
	hits := 0
	var relay *httptest.Server
	relay = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			require.Equal(t, "tr_agent_"+strings.Repeat("a", 48), r.Header.Get("X-Agent-Key"))
			json.NewEncoder(w).Encode(map[string]string{"path": "doc.md", "web_url": relay.URL + "/published"})
		case r.URL.Path == "/published":
			w.Write([]byte("published document"))
		default:
			require.Equal(t, "tr_agent_"+strings.Repeat("a", 48), r.Header.Get("X-Agent-Key"))
			w.Write([]byte(`{"web_folder_items":[{"name":"doc.md","path":"docs/doc.md"},{"name":"other.md","path":"other.md"}]}`))
		}
	}))
	defer relay.Close()
	settings, _ := json.Marshal(map[string]any{"relay_url": relay.URL + "/", "transport_enabled": true})
	row := &domain.InstanceExtensionSetting{ExtensionID: relayext.LegacyID, Enabled: true, Source: domain.InstanceSettingSourceAdmin, Settings: settings}
	require.NoError(t, instances.Upsert(ctx, row, nil))
	provider := relayext.New(extensions.Deps{Inst: &extensions.DBInstanceStore{Repo: instances}, Conns: extensions.IntegrationConnections{Repo: postgres.NewIntegrationRepo(f.db)}})
	pi := &domain.ProjectIntegration{ID: uuid.New(), ProjectID: f.projectID, Type: "team_relay", Enabled: true, Settings: json.RawMessage(`{"share_slug":"registry-runtime","share_id":"11111111-1111-1111-1111-111111111111"}`), AgentKey: "tr_agent_" + strings.Repeat("a", 48)}
	require.NoError(t, integrations.Upsert(ctx, pi))
	task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, Title: "relay registry runtime", StatusID: f.statusID, Priority: domain.PriorityMedium, AssigneeType: domain.AssigneeTypeUnassigned, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, f.taskSvc.Create(ctx, task))
	publisher := teamrelay.NewClient(integrations, postgres.NewTaskRepo(f.db), projects, provider)
	search := NewProjectIntegrationService(integrations, WithProjectIntegrationRelayResolver(provider, projects))
	t.Run("instance only Publish", func(t *testing.T) {
		publicURL, key, err := publisher.Publish(ctx, task.ID, "doc.md", []byte("body"), "text/markdown")
		require.NoError(t, err)
		require.Equal(t, relay.URL+"/published", publicURL)
		require.Equal(t, pi.AgentKey, key)
	})
	t.Run("instance only SearchTR", func(t *testing.T) {
		files, err := search.SearchTR(ctx, "registry-runtime", "doc", 10)
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.Equal(t, "docs/doc.md", files[0].Path)
	})
	t.Run("disabled project refuses search", func(t *testing.T) {
		pi.Enabled = false
		require.NoError(t, integrations.Upsert(ctx, pi))
		before := hits
		_, searchErr := search.SearchTR(ctx, "registry-runtime", "", 10)
		require.Error(t, searchErr)
		require.Equal(t, before, hits)
		pi.Enabled = true
		require.NoError(t, integrations.Upsert(ctx, pi))
	})
	t.Run("transport off blocks publication but allows search", func(t *testing.T) {
		row.Settings = json.RawMessage(`{"relay_url":"` + relay.URL + `","transport_enabled":false}`)
		require.NoError(t, instances.Upsert(ctx, row, nil))
		before := hits
		publicURL, _, err := publisher.Publish(ctx, task.ID, "doc.md", []byte("body"), "text/markdown")
		require.NoError(t, err)
		require.Empty(t, publicURL)
		require.Equal(t, before, hits)
		files, err := search.SearchTR(ctx, "registry-runtime", "doc", 10)
		require.NoError(t, err)
		require.Len(t, files, 1)
	})
	t.Run("workspace override and explicit disable", func(t *testing.T) {
		wsHits := 0
		wsRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wsHits++
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodPost {
				w.Write([]byte(`{"path":"doc.md"}`))
			} else {
				w.Write([]byte(`{"web_folder_items":[]}`))
			}
		}))
		defer wsRelay.Close()
		config, _ := json.Marshal(map[string]any{"relay_url": wsRelay.URL, "transport_enabled": true})
		conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: f.workspaceID, Provider: domain.IntegrationProviderTeamRelay, IsActive: true, Config: config}
		conns := postgres.NewIntegrationRepo(f.db)
		require.NoError(t, conns.Upsert(ctx, conn))
		before := hits
		_, _, err := publisher.Publish(ctx, task.ID, "doc.md", []byte("body"), "text/markdown")
		require.NoError(t, err)
		_, err = search.SearchTR(ctx, "registry-runtime", "", 10)
		require.NoError(t, err)
		require.Equal(t, 2, wsHits)
		require.Equal(t, before, hits)
		conn.IsActive = false
		require.NoError(t, conns.Upsert(ctx, conn))
		_, _, err = publisher.Publish(ctx, task.ID, "doc.md", []byte("body"), "text/markdown")
		require.NoError(t, err)
		_, err = search.SearchTR(ctx, "registry-runtime", "", 10)
		require.Error(t, err)
		require.Equal(t, 2, wsHits)
		require.Equal(t, before, hits)
	})
}
