package postgres

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWorkspaceMemberRepoMock(t *testing.T) (*WorkspaceMemberRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewWorkspaceMemberRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// TestWorkspaceMemberRepo_GetRole_Found is the ordinary case: a live
// workspace, a real membership row.
func TestWorkspaceMemberRepo_GetRole_Found(t *testing.T) {
	repo, mock := newWorkspaceMemberRepoMock(t)
	wsID, userID := uuid.New(), uuid.New()

	mock.ExpectQuery("SELECT wm[.]role FROM workspace_members wm").
		WithArgs(wsID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))

	role, err := repo.GetRole(context.Background(), wsID, userID)
	require.NoError(t, err)
	assert.Equal(t, "admin", role)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWorkspaceMemberRepo_GetRole_DeletedWorkspaceIsNotAMembership is the
// fix this file exists for: this is the single role lookup RequirePermission
// (rbac()), MemoryHandler.workspaceAllowed, and every other caller share —
// once the workspace is soft-deleted, none of them may keep treating an
// existing workspace_members row as a live role.
func TestWorkspaceMemberRepo_GetRole_DeletedWorkspaceIsNotAMembership(t *testing.T) {
	repo, mock := newWorkspaceMemberRepoMock(t)
	wsID, userID := uuid.New(), uuid.New()

	// The join's own WHERE w.deleted_at IS NULL is what excludes the row —
	// modelled here as the query simply returning nothing, the same as
	// sqlmock has for any WHERE clause it doesn't evaluate itself.
	mock.ExpectQuery("SELECT wm[.]role FROM workspace_members wm").
		WithArgs(wsID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}))

	_, err := repo.GetRole(context.Background(), wsID, userID)
	require.Error(t, err, "a role must not resolve for a deleted workspace")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWorkspaceMemberRepo_GetRole_SQL locks in that the join actually exists
// in the query, not just that a test can arrange for zero rows to come back.
func TestWorkspaceMemberRepo_GetRole_SQL(t *testing.T) {
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewWorkspaceMemberRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("owner"))

	_, err = repo.GetRole(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)

	normalized := regexp.MustCompile(`\s+`).ReplaceAllString(captured, " ")
	assert.Contains(t, normalized, "JOIN workspaces w ON w.id = wm.workspace_id")
	assert.Contains(t, normalized, "w.deleted_at IS NULL")
}
