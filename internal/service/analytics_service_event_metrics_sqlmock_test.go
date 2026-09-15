package service

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// The real-Postgres tests in analytics_service_test.go
// (TestAnalyticsService_GetMetrics_EventMetrics_*) prove queryEventMetrics'
// happy path against a real database, per CLAUDE-workflow.md §1o — never mock
// the DB layer for business-logic assertions. They cannot, however, make a
// specific one of three sequential queries fail while its neighbors succeed;
// a real Postgres connection either works for all three or none. These
// sqlmock tests exist purely to give the three `if err != nil { return nil,
// err }` branches queryEventMetrics added (floorQ, totalQ, byTypeQ) coverage
// under the diff-coverage gate, exactly like the precedent set for
// TaskRepo.Create's activity-insert error path
// (task_repo_create_activity_sqlmock_test.go). They pin nothing about query
// text beyond a distinguishing substring — that's the real-Postgres tests'
// job.

func newAnalyticsServiceSQLMock(t *testing.T) (*analyticsService, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return &analyticsService{db: sqlx.NewDb(rawDB, "postgres")}, mock
}

func sampleEventMetricsFilter() AnalyticsFilter {
	return AnalyticsFilter{
		WorkspaceID: uuid.New(),
		From:        time.Now().AddDate(0, 0, -7),
		To:          time.Now(),
	}
}

// TestQueryEventMetrics_FloorQueryErrorPropagates covers the first of the
// three new error branches: a failure on the TTL-floor query (MIN(created_at))
// must short-circuit immediately — totalQ/byTypeQ must never run.
func TestQueryEventMetrics_FloorQueryErrorPropagates(t *testing.T) {
	svc, mock := newAnalyticsServiceSQLMock(t)
	driverErr := errors.New("connection reset by peer")

	mock.ExpectQuery(`SELECT MIN\(em\.created_at\) FROM event_bus_messages`).
		WillReturnError(driverErr)

	_, err := svc.queryEventMetrics(context.Background(), sampleEventMetricsFilter())
	require.Error(t, err)
	require.ErrorIs(t, err, driverErr)
	require.NoError(t, mock.ExpectationsWereMet(),
		"totalQ/byTypeQ must not run once the floor query has already failed")
}

// TestQueryEventMetrics_TotalQueryErrorPropagates covers the second branch:
// the floor query succeeds (queue currently empty for this scope — NULL
// MIN), but the period-filtered COUNT(*) fails.
func TestQueryEventMetrics_TotalQueryErrorPropagates(t *testing.T) {
	svc, mock := newAnalyticsServiceSQLMock(t)
	driverErr := errors.New("statement timeout")

	mock.ExpectQuery(`SELECT MIN\(em\.created_at\) FROM event_bus_messages`).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(nil))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM event_bus_messages`).
		WillReturnError(driverErr)

	_, err := svc.queryEventMetrics(context.Background(), sampleEventMetricsFilter())
	require.Error(t, err)
	require.ErrorIs(t, err, driverErr)
	require.NoError(t, mock.ExpectationsWereMet(),
		"byTypeQ must not run once totalQ has already failed")
}

// TestQueryEventMetrics_ByTypeQueryErrorPropagates covers the third branch:
// floorQ and totalQ both succeed, but the per-type breakdown query fails.
func TestQueryEventMetrics_ByTypeQueryErrorPropagates(t *testing.T) {
	svc, mock := newAnalyticsServiceSQLMock(t)
	driverErr := errors.New("connection reset by peer")
	retainedSince := time.Now().Add(-12 * time.Hour)

	mock.ExpectQuery(`SELECT MIN\(em\.created_at\) FROM event_bus_messages`).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(retainedSince))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM event_bus_messages`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery(`SELECT em\.event_type, COUNT\(\*\) AS cnt`).
		WillReturnError(driverErr)

	_, err := svc.queryEventMetrics(context.Background(), sampleEventMetricsFilter())
	require.Error(t, err)
	require.ErrorIs(t, err, driverErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
