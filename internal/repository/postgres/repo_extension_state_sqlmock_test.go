package postgres

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A repository install that was never updated, was rolled back or was moved to
// the catalog has NULL previous_manifest and pending_manifest. GetState must
// still read the row: its callers drop the error, so a failed read used to
// lose the skipped and pending commits silently.
func TestRepoExtensionRepo_GetState_NullManifests(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := NewRepoExtensionRepo(sqlx.NewDb(rawDB, "postgres"))

	cols := []string{"previous_manifest", "previous_commit_sha", "previous_ref", "pending_version", "pending_ref",
		"pending_commit_sha", "pending_manifest", "skipped_commit_sha"}
	mock.ExpectQuery("SELECT previous_manifest").WithArgs("hello").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(nil, "", "", "", "", "", nil, "bad1"))
	st, err := repo.GetState(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionRepoState{SkippedCommitSHA: "bad1"}, *st)

	mock.ExpectQuery("SELECT previous_manifest").WithArgs("hello").
		WillReturnRows(sqlmock.NewRows(cols).AddRow([]byte(`{"id":"hello"}`), "aaa", "main", "0.2.0", "main", "bbb", []byte(`{"v":2}`), ""))
	st, err = repo.GetState(context.Background(), "hello")
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"hello"}`, string(st.PreviousManifest))
	assert.JSONEq(t, `{"v":2}`, string(st.PendingManifest))
	assert.Equal(t, "bbb", st.PendingCommitSHA)

	mock.ExpectQuery("SELECT previous_manifest").WithArgs("missing").WillReturnRows(sqlmock.NewRows(cols))
	st, err = repo.GetState(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, st)
	require.NoError(t, mock.ExpectationsWereMet())
}
