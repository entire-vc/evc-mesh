package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The behavioural coverage for the listing queries lives in the real-DB tests
// (integration_test.go, build tag `integration`), where Postgres itself decides
// what the SQL returns. These sqlmock tests pin the two things a real database
// cannot assert for us in the default test run: the exact shape of the query
// (a member predicate is present, and rows are scanned into the domain type)
// and the error path when the driver fails.

func newWorkspaceRepoMock(t *testing.T) (*WorkspaceRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewWorkspaceRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// workspaceRows builds a driver result set matching workspaceRow's db tags.
func workspaceRows(ids ...uuid.UUID) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "name", "slug", "owner_id", "settings",
		"billing_plan_id", "billing_customer_id", "icon_url",
		"created_at", "updated_at", "deleted_at",
	})
	now := time.Now().UTC()
	for i, id := range ids {
		rows.AddRow(id, "WS", "ws-slug", uuid.New(), json.RawMessage(`{}`),
			nil, nil, nil, now.Add(time.Duration(i)*time.Second), now, nil)
	}
	return rows
}

func TestWorkspaceRepo_ListForUser_QueryShapeAndMapping(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)
	userID := uuid.New()
	first, second := uuid.New(), uuid.New()

	// The user id is the sole bind parameter: it must satisfy both the
	// ownership arm and the membership arm of the predicate.
	mock.ExpectQuery("FROM workspaces w").
		WithArgs(userID).
		WillReturnRows(workspaceRows(first, second))

	got, err := repo.ListForUser(context.Background(), userID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, first, got[0].ID)
	assert.Equal(t, second, got[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkspaceRepo_ListForUser_Empty(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)
	userID := uuid.New()

	mock.ExpectQuery("FROM workspaces w").
		WithArgs(userID).
		WillReturnRows(workspaceRows())

	got, err := repo.ListForUser(context.Background(), userID)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkspaceRepo_ListForUser_QueryError(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)

	mock.ExpectQuery("FROM workspaces w").
		WillReturnError(errors.New("connection reset"))

	got, err := repo.ListForUser(context.Background(), uuid.New())
	require.Error(t, err)
	assert.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWorkspaceRepo_ListForUser_SQL locks in the predicate itself. Ownership
// alone is what made a workspace invisible to plain members (the bug this
// method fixes), and membership alone would hide a legacy workspace whose
// owner has no workspace_members row, so both arms must be present — and the
// query must not be a bare JOIN, which would duplicate a row for a user who is
// both owner and member.
func TestWorkspaceRepo_ListForUser_SQL(t *testing.T) {
	// sqlmock surfaces the executed SQL only to the query matcher, so capture
	// it from there.
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewWorkspaceRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(workspaceRows())

	_, err = repo.ListForUser(context.Background(), uuid.New())
	require.NoError(t, err)

	normalized := regexp.MustCompile(`\s+`).ReplaceAllString(captured, " ")
	assert.Contains(t, normalized, "w.owner_id = $1", "ownership arm must be present")
	assert.Contains(t, normalized, "workspace_members", "membership arm must be present")
	assert.Contains(t, normalized, "EXISTS", "membership must be an EXISTS, not a duplicating JOIN")
	assert.Contains(t, normalized, "w.deleted_at IS NULL", "soft-deleted workspaces must be excluded")
	assert.Contains(t, normalized, "ORDER BY w.created_at ASC", "ordering is part of the contract")
}

func TestWorkspaceRepo_ListByOwner_QueryShapeAndMapping(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)
	ownerID := uuid.New()
	wsID := uuid.New()

	mock.ExpectQuery("FROM workspaces WHERE owner_id = ").
		WithArgs(ownerID).
		WillReturnRows(workspaceRows(wsID))

	got, err := repo.ListByOwner(context.Background(), ownerID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, wsID, got[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkspaceRepo_ListByOwner_QueryError(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)

	mock.ExpectQuery("FROM workspaces").
		WillReturnError(errors.New("connection reset"))

	got, err := repo.ListByOwner(context.Background(), uuid.New())
	require.Error(t, err)
	assert.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- Delete: cascade to projects and tasks -----------------------------------

// TestWorkspaceRepo_Delete_CascadesToTasksAndProjects pins the fix for the
// orphaned-rows problem: deleting a workspace with nothing downstream left
// its projects and tasks fully live and fully visible to any query that only
// ever checked their OWN deleted_at. All three UPDATEs must run in the same
// transaction, in the order workspace → tasks → projects, and commit together.
func TestWorkspaceRepo_Delete_CascadesToTasksAndProjects(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE workspaces SET deleted_at = NOW[(][)] WHERE id = [$]1 AND deleted_at IS NULL").
		WithArgs(wsID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE tasks SET deleted_at = NOW[(][)]").
		WithArgs(wsID).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("UPDATE projects SET deleted_at = NOW[(][)] WHERE workspace_id = [$]1 AND deleted_at IS NULL").
		WithArgs(wsID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), wsID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWorkspaceRepo_Delete_NotFoundRollsBackWithoutTouchingChildren: a
// workspace that doesn't exist (or is already deleted) must not cascade —
// the workspace UPDATE affecting 0 rows has to stop the transaction before
// the tasks/projects UPDATEs ever run.
func TestWorkspaceRepo_Delete_NotFoundRollsBackWithoutTouchingChildren(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE workspaces SET deleted_at = NOW[(][)] WHERE id = [$]1 AND deleted_at IS NULL").
		WithArgs(wsID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.Delete(context.Background(), wsID)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "the tasks/projects UPDATEs must not run for a not-found workspace")
}

// TestWorkspaceRepo_Delete_TaskCascadeFailureRollsBackTheWorkspaceToo: if the
// task cascade fails partway through, the workspace itself must not end up
// deleted while its tasks stayed live — the transaction is what prevents that
// half-cascaded state.
func TestWorkspaceRepo_Delete_TaskCascadeFailureRollsBackTheWorkspaceToo(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE workspaces SET deleted_at = NOW[(][)] WHERE id = [$]1 AND deleted_at IS NULL").
		WithArgs(wsID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE tasks SET deleted_at = NOW[(][)]").
		WithArgs(wsID).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	err := repo.Delete(context.Background(), wsID)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWorkspaceRepo_Delete_SQL locks in the cascade's exact shape: the tasks
// UPDATE reaches every task via a project_id subquery scoped to this
// workspace (tasks have no workspace_id column of their own), and the
// projects UPDATE is scoped directly.
func TestWorkspaceRepo_Delete_SQL(t *testing.T) {
	var captured []string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = append(captured, actualSQL)
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewWorkspaceRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectBegin()
	mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Delete(context.Background(), uuid.New()))
	require.Len(t, captured, 3)

	normalize := func(s string) string { return regexp.MustCompile(`\s+`).ReplaceAllString(s, " ") }
	assert.Contains(t, normalize(captured[0]), "UPDATE workspaces SET deleted_at = NOW()")
	assert.Contains(t, normalize(captured[1]), "UPDATE tasks SET deleted_at = NOW()")
	assert.Contains(t, normalize(captured[1]), "project_id IN (SELECT id FROM projects WHERE workspace_id = $1)",
		"tasks have no workspace_id column — must reach it through projects")
	assert.Contains(t, normalize(captured[2]), "UPDATE projects SET deleted_at = NOW() WHERE workspace_id = $1 AND deleted_at IS NULL")
}
