package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func reconsentManifest(id, version string, api, events []string) string {
	a, _ := json.Marshal(api)
	e, _ := json.Marshal(events)
	return `{"id":"` + id + `","name":"Memory","version":"` + version + `","kind":"integration","runtime":"builtin","category":"vcs",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":` + string(a) + `,"events":` + string(e) + `}}`
}

func installRepoRelease(t *testing.T, db *sqlx.DB, id, version, commit, manifest string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, commit_sha, trust_level)
		VALUES ($1, $2, $3, 'repo', $4, 'custom')
		ON CONFLICT (extension_id) DO UPDATE SET version = EXCLUDED.version, manifest = EXCLUDED.manifest,
			commit_sha = EXCLUDED.commit_sha, updated_at = now()`, id, version, manifest, commit)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id) })
}

// 0.3 -> 0.4 without a disconnect on a real Postgres, through the real
// workspace service, consent repository and actor service: the token issued
// under 0.3 does not gain 0.4's added right from the update alone, gains it on
// its next request after POST .../consent, and keeps its actor id, token,
// project scope and settings. A rollback to 0.3 then brings back neither the
// right 0.4 added (no longer declared) nor the one 0.4 dropped (no longer
// consented to).
func TestReconsent_SameTokenGainsAddedRightOnlyAfterConsentDB(t *testing.T) {
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "reconsent", Slug: "reconsent-" + uuid.New().String()[:8], OwnerID: agentKeyWsReuseOwner(t, db)}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	extID := "memsvc-" + uuid.New().String()[:8]
	m03 := reconsentManifest(extID, "0.3.0", []string{"tasks:read", "docs:read"}, []string{"task.updated"})
	m04 := reconsentManifest(extID, "0.4.0", []string{"tasks:read", "tasks:write"}, []string{"task.created", "task.updated"})

	integ := postgres.NewIntegrationRepo(db)
	consents := postgres.NewIntegrationConsentRepo(db)
	installed := postgres.NewInstalledExtensionRepo(db)
	reg := extensions.NewRegistry()
	register := func(raw string) {
		man, err := extensions.ParseManifest([]byte(raw))
		require.NoError(t, err)
		require.NoError(t, reg.Replace(exttest.DemoProvider{BaseProvider: extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: integ}}, Healthy: true}))
	}
	register(m03)
	installRepoRelease(t, db, extID, "0.3.0", "c3", m03)
	wsSvc := NewExtensionWorkspaceService(reg, integ, consents).WithInstalledReleases(installed)
	actors := WithDeclaredCapabilities(WithConsents(
		NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, postgres.NewWorkspaceRepo(db)), consents),
		func(id string) ([]string, bool) {
			p, ok := reg.Get(id)
			if !ok {
				return nil, false
			}
			return extensions.DeclaredCapabilities(p.Manifest().Capabilities), true
		})

	_, err := wsSvc.Connect(ctx, ws.ID, extID, nil, map[string]any{"base_url": "https://memory.example"},
		[]string{"api:docs:read", "api:tasks:read", "events:task.updated"})
	require.NoError(t, err)
	conn, err := integ.GetByProvider(ctx, ws.ID, domain.IntegrationProvider(extID))
	require.NoError(t, err)
	require.NotNil(t, conn)
	_, err = db.Exec(`UPDATE integration_configs SET project_scope = 'bound' WHERE id = $1`, conn.ID)
	require.NoError(t, err)
	issued, err := actors.Rotate(ctx, ws.ID, conn.ID) // the first token, from the connect-time consent
	require.NoError(t, err)
	token := issued.Token

	p, err := actors.Authenticate(ctx, token)
	require.NoError(t, err)
	require.True(t, p.HasAPI("docs:read"), "positive control: a consented, declared right acts")
	require.False(t, p.HasAPI("tasks:write"))

	// Instance update to 0.4: the row and the registered provider change.
	installRepoRelease(t, db, extID, "0.4.0", "c4", m04)
	register(m04)
	p, err = actors.Authenticate(ctx, token)
	require.NoError(t, err)
	assert.False(t, p.HasAPI("tasks:write"), "the update alone grants nothing")
	assert.False(t, p.HasAPI("docs:read"), "a right 0.4 dropped stops acting at once")
	assert.Equal(t, []string{"task.updated"}, p.Capabilities["events"])

	v, err := wsSvc.List(ctx, ws.ID)
	require.NoError(t, err)
	var pending *PendingConsentView
	for i := range v {
		if v[i].ID == extID {
			pending = v[i].Connection.PendingConsent
		}
	}
	require.NotNil(t, pending)
	assert.Equal(t, PendingConsentView{Version: "0.4.0", Commit: "c4", AddedCapabilities: []string{"api:tasks:write", "events:task.created"}, AddedEvents: []string{"task.created"}}, *pending)

	_, err = wsSvc.Reconsent(ctx, ws.ID, extID, nil, "0.4.0", "c3")
	require.Error(t, err, "another commit is refused")
	_, err = wsSvc.Reconsent(ctx, ws.ID, extID, nil, "0.4.0", "c4")
	require.NoError(t, err)

	p, err = actors.Authenticate(ctx, token)
	require.NoError(t, err)
	assert.True(t, p.HasAPI("tasks:write"), "the same token gains the added right after the consent")
	assert.Equal(t, []string{"task.created", "task.updated"}, p.Capabilities["events"])
	assert.Equal(t, issued.ActorID, p.ActorID, "same actor")
	assert.True(t, p.ProjectBound, "project scope kept")
	c, err := consents.GetConsent(ctx, ws.ID, extID)
	require.NoError(t, err)
	assert.Equal(t, "0.4.0", c.Version)
	assert.Equal(t, "c4", c.Commit)
	after, err := integ.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(conn.Config), string(after.Config), "settings kept")
	assert.True(t, after.IsActive)

	// Rollback of the instance to 0.3.
	installRepoRelease(t, db, extID, "0.3.0", "c3", m03)
	register(m03)
	p, err = actors.Authenticate(ctx, token)
	require.NoError(t, err)
	assert.True(t, p.HasAPI("tasks:read"))
	assert.False(t, p.HasAPI("tasks:write"), "0.3 does not declare it")
	assert.False(t, p.HasAPI("docs:read"), "the workspace no longer consents to it: the rollback does not bring it back")
}

// The consent transaction locks the installed release: an instance update
// that is in flight (row updated, not committed) makes the consent wait, and
// the consent then sees the new release, so it is refused rather than recorded
// for the old one.
func TestReconsent_WaitsForInFlightInstanceUpdateDB(t *testing.T) {
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "reconsent-lock", Slug: "reconsent-lock-" + uuid.New().String()[:8], OwnerID: agentKeyWsReuseOwner(t, db)}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	extID := "memsvc-" + uuid.New().String()[:8]
	installRepoRelease(t, db, extID, "0.4.0", "c4", reconsentManifest(extID, "0.4.0", []string{"tasks:read"}, nil))
	integ := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProvider(extID), Config: []byte(`{}`), IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, conn))
	consents := postgres.NewIntegrationConsentRepo(db)

	update, err := db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	_, err = update.Exec(`UPDATE installed_extensions SET version = '0.5.0', commit_sha = 'c5' WHERE extension_id = $1`, extID)
	require.NoError(t, err)

	seen := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, rerr := consents.Reconsent(ctx, conn.ID, extID, func(target domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
			seen <- target.Installed.Version
			if target.Installed.Version != "0.4.0" {
				return domain.ExtensionConsent{}, nil, errVersionChanged(release{Version: target.Installed.Version})
			}
			return domain.ExtensionConsent{Capabilities: []string{"api:tasks:read"}, Version: "0.4.0", Commit: "c4"}, []byte(`{"api":["tasks:read"]}`), nil
		})
		done <- rerr
	}()
	select {
	case v := <-seen:
		t.Fatalf("the consent read the release (%s) while the update was in flight", v)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, update.Commit())
	assert.Equal(t, "0.5.0", <-seen, "the consent sees the committed update")
	err = <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version changed")
	c, err := consents.GetConsent(ctx, ws.ID, extID)
	require.NoError(t, err)
	assert.Nil(t, c, "nothing recorded")
}
