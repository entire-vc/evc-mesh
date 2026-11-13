package service

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// consentBackfill is the data part of migration 20261112400 (both UPDATEs),
// read from the migration file so the test runs exactly what deploys.
func consentBackfill(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/20261112400_extension_consent_version.sql")
	require.NoError(t, err)
	up := string(raw)
	start := strings.Index(up, "UPDATE integration_configs ic")
	end := strings.Index(up, "-- +goose Down")
	require.True(t, start > 0 && end > start, "the backfill statements are in the migration")
	return up[start:end]
}

func backfillWorkspace(t *testing.T, db *sqlx.DB) (ws *domain.Workspace, conn func(provider string, consent []string) uuid.UUID) {
	t.Helper()
	ws = &domain.Workspace{ID: uuid.New(), Name: "backfill", Slug: "backfill-" + uuid.New().String()[:8], OwnerID: agentKeyWsReuseOwner(t, db)}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(context.Background(), ws))
	return ws, func(provider string, consent []string) uuid.UUID {
		id := uuid.New()
		var c any
		if consent != nil {
			b, _ := json.Marshal(consent)
			c = string(b)
		}
		_, ierr := db.Exec(`INSERT INTO integration_configs (id, workspace_id, provider, config, is_active, consented_capabilities)
			VALUES ($1, $2, $3, '{}', true, $4::jsonb)`, id, ws.ID, provider, c)
		require.NoError(t, ierr)
		return id
	}
}

// installBuiltinRow is the installed_extensions row SyncBuiltin writes for a
// builtin: its manifest carries a network object, which is not a right.
func installBuiltinRow(t *testing.T, db *sqlx.DB, id, capabilities string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level)
		VALUES ($1, '1.0.0', $2, 'builtin', 'official')`, id,
		`{"id":"`+id+`","name":"B","version":"1.0.0","kind":"integration","runtime":"builtin","category":"vcs",
 "levels":{"instance":false,"workspace":true,"project":false},"capabilities":`+capabilities+`}`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id) })
}

// The backfill of migration 20261112400, run again on purpose-made rows: a
// connection with an actor and no consent record gets the actor's snapshot as
// its consent; a builtin connection with no record gets the declared set of its
// builtin manifest (what its status always reported); one that already has a
// consent keeps it; an external one with neither stays without a record (shown
// as needing consent).
func TestConsentBackfill_FromActorSnapshotDB(t *testing.T) {
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	backfill := consentBackfill(t)
	ws, conn := backfillWorkspace(t, db)
	suffix := uuid.New().String()[:8]
	builtinID, repoID := "bf-builtin-"+suffix, "bf-repo-"+suffix
	installBuiltinRow(t, db, builtinID, `{"api":["tasks:read","tasks:write"],"secrets":["token"],"events":[],"network":{"egress":["api.example.com"]}}`)
	installRepoRelease(t, db, repoID, "0.1.0", "c1", reconsentManifest(repoID, "0.1.0", []string{"tasks:read"}, []string{"task.updated"}))
	actor := func(connID uuid.UUID, ext, caps string) {
		_, ierr := db.Exec(`INSERT INTO extension_actors (connection_id, workspace_id, actor_id, extension_id, token_hash, capabilities)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, connID, ws.ID, uuid.New(), ext, hex64(), caps)
		require.NoError(t, ierr)
	}
	withActor := conn("bf-a", nil)
	actor(withActor, "a", `{"events":["task.updated"],"api":["tasks:read","docs:read"]}`)
	consented := conn("bf-b", []string{"api:tasks:read"})
	actor(consented, "b", `{"api":["tasks:read","tasks:write"]}`)
	conn("bf-c", nil)
	conn(builtinID, nil)
	builtinEmpty := "bf-builtin-empty-" + suffix
	installBuiltinRow(t, db, builtinEmpty, `{"api":["agents:read"]}`)
	conn(builtinEmpty, []string{})
	conn(repoID, nil)

	_, err := db.Exec(backfill)
	require.NoError(t, err)

	consents := postgres.NewIntegrationConsentRepo(db)
	got := func(ext string) *domain.ExtensionConsent {
		c, gerr := consents.GetConsent(ctx, ws.ID, ext)
		require.NoError(t, gerr)
		return c
	}
	a := got("bf-a")
	require.NotNil(t, a, "backfilled from the actor")
	assert.Equal(t, []string{"api:docs:read", "api:tasks:read", "events:task.updated"}, a.Capabilities)
	assert.False(t, a.At.IsZero(), "consented_at is the actor's issue time")
	assert.Equal(t, "", a.Version, "no release is claimed for a backfilled consent")
	b := got("bf-b")
	require.NotNil(t, b)
	assert.Equal(t, []string{"api:tasks:read"}, b.Capabilities, "an existing consent is not overwritten by the snapshot")
	assert.Nil(t, got("bf-c"), "no actor, no record")
	bi := got(builtinID)
	require.NotNil(t, bi, "builtin backfilled from its manifest")
	assert.Equal(t, []string{"api:tasks:read", "api:tasks:write", "secrets:token"}, bi.Capabilities, "the declared set, network is not a right")
	assert.Equal(t, "", bi.Version)
	be := got(builtinEmpty)
	require.NotNil(t, be)
	assert.Empty(t, be.Capabilities, "an existing (empty) record is kept")
	assert.Nil(t, got(repoID), "an external connection is not given its declared set")

	// Idempotent: a second run changes nothing.
	_, err = db.Exec(backfill)
	require.NoError(t, err)
	assert.Equal(t, bi.Capabilities, got(builtinID).Capabilities)
}

