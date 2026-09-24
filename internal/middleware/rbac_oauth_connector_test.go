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

// ---------------------------------------------------------------------------
// MCP-OAuth 1/5 review fix (B1): an OAuth-connector agent (mot_ token) must
// never hold more than the consenting user's CURRENT workspace role — not
// the full agentPerms set a trusted X-Agent-Key lead agent gets, and not
// what the role was at consent time. See RequirePermission's IsAgent branch
// in rbac.go.
// ---------------------------------------------------------------------------

// newRBACOAuthConnectorContext creates an Echo context shaped the way
// setAgentAuthContext (auth.go) leaves it after a mot_ token authenticates:
// agent auth type, plus ContextKeyAgentAuthWorkspaceID and
// ContextKeyOAuthConnectorUserID set — the two keys the clamp reads.
func newRBACOAuthConnectorContext(agentID, wsID, connectorUserID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)
	c.Set(ContextKeyWorkspaceID, wsID)
	c.Set(ContextKeyAgentAuthWorkspaceID, wsID)
	c.Set(ContextKeyOAuthConnectorUserID, connectorUserID)
	return c, rec
}

func TestRBAC_OAuthConnector_MemberCannotManageRules(t *testing.T) {
	// This is B1 itself: agentPerms grants PermManageRules to every agent
	// (so a trusted lead agent like Garfield can use it via X-Agent-Key),
	// but a member's own workspace role does not hold PermManageRules at
	// all (permissionMatrix[RoleMember] has no PermManageRules entry). A
	// connector the member's consent spun up must be denied here even
	// though the blanket agentPerms fast path would allow it.
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	require.True(t, agentPerms[PermManageRules], "precondition: agentPerms must still grant this to trusted agents")

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	h := RequirePermission(PermManageRules, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_OAuthConnector_MemberCanStillCreateTask(t *testing.T) {
	// The clamp must not be a blanket denial — a member-role connector keeps
	// whatever the member's own role legitimately holds.
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	h := RequirePermission(PermCreateTask, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRBAC_OAuthConnector_ViewerDeniedEverything(t *testing.T) {
	// Defense in depth for B1's "viewer -> отказ в согласии" — even if a
	// viewer-role connector somehow existed, permissionMatrix[RoleViewer] is
	// empty, so every permission check on it fails, not just the dangerous
	// ones.
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleViewer)

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	h := RequirePermission(PermCreateTask, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRBAC_OAuthConnector_RoleDowngradeTakesEffectImmediately(t *testing.T) {
	// "Роль пользователя проверять на КАЖДОМ запросе с mot_, а не один раз
	// при согласии" — same repo, same connector, role changes between two
	// requests, no new token issuance in between.
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleAdmin)

	c1, rec1 := newRBACOAuthConnectorContext(agentID, wsID, userID)
	h := RequirePermission(PermManageRules, repo)(okHandler)
	require.NoError(t, h(c1))
	assert.Equal(t, http.StatusOK, rec1.Code, "admin-consented connector starts with PermManageRules")

	demoteToMember(repo, wsID, userID)

	c2, rec2 := newRBACOAuthConnectorContext(agentID, wsID, userID)
	require.NoError(t, h(c2))
	assert.Equal(t, http.StatusForbidden, rec2.Code, "same connector, same request shape, but the user's role dropped — this request must reflect that now")
}

// demoteToMember is a small test-only patch since rbacMockMemberRepo's
// UpdateRole (like the rest of that mock) is a no-op stub — see its doc
// comment in rbac_test.go. Mutating the map directly is simpler than adding
// real UpdateRole semantics to a mock nothing else needs.
func demoteToMember(repo *rbacMockMemberRepo, wsID, userID uuid.UUID) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, m := range repo.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			m.Role = domain.RoleMember
		}
	}
}

func TestRBAC_TrustedAgent_StillGetsFullAgentPerms(t *testing.T) {
	// Regression guard: an X-Agent-Key agent (no OAuthConnectorUserID in
	// context at all) must keep the original fast path unchanged — this is
	// the Garfield-lead-agent case agentPerms[PermManageRules] exists for.
	repo := newRBACMockMemberRepo()
	wsID, agentID := uuid.New(), uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)
	h := RequirePermission(PermManageRules, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}
