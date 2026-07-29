package service

import (
	"context"
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func newNotifServiceMock(t *testing.T) (NotificationService, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := postgres.NewNotificationRepo(sqlx.NewDb(rawDB, "postgres"))
	return NewNotificationService(repo), mock
}

// TestDeletePreferences_ReportsWhetherARowWasThere: the handler answers 204 either
// way, but it is the service that knows, and a caller that needs the distinction
// (an audit log, a future admin view) should not have to re-query for it.
func TestDeletePreferences_ReportsWhetherARowWasThere(t *testing.T) {
	wsID := uuid.New()
	userID := uuid.New()

	t.Run("a row was removed", func(t *testing.T) {
		svc, mock := newNotifServiceMock(t)
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM notification_preferences`)).
			WithArgs(wsID, "web_push", userID).
			WillReturnResult(sqlmock.NewResult(0, 1))

		removed, err := svc.DeletePreferences(context.Background(), wsID, &userID, nil, "web_push")
		require.NoError(t, err)
		assert.True(t, removed)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("there was nothing to remove", func(t *testing.T) {
		svc, mock := newNotifServiceMock(t)
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM notification_preferences`)).
			WithArgs(wsID, "web_push", userID).
			WillReturnResult(sqlmock.NewResult(0, 0))

		removed, err := svc.DeletePreferences(context.Background(), wsID, &userID, nil, "web_push")
		require.NoError(t, err)
		assert.False(t, removed)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("a database error is not reported as a successful removal", func(t *testing.T) {
		svc, mock := newNotifServiceMock(t)
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM notification_preferences`)).
			WithArgs(wsID, "web_push", userID).
			WillReturnError(errors.New("connection reset"))

		removed, err := svc.DeletePreferences(context.Background(), wsID, &userID, nil, "web_push")
		require.Error(t, err)
		assert.False(t, removed)
	})
}

// TestDispatch_ReadsOnlyDeliverablePreferences pins the call dispatch makes.
//
// The loop that follows it filters on the channel and the event type and nothing
// else — it never asks whose preference row it is holding. So the membership
// question has to be settled by the read, and this is the assertion that it still
// is: the statement dispatch issues must be the one that joins membership, not a
// bare select on workspace_id.
func TestDispatch_ReadsOnlyDeliverablePreferences(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	wsID := uuid.New()
	done := make(chan struct{})

	mock.ExpectQuery(`workspace_members`).
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "workspace_id", "user_id", "agent_id", "channel",
			"events", "is_enabled", "config", "created_at", "updated_at",
		})).
		RowsWillBeClosed()

	svc := NewNotificationService(postgres.NewNotificationRepo(sqlx.NewDb(rawDB, "postgres")))

	go func() {
		defer close(done)
		svc.(*notificationService).dispatch(domain.NotificationEvent{
			WorkspaceID: wsID,
			EventType:   "comment.created",
			Title:       "New comment",
			Body:        "body",
		})
	}()
	<-done

	assert.NoError(t, mock.ExpectationsWereMet(),
		"dispatch did not read preferences through the membership-filtered query")
}
