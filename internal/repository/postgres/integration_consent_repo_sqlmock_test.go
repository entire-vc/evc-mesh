package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func newConsentRepoMock(t *testing.T) (*IntegrationConsentRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewIntegrationConsentRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func TestGetConsent_Recorded(t *testing.T) {
	repo, mock := newConsentRepoMock(t)
	ws, by := uuid.New(), uuid.New()
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT consented_capabilities, consented_at, consented_by, consented_version, consented_commit\s+FROM integration_configs`).
		WithArgs(ws, "team-relay").
		WillReturnRows(sqlmock.NewRows([]string{"consented_capabilities", "consented_at", "consented_by", "consented_version", "consented_commit"}).
			AddRow([]byte(`["api:tasks.read","events:task.created"]`), at, by, "0.4.0", "9c07"))

	c, err := repo.GetConsent(context.Background(), ws, "team-relay")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, []string{"api:tasks.read", "events:task.created"}, c.Capabilities)
	assert.Equal(t, at, c.At)
	require.NotNil(t, c.By)
	assert.Equal(t, by, *c.By)
	assert.Equal(t, "0.4.0", c.Version)
	assert.Equal(t, "9c07", c.Commit)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetConsent_NoConnectionOrNoConsent(t *testing.T) {
	repo, mock := newConsentRepoMock(t)
	ws := uuid.New()
	mock.ExpectQuery(`FROM integration_configs`).WithArgs(ws, "a").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`FROM integration_configs`).WithArgs(ws, "b").
		WillReturnRows(sqlmock.NewRows([]string{"consented_capabilities", "consented_at", "consented_by"}).
			AddRow(nil, nil, nil))

	c, err := repo.GetConsent(context.Background(), ws, "a")
	require.NoError(t, err)
	assert.Nil(t, c, "no connection")
	c, err = repo.GetConsent(context.Background(), ws, "b")
	require.NoError(t, err)
	assert.Nil(t, c, "connection made before consent was recorded")
}

func TestGetConsent_BadJSON(t *testing.T) {
	repo, mock := newConsentRepoMock(t)
	ws := uuid.New()
	mock.ExpectQuery(`FROM integration_configs`).WithArgs(ws, "a").
		WillReturnRows(sqlmock.NewRows([]string{"consented_capabilities", "consented_at", "consented_by"}).
			AddRow([]byte(`{"not":"a list"}`), nil, nil))
	_, err := repo.GetConsent(context.Background(), ws, "a")
	assert.Error(t, err)
}

func TestSetConsent(t *testing.T) {
	repo, mock := newConsentRepoMock(t)
	conn, by := uuid.New(), uuid.New()
	at := time.Now()
	mock.ExpectExec(`UPDATE integration_configs\s+SET consented_capabilities`).
		WithArgs(conn, []byte(`["api:tasks.read"]`), at, &by, "0.4.0", "9c07").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.SetConsent(context.Background(), conn,
		domain.ExtensionConsent{Capabilities: []string{"api:tasks.read"}, At: at, By: &by, Version: "0.4.0", Commit: "9c07"}))

	mock.ExpectExec(`UPDATE integration_configs`).WithArgs(conn, []byte(`[]`), sqlmock.AnyArg(), nil, "", "").
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.SetConsent(context.Background(), conn, domain.ExtensionConsent{})
	assert.Error(t, err, "no such connection")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReconsent_Paths(t *testing.T) {
	ctx := context.Background()
	conn := uuid.New()
	ok := func(tg domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		assert.Nil(t, tg.Installed, "no installed row")
		return domain.ExtensionConsent{Capabilities: []string{"api:tasks:read"}, Version: "0.4.0"}, nil, nil
	}
	lockConn := `SELECT true FROM integration_configs WHERE id = \$1 FOR UPDATE`
	lockInst := `FROM installed_extensions WHERE extension_id = \$1 FOR SHARE`
	locked := func(mock sqlmock.Sqlmock) {
		mock.ExpectBegin()
		mock.ExpectQuery(lockConn).WithArgs(conn).WillReturnRows(sqlmock.NewRows([]string{"bool"}).AddRow(true))
		mock.ExpectQuery(lockInst).WithArgs("hello").WillReturnError(sql.ErrNoRows)
	}
	boom := errors.New("boom")

	t.Run("begin fails", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		mock.ExpectBegin().WillReturnError(boom)
		_, err := repo.Reconsent(ctx, conn, "hello", ok)
		assert.ErrorIs(t, err, boom)
	})
	t.Run("no connection", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(lockConn).WithArgs(conn).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		_, err := repo.Reconsent(ctx, conn, "hello", ok)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusNotFound, apiErr.StatusCode())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("connection lock fails", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(lockConn).WithArgs(conn).WillReturnError(boom)
		mock.ExpectRollback()
		_, err := repo.Reconsent(ctx, conn, "hello", ok)
		assert.ErrorIs(t, err, boom)
	})
	t.Run("installed lock fails", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(lockConn).WithArgs(conn).WillReturnRows(sqlmock.NewRows([]string{"bool"}).AddRow(true))
		mock.ExpectQuery(lockInst).WithArgs("hello").WillReturnError(boom)
		mock.ExpectRollback()
		_, err := repo.Reconsent(ctx, conn, "hello", ok)
		assert.ErrorIs(t, err, boom)
	})
	t.Run("resolve refuses: nothing is written", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		locked(mock)
		mock.ExpectRollback()
		_, err := repo.Reconsent(ctx, conn, "hello", func(domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
			return domain.ExtensionConsent{}, nil, boom
		})
		assert.ErrorIs(t, err, boom)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("consent write fails", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		locked(mock)
		mock.ExpectExec(`UPDATE integration_configs`).WillReturnError(boom)
		mock.ExpectRollback()
		_, err := repo.Reconsent(ctx, conn, "hello", ok)
		assert.ErrorIs(t, err, boom)
	})
	t.Run("actor write fails", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		locked(mock)
		mock.ExpectExec(`UPDATE integration_configs`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE extension_actors SET capabilities`).WithArgs(conn, []byte(`{"api":["tasks:read"]}`)).WillReturnError(boom)
		mock.ExpectRollback()
		_, err := repo.Reconsent(ctx, conn, "hello", func(tg domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
			c, _, _ := ok(tg)
			return c, []byte(`{"api":["tasks:read"]}`), nil
		})
		assert.ErrorIs(t, err, boom)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("commit fails", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		locked(mock)
		mock.ExpectExec(`UPDATE integration_configs`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(boom)
		_, err := repo.Reconsent(ctx, conn, "hello", ok)
		assert.ErrorIs(t, err, boom)
	})
	t.Run("no actor snapshot: the actor row is not touched", func(t *testing.T) {
		repo, mock := newConsentRepoMock(t)
		locked(mock)
		mock.ExpectExec(`UPDATE integration_configs`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		c, err := repo.Reconsent(ctx, conn, "hello", ok)
		require.NoError(t, err)
		assert.Equal(t, "0.4.0", c.Version)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestConsent_DBErrors(t *testing.T) {
	repo, mock := newConsentRepoMock(t)
	ws, conn := uuid.New(), uuid.New()
	boom := errors.New("boom")
	mock.ExpectQuery(`FROM integration_configs`).WithArgs(ws, "a").WillReturnError(boom)
	_, err := repo.GetConsent(context.Background(), ws, "a")
	assert.ErrorIs(t, err, boom)
	mock.ExpectExec(`UPDATE integration_configs`).WillReturnError(boom)
	assert.ErrorIs(t, repo.SetConsent(context.Background(), conn, domain.ExtensionConsent{}), boom)
	require.NoError(t, mock.ExpectationsWereMet())
}
