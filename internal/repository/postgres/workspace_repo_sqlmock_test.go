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
	mock.ExpectQuery("SELECT w[.]\\* FROM workspaces w").
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

	mock.ExpectQuery("SELECT w[.]\\* FROM workspaces w").
		WithArgs(userID).
		WillReturnRows(workspaceRows())

	got, err := repo.ListForUser(context.Background(), userID)
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkspaceRepo_ListForUser_QueryError(t *testing.T) {
	repo, mock := newWorkspaceRepoMock(t)

	mock.ExpectQuery("SELECT w[.]\\* FROM workspaces w").
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

	mock.ExpectQuery("SELECT [*] FROM workspaces WHERE owner_id = ").
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

	mock.ExpectQuery("SELECT [*] FROM workspaces").
		WillReturnError(errors.New("connection reset"))

	got, err := repo.ListByOwner(context.Background(), uuid.New())
	require.Error(t, err)
	assert.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
