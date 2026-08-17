package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Both methods below are simple SELECTs with no branches worth a live-DB
// integration test on their own — sqlmock is enough to prove the query shape
// and the row-to-domain mapping.

func TestIntegrationRepo_ListActiveByProvider(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := NewIntegrationRepo(sqlx.NewDb(rawDB, "postgres"))

	wsID, intID := uuid.New(), uuid.New()
	now := time.Now()
	cfg, _ := json.Marshal(map[string]string{"bot_token": "enc", "bot_username": "mesh_bot"})

	mock.ExpectQuery("SELECT \\* FROM integration_configs WHERE provider = \\$1 AND is_active = true").
		WithArgs("telegram").
		WillReturnRows(sqlmock.NewRows([]string{"id", "workspace_id", "provider", "config", "is_active", "created_at", "updated_at"}).
			AddRow(intID, wsID, "telegram", cfg, true, now, now))

	result, err := repo.ListActiveByProvider(context.Background(), domain.IntegrationProviderTelegram)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, intID, result[0].ID)
	assert.Equal(t, wsID, result[0].WorkspaceID)
	assert.True(t, result[0].IsActive)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationRepo_ListActiveByProvider_Empty(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := NewIntegrationRepo(sqlx.NewDb(rawDB, "postgres"))

	mock.ExpectQuery("SELECT \\* FROM integration_configs WHERE provider = \\$1 AND is_active = true").
		WithArgs("telegram").
		WillReturnRows(sqlmock.NewRows([]string{"id", "workspace_id", "provider", "config", "is_active", "created_at", "updated_at"}))

	result, err := repo.ListActiveByProvider(context.Background(), domain.IntegrationProviderTelegram)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectRepo_ListForUserInWorkspace(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := NewProjectRepo(sqlx.NewDb(rawDB, "postgres"))

	wsID, userID, projID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT p\\.\\* FROM projects p").
		WithArgs(wsID, userID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "workspace_id", "name", "description", "slug", "icon", "settings",
			"default_assignee_type", "is_archived", "created_at", "updated_at", "deleted_at",
		}).AddRow(projID, wsID, "Website", "", "website", "", []byte("{}"), "unassigned", false, now, now, nil))

	result, err := repo.ListForUserInWorkspace(context.Background(), wsID, userID)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "Website", result[0].Name)
	assert.Equal(t, wsID, result[0].WorkspaceID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectRepo_ListForUserInWorkspace_NoProjects(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	repo := NewProjectRepo(sqlx.NewDb(rawDB, "postgres"))

	wsID, userID := uuid.New(), uuid.New()

	mock.ExpectQuery("SELECT p\\.\\* FROM projects p").
		WithArgs(wsID, userID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "workspace_id", "name", "description", "slug", "icon", "settings",
			"default_assignee_type", "is_archived", "created_at", "updated_at", "deleted_at",
		}))

	result, err := repo.ListForUserInWorkspace(context.Background(), wsID, userID)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.NoError(t, mock.ExpectationsWereMet())
}
