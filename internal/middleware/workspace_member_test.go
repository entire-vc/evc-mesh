package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

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
