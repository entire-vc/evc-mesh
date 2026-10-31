package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The event feed of one connection against real Postgres: originals only,
// oldest first, strictly after the cursor and no later than until, with
// delivered taken from the original or any successful redelivery.
func TestWebhookRepo_ListConnectionDeliveries(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	wsID := newDigestAgent(t, db, "d-"+uuid.New().String()).WorkspaceID
	ext := "evlog-" + uuid.New().String()[:8]

	conn, otherConn, sub, otherSub := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, c := range []uuid.UUID{conn, otherConn} {
		_, err := db.Exec(`INSERT INTO integration_configs (id, workspace_id, provider, is_active) VALUES ($1, $2, $3, true)`, c, wsID, ext+"-"+c.String()[:4])
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO extension_event_subscriptions (id, connection_id, event, delivery, target, secret_ref)
		VALUES ($1, $2, 'task.updated', 'webhook', 'https://hook.example/a', 's'), ($3, $4, 'task.updated', 'webhook', 'https://hook.example/b', 's')`,
		sub, conn, otherSub, otherConn)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM integration_configs WHERE id IN ($1, $2)`, conn, otherConn)
	})

	repo := NewWebhookRepo(db)
	base := time.Now().Add(-time.Hour).UTC()
	write := func(subID uuid.UUID, at time.Time, success bool, of *uuid.UUID) uuid.UUID {
		d := &domain.WebhookDelivery{
			ID: uuid.New(), SubscriptionID: &subID, EventType: "task.updated", Payload: []byte(`{}`),
			Success: success, Attempt: 1, CreatedAt: at, RedeliveryOf: of,
		}
		require.NoError(t, repo.CreateDelivery(ctx, d))
		return d.ID
	}
	a := write(sub, base, false, nil)
	b := write(sub, base.Add(time.Minute), false, nil)
	c := write(sub, base.Add(2*time.Minute), true, nil)
	write(sub, base.Add(3*time.Minute), false, &a) // failed resend of a
	write(sub, base.Add(4*time.Minute), true, &b)  // successful resend of b
	write(otherSub, base.Add(time.Minute), false, nil)
	late := write(sub, base.Add(50*time.Minute), false, nil)

	until := base.Add(10 * time.Minute)
	rows, err := repo.ListConnectionDeliveries(ctx, conn, domain.ExtensionEventCursor{}, until, 10)
	require.NoError(t, err)
	var ids []uuid.UUID
	delivered := map[uuid.UUID]bool{}
	for _, r := range rows {
		ids = append(ids, r.ID)
		delivered[r.ID] = r.Delivered
		assert.Nil(t, r.RedeliveryOf)
	}
	assert.Equal(t, []uuid.UUID{a, b, c}, ids, "originals of this connection only, oldest first, none after until")
	assert.NotContains(t, ids, late)
	assert.False(t, delivered[a], "a failed resend does not deliver")
	assert.True(t, delivered[b], "a successful resend delivers")
	assert.True(t, delivered[c])

	rows, err = repo.ListConnectionDeliveries(ctx, conn, domain.ExtensionEventCursor{CreatedAt: rows[0].CreatedAt, ID: rows[0].ID}, until, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, b, rows[0].ID, "strictly after the cursor, limit respected")

	got, err := repo.GetDelivery(ctx, b)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, sub, *got.SubscriptionID)
	missing, err := repo.GetDelivery(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, missing)
}
