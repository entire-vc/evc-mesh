package postgres

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The versioned write is a transaction with several failure points, and each one
// has to leave the database untouched rather than half-written: a bumped version
// with no snapshot would claim a history we cannot produce, and a snapshot with
// no bump would let the next conditional write succeed against content that had
// already moved.
//
// A live Postgres cannot be made to fail those statements on demand, so these
// use sqlmock — the failure is the thing under test, not the SQL.

func newMemoryRepoMock(t *testing.T) (*MemoryRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewMemoryRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func sampleMemory() *domain.Memory {
	return &domain.Memory{
		ID: uuid.New(), WorkspaceID: uuid.New(), Key: "k", Content: "c",
		Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
	}
}

func TestUpsert_RollsBackWhenTheRevisionCannotBeWritten(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	m := sampleMemory()
	boom := errors.New("revision insert exploded")

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version FROM memories WHERE id = ").
		WithArgs(m.ID).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(4))
	mock.ExpectExec("INSERT INTO memories").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO memory_revisions").WillReturnError(boom)
	// The decisive expectation: a failed snapshot must roll the version bump
	// back, not commit it.
	mock.ExpectRollback()

	err := repo.Upsert(context.Background(), m, domain.MemoryWriteIntent{Reason: "why"})
	require.ErrorIs(t, err, boom)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpsert_RollsBackWhenTheRowWriteFails(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	m := sampleMemory()
	boom := errors.New("row write exploded")

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version FROM memories WHERE id = ").
		WithArgs(m.ID).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(1))
	mock.ExpectExec("INSERT INTO memories").WillReturnError(boom)
	mock.ExpectRollback()

	err := repo.Upsert(context.Background(), m, domain.MemoryWriteIntent{})
	require.ErrorIs(t, err, boom)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpsert_ConflictRollsBackAndWritesNothing(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	m := sampleMemory()
	expected := 2

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version FROM memories WHERE id = ").
		WithArgs(m.ID).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(9))
	// No ExpectExec at all: a refused write must not touch either table. If the
	// implementation wrote anyway, sqlmock fails on the unexpected statement —
	// which is a stronger assertion than checking the returned error, because a
	// conflict error is also what an implementation that reported AND wrote
	// would return.
	mock.ExpectRollback()

	err := repo.Upsert(context.Background(), m, domain.MemoryWriteIntent{ExpectedVersion: &expected})
	var conflict *domain.MemoryVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 2, conflict.Expected)
	assert.Equal(t, 9, conflict.Actual)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A conditional write against a key that does not exist is a conflict too: the
// caller believes it is editing something. Creating it instead would silently
// turn an edit into a create, which is the same class of surprise as the
// overwrite this feature exists to stop.
func TestUpsert_ConditionalWriteOnMissingRowIsAConflict(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	m := sampleMemory()
	expected := 3

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version FROM memories WHERE id = ").
		WithArgs(m.ID).
		WillReturnRows(sqlmock.NewRows([]string{"version"})) // no rows
	mock.ExpectRollback()

	err := repo.Upsert(context.Background(), m, domain.MemoryWriteIntent{ExpectedVersion: &expected})
	var conflict *domain.MemoryVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 0, conflict.Actual, "a missing row reports actual version 0")
	require.NoError(t, mock.ExpectationsWereMet())
}

// A SELECT that fails for a reason other than "no rows" must abort. Treating an
// unreadable version as "not found" would silently downgrade a conditional
// write into a create.
func TestUpsert_UnreadableVersionAborts(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	m := sampleMemory()
	boom := errors.New("connection reset")

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version FROM memories WHERE id = ").
		WithArgs(m.ID).WillReturnError(boom)
	mock.ExpectRollback()

	err := repo.Upsert(context.Background(), m, domain.MemoryWriteIntent{})
	require.ErrorIs(t, err, boom)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpsert_BeginFailureIsReported(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	boom := errors.New("cannot begin")
	mock.ExpectBegin().WillReturnError(boom)

	err := repo.Upsert(context.Background(), sampleMemory(), domain.MemoryWriteIntent{})
	require.ErrorIs(t, err, boom)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppendRevision_ErrorIsReported(t *testing.T) {
	repo, mock := newMemoryRepoMock(t)
	boom := errors.New("append failed")
	mock.ExpectExec("INSERT INTO memory_revisions").WillReturnError(boom)

	err := repo.AppendRevision(context.Background(), domain.MemoryRevision{
		MemoryID: uuid.New(), Version: 2, Content: "x", Action: domain.MemoryActionForgotten,
	})
	require.ErrorIs(t, err, boom)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRevisions_DefaultsLimitAndReportsErrors(t *testing.T) {
	t.Run("a non-positive limit falls back to 50", func(t *testing.T) {
		repo, mock := newMemoryRepoMock(t)
		id := uuid.New()
		mock.ExpectQuery("FROM memory_revisions").
			WithArgs(id, 50).
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "memory_id", "version", "content", "tags", "action", "reason", "actor_agent_id", "created_at",
			}))
		_, err := repo.ListRevisions(context.Background(), id, 0)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("a query error is returned, not swallowed as empty history", func(t *testing.T) {
		repo, mock := newMemoryRepoMock(t)
		boom := errors.New("select failed")
		mock.ExpectQuery("FROM memory_revisions").WillReturnError(boom)
		// Returning (nil, nil) here would render as "this memory has no
		// history", which is indistinguishable from the truthful empty case.
		revs, err := repo.ListRevisions(context.Background(), uuid.New(), 10)
		require.ErrorIs(t, err, boom)
		assert.Nil(t, revs)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
