package postgres

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The DB tests in runtime_reservation_db_test.go need DATABASE_URL; these pin
// the refusal mapping without a database.

func writerHolderMock(t *testing.T) (*sqlx.DB, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return sqlx.NewDb(raw, "postgres"), mock
}

func asRefusal(t *testing.T, err error) *apierror.Error {
	t.Helper()
	var refusal *apierror.Error
	require.True(t, errors.As(err, &refusal), "got %v", err)
	require.Equal(t, http.StatusConflict, refusal.Code)
	return refusal
}

func TestTaskWriterActiveNamesHolderAndExpiry(t *testing.T) {
	db, mock := writerHolderMock(t)
	reservation, agent, task := uuid.New(), uuid.New(), uuid.New()
	expires := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM runtime_reservations r").WithArgs(task).WillReturnRows(
		sqlmock.NewRows([]string{"id", "agent_id", "agent_name", "state", "expires_at"}).
			AddRow(reservation, agent, "Dana", "reserved", expires))

	refusal := asRefusal(t, runtimeTaskWriterActive(context.Background(), db, task))
	require.Equal(t, "task_writer_active", refusal.Details)
	require.Equal(t, "2026-10-19T21:00:00Z", refusal.Context["expires_at"])
	require.Equal(t, map[string]any{"reservation_id": reservation, "agent_id": agent, "agent_name": "Dana", "state": "reserved"}, refusal.Context["holder"])
	require.NotContains(t, refusal.Context, "blocks_until")
}

func TestTaskWriterActiveConsumedHolderBlocksUntilRelease(t *testing.T) {
	db, mock := writerHolderMock(t)
	task := uuid.New()
	mock.ExpectQuery("FROM runtime_reservations r").WithArgs(task).WillReturnRows(
		sqlmock.NewRows([]string{"id", "agent_id", "agent_name", "state", "expires_at"}).
			AddRow(uuid.New(), uuid.New(), "Grace", "consumed", time.Now().Add(-time.Hour)))

	refusal := asRefusal(t, runtimeTaskWriterActive(context.Background(), db, task))
	require.Equal(t, "release", refusal.Context["blocks_until"])
	require.NotContains(t, refusal.Context, "expires_at", "a stored expiry in the past would misstate the block")
}

func TestTaskWriterActiveKeepsReasonWhenHolderUnreadable(t *testing.T) {
	db, mock := writerHolderMock(t)
	task := uuid.New()
	mock.ExpectQuery("FROM runtime_reservations r").WithArgs(task).WillReturnError(errors.New("connection reset"))

	refusal := asRefusal(t, runtimeTaskWriterActive(context.Background(), db, task))
	require.Equal(t, "task_writer_active", refusal.Details)
	require.Nil(t, refusal.Context)
}

func TestRuntimeUniqueConflictMapsConstraints(t *testing.T) {
	db, mock := writerHolderMock(t)
	task := uuid.New()
	unique := func(constraint string) error { return &pq.Error{Code: "23505", Constraint: constraint} }

	mock.ExpectQuery("FROM runtime_reservations r").WithArgs(task).WillReturnRows(
		sqlmock.NewRows([]string{"id", "agent_id", "agent_name", "state", "expires_at"}).
			AddRow(uuid.New(), uuid.New(), "Dana", "reserved", time.Now().Add(time.Minute)))
	race := asRefusal(t, runtimeUniqueConflict(context.Background(), db, task, unique("runtime_reservations_active_task")))
	require.Equal(t, "task_writer_active", race.Details)
	require.Contains(t, race.Context, "holder", "the race path names the holder too")

	for constraint, reason := range map[string]string{
		"runtime_reservations_active_worker":               "worker_active",
		"runtime_reservation_pool_claims_pool_id_slot_key": "pool_exhausted",
		"something_else": "idempotency_scope_mismatch",
	} {
		require.Equal(t, reason, asRefusal(t, runtimeUniqueConflict(context.Background(), db, task, unique(constraint))).Details)
	}

	plain := errors.New("not a unique violation")
	require.Same(t, plain, runtimeUniqueConflict(context.Background(), db, task, plain))
	require.NoError(t, mock.ExpectationsWereMet())
}