// A builtin connection made before consent was stored behaves after this
// migration as before it: the connection view does not ask for a consent, and
// status reports the declared set (before, status fell back to it). Builtins
// act on neither ext_ tokens nor push events, so nothing else reads it.
func TestConsentBackfill_BuiltinWithoutRecordBehavesAsBeforeDB(t *testing.T) {
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	ws, conn := backfillWorkspace(t, db)
	id := "bf-legacy-" + uuid.New().String()[:8]
	raw := `{"id":"` + id + `","name":"Legacy","version":"1.0.0","kind":"integration","runtime":"builtin","category":"vcs",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["agent_keys:read","agents:read"],"events":[]}}`
	man, err := extensions.ParseManifest([]byte(raw))
	require.NoError(t, err)
	integ := postgres.NewIntegrationRepo(db)
	consents := postgres.NewIntegrationConsentRepo(db)
	reg := extensions.NewRegistry().WithIntegrationConsents(consents)
	require.NoError(t, reg.Register(exttest.DemoProvider{BaseProvider: extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: integ}}, Healthy: true}))
	require.NoError(t, reg.SyncBuiltin(ctx, postgres.NewInstalledExtensionRepo(db)))
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id) })
	conn(id, nil)
	wsSvc := NewExtensionWorkspaceService(reg, integ, consents).WithInstalledReleases(postgres.NewInstalledExtensionRepo(db))
	view := func() *WorkspaceExtensionView {
		list, lerr := wsSvc.List(ctx, ws.ID)
		require.NoError(t, lerr)
		for i := range list {
			if list[i].ID == id {
				return &list[i]
			}
		}
		t.Fatalf("%s not listed", id)
		return nil
	}
	require.True(t, view().Connection.NeedsConsent, "positive control: without the backfill the legacy row would ask for consent")

	_, err = db.Exec(consentBackfill(t))
	require.NoError(t, err)
	v := view()
	assert.False(t, v.Connection.NeedsConsent)
	assert.Nil(t, v.Connection.PendingConsent)
	assert.Equal(t, []string{"api:agent_keys:read", "api:agents:read"}, v.Status.ConsentedCapabilities, "what status reported before: the declared set")
	assert.True(t, v.Status.Enabled)
}

func hex64() string {
	return strings.ReplaceAll(uuid.New().String()+uuid.New().String(), "-", "")
}
