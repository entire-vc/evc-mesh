package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Runs against real Postgres like the other *_db_test.go files in this
// package: the rules under test (public list, manifest capabilities, unique
// row) live across the service and the table. Skips when no database is
// reachable.
func extEventSubTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestExtensionEventSubscription_PublicListAndCapabilities(t *testing.T) {
	db := extEventSubTestDB(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	extID := "evsub-" + suffix
	owner := &domain.User{
		ID: uuid.New(), Email: "evsub-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Ext Event Owner", Username: "evsub-" + suffix, IsActive: true,
	}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, owner))
	ws := &domain.Workspace{ID: uuid.New(), Name: "evsub " + suffix, Slug: "evsub-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, extID)
	})

	manifest, err := json.Marshal(map[string]any{
		"id": extID, "version": "1.0.0", "runtime": "builtin",
		"capabilities": map[string]any{"events": []string{extensions.EventTaskUpdated}},
	})
	require.NoError(t, err)
	require.NoError(t, postgres.NewInstalledExtensionRepo(db).UpsertBuiltin(ctx, &domain.InstalledExtension{
		ExtensionID: extID, Version: "1.0.0", Manifest: manifest, TrustLevel: domain.ExtensionTrustBuiltin,
	}))
	integrations := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProvider(extID), Config: []byte("{}"), IsActive: true}
	require.NoError(t, integrations.Upsert(ctx, conn))

	svc := NewExtensionEventSubscriptionService(postgres.NewExtensionEventSubscriptionRepo(db), integrations, postgres.NewInstalledExtensionRepo(db))
	hook := func(event string) CreateExtensionEventSubscriptionInput {
		return CreateExtensionEventSubscriptionInput{
			ConnectionID: conn.ID, Event: event, Delivery: domain.ExtensionDeliveryWebhook,
			Target: "https://ext.example.com/hook", SecretRef: "ext-signing",
		}
	}
	statusOf := func(err error) int {
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		return apiErr.StatusCode()
	}

	// An event that is not on the public list: 400.
	_, err = svc.Subscribe(ctx, hook("task.exploded"))
	assert.Equal(t, 400, statusOf(err))

	// On the public list but absent from the manifest's capabilities.events: 403.
	_, err = svc.Subscribe(ctx, hook(extensions.EventCommentCreated))
	assert.Equal(t, 403, statusOf(err))

	// Declared in capabilities: stored, and a repeat returns the same row.
	first, err := svc.Subscribe(ctx, hook(extensions.EventTaskUpdated))
	require.NoError(t, err)
	again, err := svc.Subscribe(ctx, hook(extensions.EventTaskUpdated))
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	list, err := svc.ListByConnection(ctx, conn.ID)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// A webhook without a signing secret reference is refused before any lookup.
	noSecret := hook(extensions.EventTaskUpdated)
	noSecret.SecretRef = ""
	_, err = svc.Subscribe(ctx, noSecret)
	assert.Equal(t, 400, statusOf(err))

	require.NoError(t, svc.Unsubscribe(ctx, first.ID))
	assert.Equal(t, 404, statusOf(svc.Unsubscribe(ctx, first.ID)))
}
