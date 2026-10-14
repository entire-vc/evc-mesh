package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// webhook_deliveries shows an extension delivery, with its retry, against real
// Postgres: the receiver answers 500 then 200, and the table ends with one row
// owned by the subscription (webhook_id NULL), attempt 2, success.
func TestExtensionDelivery_WebhookDeliveriesRowAndRetry_DB(t *testing.T) {
	db := extEventSubTestDB(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	extID := "evdel-" + suffix
	owner := &domain.User{
		ID: uuid.New(), Email: "evdel-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Ext Delivery Owner", Username: "evdel-" + suffix, IsActive: true,
	}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, owner))
	ws := &domain.Workspace{ID: uuid.New(), Name: "evdel " + suffix, Slug: "evdel-" + suffix, OwnerID: owner.ID}
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
	installed := postgres.NewInstalledExtensionRepo(db)
	require.NoError(t, installed.UpsertBuiltin(ctx, &domain.InstalledExtension{
		ExtensionID: extID, Version: "1.0.0", Manifest: manifest, TrustLevel: domain.ExtensionTrustBuiltin,
	}))
	integrations := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProvider(extID), Config: []byte("{}"), IsActive: true}
	require.NoError(t, integrations.Upsert(ctx, conn))

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	subs := postgres.NewExtensionEventSubscriptionRepo(db)
	subSvc := NewExtensionEventSubscriptionService(subs, integrations, installed)
	sub, err := subSvc.Subscribe(ctx, CreateExtensionEventSubscriptionInput{
		ConnectionID: conn.ID, Event: extensions.EventTaskUpdated,
		Delivery: domain.ExtensionDeliveryWebhook, Target: srv.URL, SecretRef: "ext-hook-secret",
	})
	require.NoError(t, err)

	svc := &webhookService{
		repo:     postgres.NewWebhookRepo(db),
		client:   newWebhookHTTPClient(func(net.IP) bool { return true }),
		backoffs: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
		ext: &extensionDelivery{
			subs: subs, integrations: integrations,
			secrets: &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: "ext-hook-secret", Value: "v"}}},
		},
	}
	svc.Dispatch(ctx, ws.ID, "task.updated", map[string]any{"task_id": "t1"})

	type row struct {
		WebhookID      *uuid.UUID `db:"webhook_id"`
		SubscriptionID *uuid.UUID `db:"subscription_id"`
		EventType      string     `db:"event_type"`
		Success        bool       `db:"success"`
		Attempt        int        `db:"attempt"`
		ResponseStatus *int       `db:"response_status"`
	}
	var rows []row
	require.Eventually(t, func() bool {
		rows = nil
		require.NoError(t, db.Select(&rows, `SELECT webhook_id, subscription_id, event_type, success, attempt, response_status
			FROM webhook_deliveries WHERE subscription_id = $1`, sub.ID))
		return len(rows) == 1
	}, 5*time.Second, 20*time.Millisecond)

	assert.Nil(t, rows[0].WebhookID)
	assert.Equal(t, "task.updated", rows[0].EventType)
	assert.True(t, rows[0].Success)
	assert.Equal(t, 2, rows[0].Attempt, "500 first, then 200")
	require.NotNil(t, rows[0].ResponseStatus)
	assert.Equal(t, 200, *rows[0].ResponseStatus)
	assert.EqualValues(t, 2, hits.Load())
}
