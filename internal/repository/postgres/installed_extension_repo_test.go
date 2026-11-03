package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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

var installedCols = []string{"extension_id", "version", "manifest", "source", "repo_url", "repo_ref", "commit_sha",
	"artifact_sha256", "trust_level", "installed_by", "previous_version", "blocked", "blocked_reason", "created_at", "updated_at"}

func catalogRow(version, commit string) *sqlmock.Rows {
	return sqlmock.NewRows(installedCols).AddRow("hello", version, []byte(`{"id":"hello"}`), "catalog", "", "", commit, "dg-"+version, "verified", nil, "", false, "", time.Now(), time.Now())
}

func newCatalogTarget() *domain.InstalledExtension {
	return &domain.InstalledExtension{ExtensionID: "hello", Version: "2", Manifest: []byte(`{"id":"hello"}`), CommitSHA: "c2", ArtifactSHA256: "dg-2", TrustLevel: "verified"}
}

func TestInstalledExtensionRepo_UpdateCatalog_SealsSnapshotInOneTx(t *testing.T) {
	repo, mock := newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM installed_extensions WHERE extension_id = \$1 AND source = 'catalog' FOR UPDATE`).WithArgs("hello").WillReturnRows(catalogRow("1", "c1"))
	mock.ExpectExec(`UPDATE installed_extensions SET\s+previous_version = version, previous_snapshot = \$7, skipped_commit_sha = ''`).
		WithArgs("hello", "2", []byte(`{"id":"hello"}`), "c2", "dg-2", "verified", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.UpdateCatalog(context.Background(), newCatalogTarget(), domain.CatalogUpdateGuard{FromVersion: "1", FromCommitSHA: "c1"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInstalledExtensionRepo_UpdateCatalog_Refusals(t *testing.T) {
	ctx := context.Background()
	notFound := func(t *testing.T) (*InstalledExtensionRepo, sqlmock.Sqlmock) {
		repo, mock := newInstalledExtensionRepoMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`FOR UPDATE`).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		return repo, mock
	}
	repo, mock := notFound(t)
	require.Error(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{}))
	require.NoError(t, mock.ExpectationsWereMet())

	// the row moved on since the updater read it: nothing is written
	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(catalogRow("1", "c1"))
	mock.ExpectRollback()
	require.Error(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{FromVersion: "1", FromCommitSHA: "other"}))
	require.NoError(t, mock.ExpectationsWereMet())

	// the unattended pass: the target is the commit an admin rolled back from
	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(catalogRow("1", "c1"))
	mock.ExpectQuery(`SELECT skipped_commit_sha`).WithArgs("hello").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow("c2"))
	mock.ExpectRollback()
	require.Error(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{FromVersion: "1", FromCommitSHA: "c1", Auto: true}))
	require.NoError(t, mock.ExpectationsWereMet())

	// the unattended pass with nothing skipped goes through; reading it can fail
	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(catalogRow("1", "c1"))
	mock.ExpectQuery(`SELECT skipped_commit_sha`).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	require.ErrorIs(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{FromVersion: "1", FromCommitSHA: "c1", Auto: true}), sql.ErrConnDone)

	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	require.ErrorIs(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{}), sql.ErrConnDone)

	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	require.ErrorIs(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{}), sql.ErrConnDone)

	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(catalogRow("1", "c1"))
	mock.ExpectExec(`UPDATE installed_extensions`).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	require.ErrorIs(t, repo.UpdateCatalog(ctx, newCatalogTarget(), domain.CatalogUpdateGuard{FromVersion: "1", FromCommitSHA: "c1"}), sql.ErrConnDone)
}

func TestInstalledExtensionRepo_GetCatalogSnapshotAndSkipped(t *testing.T) {
	ctx := context.Background()
	repo, mock := newInstalledExtensionRepoMock(t)
	col := []string{"previous_snapshot"}
	mock.ExpectQuery(`SELECT previous_snapshot`).WithArgs("hello").WillReturnRows(sqlmock.NewRows(col).AddRow([]byte(`{"version":"1","digest":"d"}`)))
	s, err := repo.GetCatalogSnapshot(ctx, "hello")
	require.NoError(t, err)
	require.Equal(t, "1", s.Version)

	mock.ExpectQuery(`SELECT previous_snapshot`).WillReturnRows(sqlmock.NewRows(col).AddRow(nil))
	s, err = repo.GetCatalogSnapshot(ctx, "hello")
	require.NoError(t, err)
	require.Nil(t, s, "no snapshot: legacy or already rolled back")

	mock.ExpectQuery(`SELECT previous_snapshot`).WillReturnError(sql.ErrNoRows)
	_, err = repo.GetCatalogSnapshot(ctx, "ghost")
	require.Error(t, err)

	mock.ExpectQuery(`SELECT previous_snapshot`).WillReturnError(sql.ErrConnDone)
	_, err = repo.GetCatalogSnapshot(ctx, "hello")
	require.ErrorIs(t, err, sql.ErrConnDone)

	mock.ExpectQuery(`SELECT previous_snapshot`).WillReturnRows(sqlmock.NewRows(col).AddRow([]byte(`"a string"`)))
	_, err = repo.GetCatalogSnapshot(ctx, "hello")
	require.Error(t, err, "unreadable snapshot")

	mock.ExpectQuery(`SELECT skipped_commit_sha`).WithArgs("hello").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow("c2"))
	sha, err := repo.SkippedCommit(ctx, "hello")
	require.NoError(t, err)
	require.Equal(t, "c2", sha)
	mock.ExpectQuery(`SELECT skipped_commit_sha`).WillReturnError(sql.ErrNoRows)
	sha, err = repo.SkippedCommit(ctx, "ghost")
	require.NoError(t, err)
	require.Empty(t, sha)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInstalledExtensionRepo_RollbackCatalog(t *testing.T) {
	ctx := context.Background()
	snap := domain.CatalogSnapshot{Version: "1", Manifest: []byte(`{"id":"hello"}`), CommitSHA: "c1", ArtifactSHA256: "dg-1", TrustLevel: "verified"}
	guard := domain.CatalogRollbackGuard{Version: "2", CommitSHA: "c2", SnapshotDigest: "d"}
	journal := &domain.InstanceExtensionJournalEntry{ExtensionID: "hello", Action: domain.InstanceJournalRolledBack}
	upd := `UPDATE installed_extensions SET\s+skipped_commit_sha = commit_sha,`
	args := []driver.Value{"hello", "2", "c2", "d", "1", []byte(`{"id":"hello"}`), "c1", "dg-1", "verified"}

	repo, mock := newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(upd).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO instance_extension_journal`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.RollbackCatalog(ctx, "hello", guard, snap, journal))
	require.NoError(t, mock.ExpectationsWereMet())

	// the guard did not match: 409, no receipt, rolled back
	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(upd).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	var ae *apierror.Error
	require.ErrorAs(t, repo.RollbackCatalog(ctx, "hello", guard, snap, journal), &ae)
	require.Equal(t, 409, ae.Code)
	require.NoError(t, mock.ExpectationsWereMet())

	// a failed receipt rolls the rollback back
	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(upd).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO instance_extension_journal`).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	require.ErrorIs(t, repo.RollbackCatalog(ctx, "hello", guard, snap, journal), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())

	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	require.ErrorIs(t, repo.RollbackCatalog(ctx, "hello", guard, snap, journal), sql.ErrConnDone)

	repo, mock = newInstalledExtensionRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(upd).WithArgs(args...).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	require.ErrorIs(t, repo.RollbackCatalog(ctx, "hello", guard, snap, journal), sql.ErrConnDone)
}
