package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// GET /notifications answered 500 for anyone holding a row written before
// message_params existed (the column is nullable and was not backfilled). The
// repository test covers the Scan; this covers what the caller sees: 200, the
// stored legacy title/body, and no message_params key.
func TestListNotificationsHandler_LegacyRowWithNullMessageParams(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for real PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	userID, wsID := uuid.New(), uuid.New()
	handle := "nullparams-" + userID.String()[:8]
	_, err = db.Exec(`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		VALUES ($1, $2, 'not-a-real-hash', 'Null Params', $3, true)`, userID, handle+"@example.test", handle)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID) })
	_, err = db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Null Params', $2, $3)`,
		wsID, "nullparams-ws-"+wsID.String()[:8], userID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })
	_, err = db.Exec(`INSERT INTO notifications (id, workspace_id, user_id, event_type, title, body, message_params)
		VALUES ($1, $2, $3, 'task_assigned', 'Old title', 'Old body', NULL)`, uuid.New(), wsID, userID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM notifications WHERE user_id = $1`, userID) })

	h := NewNotificationHandler(service.NewNotificationService(postgres.NewNotificationRepo(db)), nil)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/notifications", http.NoBody), rec)
	c.Set("user_id", userID)

	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Items       []map[string]any `json:"items"`
		UnreadCount int              `json:"unread_count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	assert.Equal(t, 1, body.UnreadCount)
	assert.Equal(t, "Old title", body.Items[0]["title"])
	assert.Equal(t, "Old body", body.Items[0]["body"])
	assert.NotContains(t, body.Items[0], "message_params")
}
