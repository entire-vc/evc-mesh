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
// authWsID is the workspace the agent's PRESENTED KEY actually authenticated
// into (see ContextKeyAgentAuthWorkspaceID) — pass the same value as wsID to
// model "the caller's own key, own resource"; pass a different one to model a
// key scoped elsewhere being pointed at wsID (the #7661fc5d cross-workspace case).
// agentID is the authenticated agent's UUID.
func setupAgentCtx(e *echo.Echo, wsID, authWsID, agentID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyWorkspaceID, wsID) // set by WorkspaceRLS (from task_id → project → workspace)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)
	c.Set(ContextKeyAgentAuthWorkspaceID, authWsID) // set by AgentKeyAuth at auth time
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

// TestRequireWorkspaceMember_Agent_SameWorkspace verifies an agent accessing a resource
// in the SAME workspace its presented key authenticated into passes through.
// No DB query: the equality check against ContextKeyAgentAuthWorkspaceID is
// the whole proof (see RequireWorkspaceMember's doc comment) — db is nil to
// make that assertion explicit rather than incidental.
func TestRequireWorkspaceMember_Agent_SameWorkspace(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()
	agentID := uuid.New()

	c, rec := setupAgentCtx(e, wsID, wsID, agentID)

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireWorkspaceMember_Agent_CrossWorkspace_IDOR verifies that an agent from
// workspace-A cannot access a resource belonging to workspace-B (cross-workspace IDOR).
//
// Before the wsAccess fix, WorkspaceRLS overwrote the workspace context from the task's
// workspace (B), but without RequireWorkspaceMember on the route, no check confirmed the
// caller belonged to B. This test documents the expected 403 after the fix.
func TestRequireWorkspaceMember_Agent_CrossWorkspace_IDOR(t *testing.T) {
	e := echo.New()
	workspaceA := uuid.New() // the workspace the agent's key actually authenticated into
	workspaceB := uuid.New() // target task's workspace — a DIFFERENT tenant

	agentID := uuid.New()

	// Context workspace_id = B (set by WorkspaceRLS resolving from the task's project),
	// but the presented key's own auth-workspace is A — the two disagree.
	c, rec := setupAgentCtx(e, workspaceB, workspaceA, agentID)

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, http.StatusForbidden, apiErr.Code)
}

// TestRequireWorkspaceMember_Agent_MultiWorkspace_GrantElsewhereDoesNotGrantAccess
// is the direct regression test for #7661fc5d: an agent that legitimately
// holds an ACTIVE grant in the target workspace must still be refused when
// the key PRESENTED on this particular request authenticated into a
// different one. Membership somewhere is necessary but not sufficient — this
// is what distinguishes the fix from TestRequireWorkspaceMember_Agent_CrossWorkspace_IDOR
// above (no membership anywhere): here the agent unambiguously has legitimate
// standing in wsID, just not via THIS key.
//
// The DB mock is wired to answer the way the PRE-FIX AgentIsInWorkspace query
// would have — a real matching row for (agentID, guestWS) — specifically so
// that reverting the fix flips this test red instead of leaving it vacuously
// green: a db=nil or an unconfigured mock would fail-closed under the old
// code regardless of the actual security property, and prove nothing (caught
// on this exact test during independent verification of this fix).
// mock.ExpectationsWereMet() is deliberately NOT asserted — the fixed code is
// expected to never reach the DB at all; the mock exists only to make a
// regression to the old code observable, not to assert the new code's own
// (already-established, DB-free) behavior.
func TestRequireWorkspaceMember_Agent_MultiWorkspace_GrantElsewhereDoesNotGrantAccess(t *testing.T) {
	e := echo.New()
	homeWS := uuid.New()  // the workspace this request's key authenticated into
	guestWS := uuid.New() // a SECOND workspace the same agent also holds an active grant in
	agentID := uuid.New()

	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()
	db := sqlx.NewDb(rawDB, "postgres")
	mock.ExpectQuery(agentIsInWorkspaceQueryPattern).
		WithArgs(agentID, guestWS).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))

	// The agent really is a member of guestWS (a real grant exists) — but this
	// request's key resolved to homeWS, not guestWS.
	c, rec := setupAgentCtx(e, guestWS, homeWS, agentID)

	mw := RequireWorkspaceMember(db)
	err = mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"a key scoped to homeWS must not reach guestWS's data even though the agent has a real grant there")
}

// TestRequireWorkspaceMember_Agent_NoAuthWorkspaceInContext verifies fail-closed
// behavior when IsAgent(c) is true but ContextKeyAgentAuthWorkspaceID was never
// set — e.g. a middleware-ordering bug that runs this guard without AgentKeyAuth
// having run first. Missing proof must never default to "allowed".
func TestRequireWorkspaceMember_Agent_NoAuthWorkspaceInContext(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyWorkspaceID, wsID)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())
	// ContextKeyAgentAuthWorkspaceID deliberately not set.

	mw := RequireWorkspaceMember(nil)
	err := mw(nopHandler)(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
