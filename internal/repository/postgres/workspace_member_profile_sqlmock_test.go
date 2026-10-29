package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The real-database round trip is in internal/handler/team_member_profile_db_test.go;
// these pin the column plumbing of each query without a database.

func newMemberProfileRepo(t *testing.T) (*WorkspaceMemberRepo, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return NewWorkspaceMemberRepo(sqlx.NewDb(raw, "postgres")), mock
}

var memberRowColumns = []string{
	"id", "workspace_id", "user_id", "role", "invited_by", "created_at", "updated_at",
	"capabilities", "responsibility_zone", "availability",
	"u_id", "u_email", "u_display_name", "u_username", "u_avatar_url",
}

func TestWorkspaceMemberRepo_Create_ReturnsProfileDefaults(t *testing.T) {
	repo, mock := newMemberProfileRepo(t)
	m := &domain.WorkspaceMember{ID: uuid.New(), WorkspaceID: uuid.New(), UserID: uuid.New(), Role: domain.RoleMember}
	mock.ExpectQuery(regexp.QuoteMeta("RETURNING capabilities, responsibility_zone, availability")).
		WillReturnRows(sqlmock.NewRows([]string{"capabilities", "responsibility_zone", "availability"}).
			AddRow([]byte(`[]`), "", "business-hours"))

	require.NoError(t, repo.Create(context.Background(), m))
	assert.JSONEq(t, `[]`, string(m.Capabilities))
	assert.Equal(t, "business-hours", m.Availability)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkspaceMemberRepo_GetByWorkspaceAndUser_ReadsProfile(t *testing.T) {
	repo, mock := newMemberProfileRepo(t)
	wsID, userID := uuid.New(), uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("capabilities, responsibility_zone, availability")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "workspace_id", "user_id", "role", "invited_by",
			"capabilities", "responsibility_zone", "availability", "created_at", "updated_at"}).
			AddRow(uuid.New(), wsID, userID, "member", nil, []byte(`["go"]`), "X", "async", time.Now(), time.Now()))

	got, err := repo.GetByWorkspaceAndUser(context.Background(), wsID, userID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "X", got.ResponsibilityZone)
	assert.Equal(t, "async", got.Availability)
	assert.JSONEq(t, `["go"]`, string(got.Capabilities))
}

func TestWorkspaceMemberRepo_ListQueries_ReadProfile(t *testing.T) {
	ctx := context.Background()
	wsID, userID := uuid.New(), uuid.New()
	row := []driver.Value{uuid.New(), wsID, userID, "member", nil, time.Now(), time.Now(),
		[]byte(`["go"]`), "X", "async", userID, "a@example.test", "Ann", "ann", ""}

	t.Run("List", func(t *testing.T) {
		repo, mock := newMemberProfileRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta("wm.capabilities, wm.responsibility_zone, wm.availability")).
			WillReturnRows(sqlmock.NewRows(memberRowColumns).AddRow(row...))
		got, err := repo.List(ctx, wsID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "X", got[0].ResponsibilityZone)
		assert.Equal(t, "async", got[0].Availability)
		assert.JSONEq(t, `["go"]`, string(got[0].Capabilities))
	})

	t.Run("ListWithProjects", func(t *testing.T) {
		repo, mock := newMemberProfileRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta("wm.capabilities, wm.responsibility_zone, wm.availability")).
			WillReturnRows(sqlmock.NewRows(append(append([]string{}, memberRowColumns...), "project_names")).
				AddRow(append(append([]driver.Value{}, row...), []byte(`["P"]`))...))
		got, err := repo.ListWithProjects(ctx, wsID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "X", got[0].ResponsibilityZone)
		assert.Equal(t, "async", got[0].Availability)
		assert.Equal(t, "ann", got[0].User.Username)
		assert.Equal(t, []string{"P"}, got[0].Projects)
	})
}

func TestWorkspaceMemberRepo_UpdateProfile(t *testing.T) {
	ctx := context.Background()
	wsID, userID := uuid.New(), uuid.New()
	zone := "X"
	update := regexp.QuoteMeta("UPDATE workspace_members SET")

	t.Run("nil fields go as NULL so COALESCE keeps them", func(t *testing.T) {
		repo, mock := newMemberProfileRepo(t)
		mock.ExpectExec(update).
			WithArgs(wsID, userID, `["go"]`, &zone, nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		found, err := repo.UpdateProfile(ctx, wsID, userID, domain.MemberProfileUpdate{
			Capabilities: []byte(`["go"]`), ResponsibilityZone: &zone,
		})
		require.NoError(t, err)
		assert.True(t, found)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no row means not a member", func(t *testing.T) {
		repo, mock := newMemberProfileRepo(t)
		mock.ExpectExec(update).WillReturnResult(sqlmock.NewResult(0, 0))
		found, err := repo.UpdateProfile(ctx, wsID, userID, domain.MemberProfileUpdate{ResponsibilityZone: &zone})
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("exec error", func(t *testing.T) {
		repo, mock := newMemberProfileRepo(t)
		mock.ExpectExec(update).WillReturnError(errors.New("db down"))
		_, err := repo.UpdateProfile(ctx, wsID, userID, domain.MemberProfileUpdate{ResponsibilityZone: &zone})
		require.ErrorContains(t, err, "db down")
	})

	t.Run("rows affected error", func(t *testing.T) {
		repo, mock := newMemberProfileRepo(t)
		mock.ExpectExec(update).WillReturnResult(sqlmock.NewErrorResult(errors.New("no count")))
		_, err := repo.UpdateProfile(ctx, wsID, userID, domain.MemberProfileUpdate{ResponsibilityZone: &zone})
		require.ErrorContains(t, err, "no count")
	})
}
