package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupAgentCtx builds an Echo context mimicking DualAuth + WorkspaceRLS for an agent caller.
// wsID is the workspace resolved from the requested resource (e.g. the task's workspace).
// agentID is the authenticated agent's UUID.
func setupAgentCtx(e *echo.Echo, wsID, agentID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyWorkspaceID, wsID) // set by WorkspaceRLS (from task_id → project → workspace)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)
	return c, rec
}

// setupWSMemberCtx builds an Echo context with the workspace_id and auth_type already set,
// mimicking what DualAuth + WorkspaceRLS produce before our middleware runs.
func setupWSMemberCtx(e *echo.Echo, wsID uuid.UUID, authType string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyWorkspaceID, wsID)
	c.Set(ContextKeyAuthType, authType)
	return c, rec
}

// nopHandler is a minimal next handler that records it was reached.
func nopHandler(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

// TestRequireWorkspaceMember_User_Member verifies a user with a workspace role passes through.
func TestRequireWorkspaceMember_User_Member(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()

	c, rec := setupWSMemberCtx(e, wsID, AuthTypeUser)
	c.Set(ContextKeyWorkspaceRole, "member")

	// RequireWorkspaceMember with nil db — user path never queries DB.
	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireWorkspaceMember_User_Owner verifies a workspace owner passes through.
func TestRequireWorkspaceMember_User_Owner(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()

	c, rec := setupWSMemberCtx(e, wsID, AuthTypeUser)
	c.Set(ContextKeyWorkspaceRole, "owner")

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireWorkspaceMember_User_NotMember verifies a user with no role gets 403.
func TestRequireWorkspaceMember_User_NotMember(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()

	c, rec := setupWSMemberCtx(e, wsID, AuthTypeUser)
	// workspace_role not set — user is not a member of this workspace

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, http.StatusForbidden, apiErr.Code)
}

// TestRequireWorkspaceMember_User_EmptyRole verifies an empty role string yields 403.
func TestRequireWorkspaceMember_User_EmptyRole(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()

	c, rec := setupWSMemberCtx(e, wsID, AuthTypeUser)
	c.Set(ContextKeyWorkspaceRole, "")

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRequireWorkspaceMember_NoWorkspaceID verifies a missing workspace_id context key yields 403.
func TestRequireWorkspaceMember_NoWorkspaceID(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyWorkspaceRole, "member")
	// workspace_id deliberately not set

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// Agent path — cross-workspace IDOR tests
// ---------------------------------------------------------------------------

// newMockDB creates a sqlx.DB backed by go-sqlmock for agent workspace lookup tests.
func newMockDB(t *testing.T) (*sqlx.DB, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return sqlx.NewDb(rawDB, "postgres"), mock
}

// TestRequireWorkspaceMember_Agent_SameWorkspace verifies an agent accessing a resource
// in its own workspace passes through.
func TestRequireWorkspaceMember_Agent_SameWorkspace(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()
	agentID := uuid.New()

	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID.String()))

	c, rec := setupAgentCtx(e, wsID, agentID)

	mw := RequireWorkspaceMember(db)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMember_Agent_CrossWorkspace_IDOR verifies that an agent from
// workspace-A cannot access a resource belonging to workspace-B (cross-workspace IDOR).
//
// Before the wsAccess fix, WorkspaceRLS overwrote the workspace context from the task's
// workspace (B), but without RequireWorkspaceMember on the route, no check confirmed the
// caller belonged to B. This test documents the expected 403 after the fix.
func TestRequireWorkspaceMember_Agent_CrossWorkspace_IDOR(t *testing.T) {
	e := echo.New()
	workspaceA := uuid.New() // agent's own workspace
	workspaceB := uuid.New() // target task's workspace — different

	agentID := uuid.New()

	db, mock := newMockDB(t)
	// DB returns agent's real workspace (A); context has the target workspace (B).
	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(workspaceA.String()))

	// Context workspace_id = B (set by WorkspaceRLS resolving from the task's project).
	c, rec := setupAgentCtx(e, workspaceB, agentID)

	mw := RequireWorkspaceMember(db)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, http.StatusForbidden, apiErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMember_Agent_NotFound verifies that an agent ID with no DB row
// (revoked/deleted agent) yields 403.
func TestRequireWorkspaceMember_Agent_NotFound(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()
	agentID := uuid.New()

	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"})) // empty result set → sql.ErrNoRows

	c, rec := setupAgentCtx(e, wsID, agentID)

	mw := RequireWorkspaceMember(db)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
