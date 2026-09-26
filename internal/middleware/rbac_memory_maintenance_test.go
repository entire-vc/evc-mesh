package middleware

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task 440358b6: POST /memories/reindex, /memories/backfill-chunks,
// /memories/rechunk-stale and /memories/backfill-doc-index moved from
// connectorRBAC(mw.PermMemoryIndex) to rbac(mw.PermMemoryIndex) in main.go —
// connectorRBAC is a documented no-op for humans and X-Agent-Key agents (see
// RequireConnectorPermission's doc comment), so a plain human JWT, including a
// viewer, could call these routes and spend embedding quota. This file proves
// the full role matrix for RequirePermission (what rbac() calls) on
// PermMemoryIndex specifically, the permission these four routes now carry.

// TestRBAC_MemoryMaintenance_HumanRoleMatrix is the direct fix proof: every
// human role rbac(mw.PermMemoryIndex) now gates on, in one table so the matrix
// from the task's acceptance criteria is visible in one place.
func TestRBAC_MemoryMaintenance_HumanRoleMatrix(t *testing.T) {
	cases := []struct {
		role string
		want int
	}{
		{domain.RoleViewer, http.StatusForbidden},
		{domain.RoleMember, http.StatusForbidden},
		{domain.RoleAdmin, http.StatusOK},
		{domain.RoleOwner, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			repo := newRBACMockMemberRepo()
			wsID, userID := uuid.New(), uuid.New()
			repo.addMember(wsID, userID, tc.role)

			c, rec := newRBACEchoContext(userID, wsID)
			h := RequirePermission(PermMemoryIndex, repo)(okHandler)
			require.NoError(t, h(c))
			assert.Equal(t, tc.want, rec.Code, "role %s", tc.role)
		})
	}
}

// TestRBAC_MemoryMaintenance_TrustedAgentCanRebuildIndex is the fleet-op path:
// mesh-embed-backfill.sh and rechunk-prod-corpus.sh call these routes under a
// plain X-Agent-Key (no OAuthConnectorUserID in context), which is
// agentPerms' fast path, not a workspace-role lookup. (The hourly
// vc.entire.memory-reindex launchd job and mesh-embed-healthcheck.sh are NOT
// callers of these routes: the launchd job only rewrites local MEMORY.md
// shards, and mesh-embed-healthcheck.sh calls the embedding provider's own
// endpoint directly, not mesh-api.) This is why PermMemoryIndex had to be added to
// agentPerms deliberately (see its doc comment in rbac.go) rather than left
// for connectorRBAC to cover — connectorRBAC never touched X-Agent-Key
// agents to begin with, but rbac() checks agentPerms first and would 403 an
// agent that isn't listed there.
func TestRBAC_MemoryMaintenance_TrustedAgentCanRebuildIndex(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, agentID := uuid.New(), uuid.New()

	c, rec := newRBACAgentContext(agentID, wsID)
	h := RequirePermission(PermMemoryIndex, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRBAC_MemoryMaintenance_OAuthConnector_MemberDenied is the connector case
// from the task's matrix: an OAuth connector (mot_) whose consenting user is
// only a member must still be denied, exactly as it already was under
// connectorRBAC — moving the route to rbac() must not accidentally loosen the
// connector clamp, since RequirePermission's agent branch runs the identical
// clampConnector helper for any caller carrying ContextKeyOAuthConnectorUserID.
func TestRBAC_MemoryMaintenance_OAuthConnector_MemberDenied(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleMember)

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	h := RequirePermission(PermMemoryIndex, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRBAC_MemoryMaintenance_OAuthConnector_AdminAllowed proves the clamp is
// not a blanket connector denial: an admin-role connector keeps what the
// admin's own role legitimately holds, same as any other rbac() permission.
func TestRBAC_MemoryMaintenance_OAuthConnector_AdminAllowed(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleAdmin)

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	h := RequirePermission(PermMemoryIndex, repo)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRBAC_MemoryMaintenance_ConnectorRBACWasANoOpForHumans documents the
// vulnerability's exact mechanism: connectorRBAC (RequireConnectorPermission)
// was never wrong on its own terms — it only clamps a connector and is a
// documented no-op for anyone else — so wiring it onto a route did nothing to
// stop the plain human JWT case this task is about. This is why the fix is a
// route-wiring change (rbac() instead of connectorRBAC()), not a bug fix
// inside RequireConnectorPermission itself; this test keeps documenting the
// old wiring's behavior so a future reader does not mistake the no-op for a
// regression to chase inside that function.
func TestRBAC_MemoryMaintenance_ConnectorRBACWasANoOpForHumans(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, userID := uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleViewer)

	c, rec := newRBACEchoContext(userID, wsID)
	h := RequireConnectorPermission(PermMemoryIndex, repo, nil)(okHandler)
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code, "connectorRBAC must not gate a plain human JWT — this is exactly why these routes needed rbac() instead")
}
