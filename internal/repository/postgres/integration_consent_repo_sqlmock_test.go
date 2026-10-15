package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
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
	mock.ExpectQuery(`SELECT consented_capabilities, consented_at, consented_by\s+FROM integration_configs`).
		WithArgs(ws, "team-relay").
		WillReturnRows(sqlmock.NewRows([]string{"consented_capabilities", "consented_at", "consented_by"}).
			AddRow([]byte(`["api:tasks.read","events:task.created"]`), at, by))

	c, err := repo.GetConsent(context.Background(), ws, "team-relay")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, []string{"api:tasks.read", "events:task.created"}, c.Capabilities)
	assert.Equal(t, at, c.At)
	require.NotNil(t, c.By)
	assert.Equal(t, by, *c.By)
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
		WithArgs(conn, []byte(`["api:tasks.read"]`), at, &by).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.SetConsent(context.Background(), conn,
		domain.ExtensionConsent{Capabilities: []string{"api:tasks.read"}, At: at, By: &by}))

	mock.ExpectExec(`UPDATE integration_configs`).WithArgs(conn, []byte(`[]`), sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.SetConsent(context.Background(), conn, domain.ExtensionConsent{})
	assert.Error(t, err, "no such connection")
	require.NoError(t, mock.ExpectationsWereMet())
}
