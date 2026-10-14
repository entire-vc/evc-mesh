package postgres

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func newExtEventRepoMock(t *testing.T) (*ExtensionEventSubscriptionRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewExtensionEventSubscriptionRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

var extEventCols = []string{"id", "connection_id", "event", "delivery", "target", "secret_ref", "created_at"}

func TestExtensionEventSubscriptionRepo_CreateIsIdempotent(t *testing.T) {
	repo, mock := newExtEventRepoMock(t)
	id, conn := uuid.New(), uuid.New()
	mock.ExpectQuery(`INSERT INTO extension_event_subscriptions .* ON CONFLICT \(connection_id, event, delivery, target\)`).
		WithArgs(id, conn, "task.updated", "webhook", "https://x.example/h", "s").
		WillReturnRows(sqlmock.NewRows(extEventCols).AddRow(id, conn, "task.updated", "webhook", "https://x.example/h", "s", time.Now()))
	got, err := repo.Create(context.Background(), &domain.ExtensionEventSubscription{
		ID: id, ConnectionID: conn, Event: "task.updated", Delivery: domain.ExtensionDeliveryWebhook, Target: "https://x.example/h", SecretRef: "s",
	})
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExtensionEventSubscriptionRepo_Reads(t *testing.T) {
	repo, mock := newExtEventRepoMock(t)
	ctx := context.Background()
	id, conn := uuid.New(), uuid.New()
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows(extEventCols).AddRow(id, conn, "task.updated", "nats", "", "", time.Now())
	}

	mock.ExpectQuery(`FROM extension_event_subscriptions WHERE id = \$1`).WithArgs(id).WillReturnRows(row())
	got, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionDeliveryNATS, got.Delivery)

	mock.ExpectQuery(`FROM extension_event_subscriptions WHERE id = \$1`).WithArgs(id).WillReturnRows(sqlmock.NewRows(extEventCols))
	missing, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, missing, "an unknown id is (nil, nil), not an error")

	mock.ExpectQuery(`WHERE connection_id = \$1`).WithArgs(conn).WillReturnRows(row())
	byConn, err := repo.ListByConnection(ctx, conn)
	require.NoError(t, err)
	assert.Len(t, byConn, 1)

	mock.ExpectQuery(`WHERE event = \$1`).WithArgs("task.updated").WillReturnRows(row())
	byEvent, err := repo.ListByEvent(ctx, "task.updated")
	require.NoError(t, err)
	assert.Len(t, byEvent, 1)

	mock.ExpectExec(`DELETE FROM extension_event_subscriptions WHERE id = \$1`).WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.Delete(ctx, id))
	require.NoError(t, mock.ExpectationsWereMet())
}
