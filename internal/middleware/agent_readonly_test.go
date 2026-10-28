package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// an internal ticket: an agent whose grant role is viewer wrote a comment (HTTP 200)
// because nothing applied that role to an agent actor.

func newRoleAgentContext(method, role string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())
	c.Set(ContextKeyWorkspaceID, uuid.New())
	c.Set(ContextKeyAgentAuthWorkspaceID, uuid.New())
	c.Set(ContextKeyWorkspaceRole, role)
	return c, rec
}

func TestAgentReadOnlyGuard_ViewerAgentRefusedOnEveryWriteMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		c, rec := newRoleAgentContext(method, domain.RoleViewer)
		reached := false
		err := AgentReadOnlyGuard()(func(c echo.Context) error { reached = true; return okHandler(c) })(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, rec.Code, method)
		assert.Contains(t, rec.Body.String(), "read-only agent credential", method)
		assert.False(t, reached, "%s reached the handler", method)
	}
}

func TestAgentReadOnlyGuard_ViewerAgentMayRead(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		c, rec := newRoleAgentContext(method, domain.RoleViewer)
		require.NoError(t, AgentReadOnlyGuard()(okHandler)(c))
		assert.Equal(t, http.StatusOK, rec.Code, method)
	}
}

// Backward compatibility: every existing agent key resolves to member (legacy
// fallback, OAuth connectors, chat sessions) or to its grant's admin/owner role;
// none of them may change behaviour.
func TestAgentReadOnlyGuard_OtherAgentRolesUnchanged(t *testing.T) {
	for _, role := range []string{"", domain.RoleMember, domain.RoleAdmin, domain.RoleOwner} {
		c, rec := newRoleAgentContext(http.MethodPost, role)
		require.NoError(t, AgentReadOnlyGuard()(okHandler)(c))
		assert.Equal(t, http.StatusOK, rec.Code, "role %q", role)
	}
}

// The guard is about agent credentials only: a human viewer keeps whatever the
// routes themselves allow (role checks for users live in rbac()).
func TestAgentReadOnlyGuard_HumanViewerNotTouched(t *testing.T) {
	c, rec := newRBACEchoContext(uuid.New(), uuid.New())
	c.Set(ContextKeyWorkspaceRole, domain.RoleViewer)
	require.NoError(t, AgentReadOnlyGuard()(okHandler)(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// Defense in depth: rbac() itself applies the viewer row of the matrix to a
// viewer agent instead of agentPerms, so a route outside the guarded group still
// refuses it.
func TestRBAC_ViewerAgent_RefusedOnEveryAgentPermission(t *testing.T) {
	require.NotEmpty(t, agentPerms)
	for perm := range agentPerms {
		c, rec := newRoleAgentContext(http.MethodPost, domain.RoleViewer)
		require.NoError(t, RequirePermission(perm, newRBACMockMemberRepo())(okHandler)(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, string(perm))
		assert.Contains(t, rec.Body.String(), "read-only agent credential", string(perm))

		// Control: the same permission with a member agent passes.
		c, rec = newRoleAgentContext(http.MethodPost, domain.RoleMember)
		require.NoError(t, RequirePermission(perm, newRBACMockMemberRepo())(okHandler)(c))
		assert.Equal(t, http.StatusOK, rec.Code, "member agent, %s", perm)
	}
}

func TestRequireConnectorPermission_ViewerAgentRefused(t *testing.T) {
	for _, perm := range []Permission{PermWriteMemory, PermAddComment, PermUpdateTask, PermManageProject} {
		c, rec := newRoleAgentContext(http.MethodPost, domain.RoleViewer)
		require.NoError(t, RequireConnectorPermission(perm, newRBACMockMemberRepo(), nil)(okHandler)(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, string(perm))

		// Control: a member X-Agent-Key agent is not clamped on these routes.
		c, rec = newRoleAgentContext(http.MethodPost, domain.RoleMember)
		require.NoError(t, RequireConnectorPermission(perm, newRBACMockMemberRepo(), nil)(okHandler)(c))
		assert.Equal(t, http.StatusOK, rec.Code, "member agent, %s", perm)
	}
}

// AC4: issuing a credential or changing what a credential may do is human-only.
// An agent cannot invite/revoke a grant (POST/DELETE agent-grants carry
// rbac(PermManageMembers)), register an agent, or issue/rotate a key
// (rbac(PermDeleteAgent)) — whatever role its own grant holds, admin included,
// so a viewer cannot be lifted to member by itself or by a neighbour agent.
// The route → permission pinning is in cmd/api/rbac_routes_audit_test.go.
func TestRBAC_NoAgentRoleCanIssueOrRegradeCredentials(t *testing.T) {
	for _, perm := range []Permission{PermManageMembers, PermRegisterAgent, PermDeleteAgent} {
		for _, role := range []string{domain.RoleViewer, domain.RoleMember, domain.RoleAdmin, domain.RoleOwner} {
			c, rec := newRoleAgentContext(http.MethodPost, role)
			require.NoError(t, RequirePermission(perm, newRBACMockMemberRepo())(okHandler)(c))
			assert.Equal(t, http.StatusForbidden, rec.Code, "agent role %q, %s", role, perm)
		}
	}
}
