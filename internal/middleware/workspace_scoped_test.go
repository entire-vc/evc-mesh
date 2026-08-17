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
)

// scopedCtx builds an Echo context carrying a :ws_id path parameter, mimicking what
// the router plus DualAuth and WorkspaceRLS produce before the scoped guard runs.
// Pass wsParam == "" to model a route that has no :ws_id at all.
func scopedCtx(e *echo.Echo, wsParam string, wsID uuid.UUID, authType string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsParam != "" {
		c.SetParamNames("ws_id")
		c.SetParamValues(wsParam)
		c.Set(ContextKeyWorkspaceID, wsID)
		// WorkspaceRLS records that this workspace came out of the path rather
		// than out of the caller's own credentials; the guard only trusts the
		// latter to mean "already mine".
		c.Set(ContextKeyWorkspaceSource, WorkspaceSourceParam)
	}
	c.Set(ContextKeyAuthType, authType)
	return c, rec
}

// TestRequireWorkspaceMemberScoped_NoWorkspaceParam_PassesThrough verifies the guard is
// inert on routes with no :ws_id — it is registered on the whole authenticated API group,
// so every workspace-free route (/auth/me, /tasks/:task_id, ...) flows through untouched.
func TestRequireWorkspaceMemberScoped_NoWorkspaceParam_PassesThrough(t *testing.T) {
	e := echo.New()

	c, rec := scopedCtx(e, "", uuid.Nil, AuthTypeUser)
	// Deliberately no workspace role and no workspace id: an unrelated route.

	mw := RequireWorkspaceMemberScoped(nil)
	require.NoError(t, mw(nopHandler)(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireWorkspaceMemberScoped_Member_Allowed verifies a member of the requested
// workspace still reaches the handler.
func TestRequireWorkspaceMemberScoped_Member_Allowed(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()

	c, rec := scopedCtx(e, wsID.String(), wsID, AuthTypeUser)
	c.Set(ContextKeyWorkspaceRole, "member")

	mw := RequireWorkspaceMemberScoped(nil)
	require.NoError(t, mw(nopHandler)(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireWorkspaceMemberScoped_Stranger_Forbidden is the regression test for the
// cross-tenant hole: a logged-in user with no membership in the requested workspace must
// be stopped by the guard alone, without the route having opted in.
func TestRequireWorkspaceMemberScoped_Stranger_Forbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	e := echo.New()
	wsID := uuid.New()
	strangerID := uuid.New()

	c, rec := scopedCtx(e, wsID.String(), wsID, AuthTypeUser)
	c.Set(ContextKeyUserID, strangerID)
	// No workspace role: WorkspaceRLS found no workspace_members row.

	// The owner fallback runs and finds a different owner.
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(uuid.New()))

	mw := RequireWorkspaceMemberScoped(sqlxDB)
	require.NoError(t, mw(nopHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, rec.Body.String(), "owner_id", "the 403 must not echo database internals")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_OwnerWithoutMemberRow_Allowed covers the legacy case
// the guard must not break: the owner membership row is written best-effort after the
// workspace itself, so a partial failure leaves an owner who is not a member. Those
// owners must keep access — otherwise this fix reads as "my workspace disappeared".
func TestRequireWorkspaceMemberScoped_OwnerWithoutMemberRow_Allowed(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	e := echo.New()
	wsID := uuid.New()
	ownerID := uuid.New()

	c, rec := scopedCtx(e, wsID.String(), wsID, AuthTypeUser)
	c.Set(ContextKeyUserID, ownerID)
	// No workspace role — the membership row is the one that went missing.

	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(ownerID))

	mw := RequireWorkspaceMemberScoped(sqlxDB)
	require.NoError(t, mw(nopHandler)(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_AgentOfAnotherWorkspace_Forbidden verifies the agent
// branch is enforced too. RBAC alone would not catch this: for agents RequirePermission
// takes a fast path with no workspace check at all, so an agent key from workspace A
// would otherwise reach workspace B on any route guarded only by rbac().
func TestRequireWorkspaceMemberScoped_AgentOfAnotherWorkspace_Forbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	sqlxDB := sqlx.NewDb(db, "postgres")

	e := echo.New()
	targetWS := uuid.New()
	agentID := uuid.New()
	agentWS := uuid.New()

	c, rec := scopedCtx(e, targetWS.String(), targetWS, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)

	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(agentWS))

	mw := RequireWorkspaceMemberScoped(sqlxDB)
	require.NoError(t, mw(nopHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_MalformedWorkspaceID_Forbidden verifies a :ws_id that
// WorkspaceRLS could not resolve (not a UUID, or a workspace that does not exist) is
// denied rather than falling through as "no workspace in context, nothing to check".
func TestRequireWorkspaceMemberScoped_MalformedWorkspaceID_Forbidden(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")
	c.Set(ContextKeyAuthType, AuthTypeUser)
	// WorkspaceRLS never set ContextKeyWorkspaceID because the param did not parse.

	mw := RequireWorkspaceMemberScoped(nil)
	require.NoError(t, mw(nopHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
