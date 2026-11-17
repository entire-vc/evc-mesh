package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// D2: removing a repository extension from the instance stops it
// everywhere — it is no longer installed or registered (no slots), no event
// reaches it and its token answers 401 — while its data stays: a reinstall of
// the same version sees the ext_<id>_* row and the workspace settings again.
func TestExtensionUninstall_StopsExtensionKeepsData_DB(t *testing.T) {
	db := extEventSubTestDB(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	// A fixed id keeps the data table name a literal (ext_<id>_ prefix of the
	// extension data convention); leftovers of an earlier run are cleared first.
	const extID = "uninst-d2-keep"
	clearExt := func() {
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, extID)
		_, _ = db.Exec(`DELETE FROM extension_removals WHERE extension_id = $1`, extID)
		_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, extID)
		_, _ = db.Exec(`DROP TABLE IF EXISTS ext_uninst_d2_keep_notes`)
	}
	clearExt()
	owner := &domain.User{
		ID: uuid.New(), Email: "uninst-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Uninstall Owner", Username: "uninst-" + suffix, IsActive: true,
	}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, owner))
	ws := &domain.Workspace{ID: uuid.New(), Name: "uninst " + suffix, Slug: "uninst-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
		clearExt()
		_, _ = db.Exec(`DELETE FROM users WHERE id = $1`, owner.ID)
	})

	manifest := []byte(strings.Replace(helloManifest, `"id":"hello"`, `"id":"`+extID+`"`, 1))
	installed := postgres.NewInstalledExtensionRepo(db)
	repoExt := postgres.NewRepoExtensionRepo(db)
	install := func() {
		require.NoError(t, repoExt.InsertRepo(ctx, &domain.InstalledExtension{
			ExtensionID: extID, Version: "0.1.0", Manifest: manifest, Source: domain.ExtensionSourceRepo,
			RepoURL: "https://example.com/" + extID + ".git", RepoRef: "v0.1.0", CommitSHA: strings.Repeat("a", 40),
			TrustLevel: domain.ExtensionTrustCustom,
		}, "", nil))
	}
	install()

	// The extension's own data, kept under its ext_<id>_ prefix.
	_, err := db.Exec(`CREATE TABLE ext_uninst_d2_keep_notes (id SERIAL PRIMARY KEY, body TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ext_uninst_d2_keep_notes (body) VALUES ($1)`, "kept across reinstall")
	require.NoError(t, err)

	integrations := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProvider(extID),
		Config: []byte(`{"hello_url":"https://hello.example.com"}`), IsActive: true}
	require.NoError(t, integrations.Upsert(ctx, conn))

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	subs := postgres.NewExtensionEventSubscriptionRepo(db)
	sub, err := NewExtensionEventSubscriptionService(subs, integrations, installed).Subscribe(ctx, CreateExtensionEventSubscriptionInput{
		ConnectionID: conn.ID, Event: extensions.EventTaskUpdated,
		Delivery: domain.ExtensionDeliveryWebhook, Target: srv.URL, SecretRef: "ext-hook-secret",
	})
	require.NoError(t, err)
	hooks := &webhookService{
		repo:     postgres.NewWebhookRepo(db),
		client:   newWebhookHTTPClient(func(net.IP) bool { return true }),
		backoffs: []time.Duration{time.Millisecond},
		ext: &extensionDelivery{subs: subs, integrations: integrations,
			secrets: &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}}}},
	}
	deliveries := func() int {
		var n int
		require.NoError(t, db.Get(&n, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, sub.ID))
		return n
	}

	actors := NewExtensionActorService(postgres.NewExtensionActorRepo(db), integrations, postgres.NewWorkspaceRepo(db))
	issued, err := actors.Issue(ctx, ws.ID, conn.ID, extID, "Uninstall Test", []string{"api:tasks:read", "events:task.updated"})
	require.NoError(t, err)

	reg := extensions.NewRegistry()
	rt := &external.Runtime{Reg: reg, Installed: installed, Client: &http.Client{Timeout: time.Second},
		HS: &external.Handshaker{Client: &http.Client{Timeout: time.Second}}}
	require.NoError(t, rt.Sync(ctx))

	// Positive control: installed, registered, delivered to, token accepted.
	_, registered := reg.Get(extID)
	require.True(t, registered, "installed external extension is registered")
	hooks.Dispatch(ctx, ws.ID, "task.updated", map[string]any{"task_id": "before"})
	require.Eventually(t, func() bool { return deliveries() == 1 }, 5*time.Second, 20*time.Millisecond)
	_, err = actors.Authenticate(ctx, issued.Token)
	require.NoError(t, err, "token works before the uninstall")

	admin := owner.ID
	rep, err := postgres.NewExtensionUninstallRepo(db).Uninstall(ctx, extID,
		&domain.InstanceExtensionJournalEntry{ActorID: &admin, Action: domain.InstanceJournalUninstalled})
	require.NoError(t, err)
	assert.Equal(t, extID, rep.ExtensionID)
	assert.EqualValues(t, 1, rep.Connections)
	assert.EqualValues(t, 1, rep.TokensRevoked)
	require.NoError(t, rt.Sync(ctx))

	// Not in the installed list and not registered: no slot is served.
	rows, err := installed.List(ctx)
	require.NoError(t, err)
	for _, r := range rows {
		require.NotEqual(t, extID, r.ExtensionID, "uninstalled extension is still listed")
	}
	_, registered = reg.Get(extID)
	assert.False(t, registered, "uninstalled extension is still registered")

	// No event is delivered after the uninstall.
	before := hits.Load()
	hooks.Dispatch(ctx, ws.ID, "task.updated", map[string]any{"task_id": "after"})
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, deliveries(), "an event reached the uninstalled extension")
	assert.Equal(t, before, hits.Load())

	// The token is refused with 401.
	_, err = actors.Authenticate(ctx, issued.Token)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "want an API error, got %v", err)
	assert.Equal(t, http.StatusUnauthorized, apiErr.Code)

	var action string
	require.NoError(t, db.Get(&action, `SELECT action FROM instance_extension_journal WHERE extension_id = $1 ORDER BY created_at DESC LIMIT 1`, extID))
	assert.Equal(t, domain.InstanceJournalUninstalled, action)

	// Second uninstall: nothing left to remove.
	_, err = postgres.NewExtensionUninstallRepo(db).Uninstall(ctx, extID, nil)
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusNotFound, apiErr.Code)

	// Reinstall the same version: the data row and the workspace settings are there.
	install()
	var body string
	require.NoError(t, db.Get(&body, `SELECT body FROM ext_uninst_d2_keep_notes LIMIT 1`))
	assert.Equal(t, "kept across reinstall", body)
	kept, err := integrations.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "the workspace connection was deleted")
	assert.JSONEq(t, `{"hello_url":"https://hello.example.com"}`, string(kept.Config))
	assert.False(t, kept.IsActive, "a reinstall does not switch workspaces back on by itself")
}

// A built-in extension is never removed: 409, the row stays.
func TestExtensionUninstall_BuiltinRefused_DB(t *testing.T) {
	db := extEventSubTestDB(t)
	ctx := context.Background()
	extID := "uninst-builtin-" + uuid.New().String()[:8]
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, extID) })
	installed := postgres.NewInstalledExtensionRepo(db)
	require.NoError(t, installed.UpsertBuiltin(ctx, &domain.InstalledExtension{
		ExtensionID: extID, Version: "1.0.0", Manifest: []byte(`{"id":"` + extID + `","kind":"integration"}`), TrustLevel: domain.ExtensionTrustBuiltin,
	}))
	_, err := postgres.NewExtensionUninstallRepo(db).Uninstall(ctx, extID, nil)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "want an API error, got %v", err)
	assert.Equal(t, http.StatusConflict, apiErr.Code)
	row, err := installed.Get(ctx, extID)
	require.NoError(t, err)
	assert.NotNil(t, row, "the built-in row was removed")
}
