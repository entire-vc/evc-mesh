package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Always-run counterpart to TestTaskRepo_ListTriageQueue (build tag integration):
// the coverage gate runs a plain `go test`, so these pin the union predicate and
// the arg wiring without a live Postgres.

func TestTaskRepo_ListTriageQueue_UnionPredicateAndOrder(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	workspaceID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("t.human_gate = true AND ts.category NOT IN ($3, $4)")).
		WithArgs(workspaceID, domain.StatusCategoryTriage, domain.StatusCategoryDone, domain.StatusCategoryCancelled).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))
	mock.ExpectQuery(regexp.QuoteMeta("ORDER BY (t.human_gate AND t.human_gate_class = 'hard') DESC")).
		WithArgs(workspaceID, domain.StatusCategoryTriage, domain.StatusCategoryDone, domain.StatusCategoryCancelled, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	page, err := repo.ListTriageQueue(context.Background(), workspaceID, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 7, page.TotalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_ListTriageQueue_CountError(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(t.id)")).WillReturnError(errors.New("boom"))

	_, err := repo.ListTriageQueue(context.Background(), uuid.New(), pagination.Params{})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTaskRepo_ListTriageQueue_DataError(t *testing.T) {
	repo, mock := newTaskRepoMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(t.id)")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("ORDER BY")).WillReturnError(errors.New("boom"))

	_, err := repo.ListTriageQueue(context.Background(), uuid.New(), pagination.Params{})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
