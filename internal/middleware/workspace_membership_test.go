package middleware

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These three helpers are the membership rule in a form callers outside the Echo
// middleware chain can use — the WebSocket handshake in particular. They are
// tested here rather than only through the middleware because a divergence
// between the socket's answer and the API's answer is a cross-tenant hole, and
// the socket is the caller that cannot reuse the middleware.

func TestUserIsWorkspaceMember(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	wsID := uuid.New()
	member := uuid.New()
	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("viewer"))
	assert.True(t, UserIsWorkspaceMember(context.Background(), sqlxDB, wsID, member))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUserIsWorkspaceMember_OwnerWithoutMemberRow covers the case that exists in
// production: the owner membership row is written best-effort after the workspace
// itself, so a partial failure leaves an owner who is not one of its members.
// Without the fallback, this fix would lock those owners out of their own live
// event feed.
func TestUserIsWorkspaceMember_OwnerWithoutMemberRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	wsID := uuid.New()
	owner := uuid.New()
	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(wsID, owner).
		WillReturnError(errors.New("no rows"))
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(owner))

	assert.True(t, UserIsWorkspaceMember(context.Background(), sqlxDB, wsID, owner))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUserIsWorkspaceMember_EmptyRoleIsNotMembership: a blank role must not read
// as membership just because the query succeeded.
func TestUserIsWorkspaceMember_EmptyRoleIsNotMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	wsID := uuid.New()
	userID := uuid.New()
	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(wsID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow(""))
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(uuid.New()))

	assert.False(t, UserIsWorkspaceMember(context.Background(), sqlxDB, wsID, userID))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserOwnsWorkspace(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	wsID := uuid.New()
	owner := uuid.New()
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(owner))
	assert.True(t, UserOwnsWorkspace(context.Background(), sqlxDB, wsID, owner))

	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnError(errors.New("no rows"))
	assert.False(t, UserOwnsWorkspace(context.Background(), sqlxDB, wsID, owner))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentIsInWorkspace(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	wsID := uuid.New()
	agentID := uuid.New()
	mock.ExpectQuery("SELECT workspace_id FROM agents").
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	assert.True(t, AgentIsInWorkspace(context.Background(), sqlxDB, wsID, agentID))

	// An agent of a different workspace.
	mock.ExpectQuery("SELECT workspace_id FROM agents").
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(uuid.New()))
	assert.False(t, AgentIsInWorkspace(context.Background(), sqlxDB, wsID, agentID))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestMembershipHelpers_NilDBFailClosed: no database means no proof of
// membership, which must read as "not a member".
func TestMembershipHelpers_NilDBFailClosed(t *testing.T) {
	ctx := context.Background()
	assert.False(t, UserIsWorkspaceMember(ctx, nil, uuid.New(), uuid.New()))
	assert.False(t, UserOwnsWorkspace(ctx, nil, uuid.New(), uuid.New()))
	assert.False(t, AgentIsInWorkspace(ctx, nil, uuid.New(), uuid.New()))
}

// TestRoleHasPermission mirrors the matrix RequirePermission uses, for the one
// caller that has to borrow it: Spark's install route, whose target workspace is
// in the request body where no middleware can see it.
func TestRoleHasPermission(t *testing.T) {
	assert.True(t, RoleHasPermission("owner", PermRegisterAgent))
	assert.True(t, RoleHasPermission("admin", PermRegisterAgent))
	assert.False(t, RoleHasPermission("member", PermRegisterAgent))
	assert.False(t, RoleHasPermission("viewer", PermRegisterAgent))
	assert.False(t, RoleHasPermission("", PermRegisterAgent))
	assert.False(t, RoleHasPermission("not-a-role", PermRegisterAgent))
}
