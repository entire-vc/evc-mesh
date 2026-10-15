package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rows written before migration 20261013007 have message_params IS NULL (the
// column is nullable and was never backfilled). A mock cannot show what the
// driver does with a real NULL, so this reads a row seeded by direct SQL —
// ListUnread used to fail for the whole user on the first such row, which
// surfaced as a 500 on GET /api/v1/notifications.
func TestListUnreadDB_RowWithNullMessageParamsIsReturned(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	wsID, userID := notifPrefsFixture(t, db)

	legacyID, withParamsID := uuid.New(), uuid.New()
	_, err := db.Exec(
		`INSERT INTO notifications (id, workspace_id, user_id, event_type, title, body, message_params)
		 VALUES ($1, $2, $3, 'task_assigned', 'legacy', 'no params', NULL)`,
		legacyID, wsID, userID)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO notifications (id, workspace_id, user_id, event_type, title, body, message_code, message_params)
		 VALUES ($1, $2, $3, 'task_assigned', 'new', 'with params', 'task.assigned', '{"task":"T"}')`,
		withParamsID, wsID, userID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM notifications WHERE user_id = $1`, userID) })

	items, err := repo.ListUnread(ctx, userID, 50)
	require.NoError(t, err)
	require.Len(t, items, 2)

	byID := map[uuid.UUID]int{}
	for i, it := range items {
		byID[it.ID] = i
	}
	assert.Empty(t, items[byID[legacyID]].MessageParams, "NULL must read as no params")
	assert.JSONEq(t, `{"task":"T"}`, string(items[byID[withParamsID]].MessageParams))
}
