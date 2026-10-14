package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func newInstalledExtensionRepoMock(t *testing.T) (*InstalledExtensionRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewInstalledExtensionRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestInstalledExtensionRepo_UpsertBuiltin_Args(t *testing.T) {
	repo, mock := newInstalledExtensionRepoMock(t)
	mock.ExpectExec(`INSERT INTO installed_extensions`).
		WithArgs("gitlab", "1.4.0", []byte(`{"id":"gitlab"}`), "builtin").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpsertBuiltin(context.Background(), &domain.InstalledExtension{
		ExtensionID: "gitlab", Version: "1.4.0", Manifest: []byte(`{"id":"gitlab"}`), TrustLevel: domain.ExtensionTrustBuiltin,
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInstalledExtensionRepo_GetAndList(t *testing.T) {
	cols := []string{"extension_id", "version", "manifest", "source", "repo_url", "repo_ref", "commit_sha",
		"artifact_sha256", "trust_level", "installed_by", "previous_version", "blocked", "blocked_reason", "created_at", "updated_at"}
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows(cols).AddRow("gitlab", "1.4.0", []byte(`{}`), "builtin", "", "", "", "", "builtin", nil, "", false, "", time.Now(), time.Now())
	}

	repo, mock := newInstalledExtensionRepoMock(t)
	mock.ExpectQuery(`FROM installed_extensions WHERE extension_id`).WithArgs("gitlab").WillReturnRows(row())
	got, err := repo.Get(context.Background(), "gitlab")
	require.NoError(t, err)
	require.Equal(t, "1.4.0", got.Version)

	mock.ExpectQuery(`FROM installed_extensions WHERE extension_id`).WithArgs("nope").WillReturnError(sql.ErrNoRows)
	missing, err := repo.Get(context.Background(), "nope")
	require.NoError(t, err)
	require.Nil(t, missing)

	mock.ExpectQuery(`FROM installed_extensions WHERE extension_id`).WithArgs("x").WillReturnError(sql.ErrConnDone)
	_, err = repo.Get(context.Background(), "x")
	require.Error(t, err)

	mock.ExpectQuery(`FROM installed_extensions ORDER BY`).WillReturnRows(row())
	list, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
}
