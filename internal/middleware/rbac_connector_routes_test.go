package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MCP-OAuth sec (#266eae6a): RequireConnectorPermission is the role bar for
// routes that have no rbac(). It must bind an OAuth connector to its user's
// CURRENT role and change nothing for humans or X-Agent-Key agents.

// runGuard runs a guard around a handler that records whether it was reached.
// Asserting only the response code is not enough: c.JSON returns nil on a
// successful write, so a guard that writes its 403 and then still calls next
// produces a correct-looking status while the handler goes on to do the write.
func runGuard(t *testing.T, guard echo.MiddlewareFunc, c echo.Context) (reached bool) {
	t.Helper()
	h := guard(func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	})
	require.NoError(t, h(c))
	return reached
}

// connectorWithParam is newRBACOAuthConnectorContext plus an :agent_id path
// param, the shape POST /agents/:agent_id/activity presents.
func connectorWithParam(callerAgentID, wsID, userID uuid.UUID, param string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("agent_id")
	c.SetParamValues(param)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, callerAgentID)
	c.Set(ContextKeyWorkspaceID, wsID)
	c.Set(ContextKeyAgentAuthWorkspaceID, wsID)
	c.Set(ContextKeyOAuthConnectorUserID, userID)
	return c, rec
}

func TestRequireConnectorPermission_RoleMatrix(t *testing.T) {
	cases := []struct {
		name string
		perm Permission
		role string
		want int
	}{
		// PATCH /projects/:id and the status routes — the card's e2e case.
		{"member cannot manage project", PermManageProject, domain.RoleMember, http.StatusForbidden},
		{"viewer cannot manage project", PermManageProject, domain.RoleViewer, http.StatusForbidden},
		{"admin can manage project", PermManageProject, domain.RoleAdmin, http.StatusOK},
		{"owner can manage project", PermManageProject, domain.RoleOwner, http.StatusOK},

		// Memory and knowledge writes — what the MCP server legitimately does.
		{"member can write memory", PermWriteMemory, domain.RoleMember, http.StatusOK},
		{"viewer cannot write memory", PermWriteMemory, domain.RoleViewer, http.StatusForbidden},
		{"admin can write memory", PermWriteMemory, domain.RoleAdmin, http.StatusOK},

		// Index maintenance spends embedding quota.
		{"member cannot rebuild memory index", PermMemoryIndex, domain.RoleMember, http.StatusForbidden},
		{"admin can rebuild memory index", PermMemoryIndex, domain.RoleAdmin, http.StatusOK},

		// Checkout rides on the task-update bar; comment posts on the comment bar.
		{"member can checkout (update task)", PermUpdateTask, domain.RoleMember, http.StatusOK},
		{"viewer cannot checkout (update task)", PermUpdateTask, domain.RoleViewer, http.StatusForbidden},
		{"viewer cannot post a project update (add comment)", PermAddComment, domain.RoleViewer, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRBACMockMemberRepo()
			wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
			repo.addMember(wsID, userID, tc.role)

			c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
			reached := runGuard(t, RequireConnectorPermission(tc.perm, repo, nil), c)
			assert.Equal(t, tc.want, rec.Code)
			assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
		})
	}
}

// The regression the card asks for: a trusted X-Agent-Key agent and a human
// must reach the same routes exactly as before, even when they hold none of the
// new permissions. If this ever fails, the clamp has started narrowing people.
func TestRequireConnectorPermission_DoesNotTouchAgentKeyAgentsOrHumans(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	// A human viewer: holds no PermManageProject / PermWriteMemory.
	repo.addMember(wsID, userID, domain.RoleViewer)

	// PermMemoryIndex is deliberately excluded from this loop (task 440358b6):
	// it moved into agentPerms once its routes moved to rbac(), so the
	// "precondition: not in agentPerms" assertion below no longer holds for
	// it — see TestRBAC_MemoryMaintenance_TrustedAgentCanRebuildIndex for its
	// own coverage of the X-Agent-Key path.
	for _, perm := range []Permission{PermManageProject, PermWriteMemory} {
		t.Run("agk_ agent / "+string(perm), func(t *testing.T) {
			require.False(t, agentPerms[perm], "precondition: the new permissions are not in agentPerms — that is the point")
			c, rec := newRBACAgentContext(agentID, wsID) // no connector user id set
			reached := runGuard(t, RequireConnectorPermission(perm, repo, nil), c)
			assert.Equal(t, http.StatusOK, rec.Code, "an X-Agent-Key agent must not be clamped by the connector bar")
			assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
		})
		t.Run("human viewer / "+string(perm), func(t *testing.T) {
			c, rec := newRBACEchoContext(userID, wsID)
			reached := runGuard(t, RequireConnectorPermission(perm, repo, nil), c)
			assert.Equal(t, http.StatusOK, rec.Code, "a human keeps whatever the route enforces today; this middleware must not add a bar")
			assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
		})
	}
}

func TestRequireConnectorPermission_RoleDowngradeAppliesOnNextRequest(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleAdmin)
	guard := RequireConnectorPermission(PermManageProject, repo, nil)

	c1, rec1 := newRBACOAuthConnectorContext(agentID, wsID, userID)
	reached1 := runGuard(t, guard, c1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.True(t, reached1)

	demoteToMember(repo, wsID, userID)

	c2, rec2 := newRBACOAuthConnectorContext(agentID, wsID, userID)
	reached2 := runGuard(t, guard, c2)
	assert.Equal(t, http.StatusForbidden, rec2.Code, "the user's role dropped between two requests; the connector must not keep the old bar")
	assert.False(t, reached2, "and the handler must not run")
}

func TestRequireConnectorPermission_UserRemovedFromWorkspaceIsDenied(t *testing.T) {
	// No membership row at all (user removed): GetRole errors/returns "", so the
	// clamp must fail closed rather than fall through to next().
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	reached := runGuard(t, RequireConnectorPermission(PermWriteMemory, repo, nil), c)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
}

func TestRequireConnectorPermission_ConnectorWithoutWorkspaceContextIsDenied(t *testing.T) {
	repo := newRBACMockMemberRepo()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())
	c.Set(ContextKeyOAuthConnectorUserID, uuid.New()) // connector, but no bound workspace

	reached := runGuard(t, RequireConnectorPermission(PermWriteMemory, repo, nil), c)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
}

func TestRequireConnectorSelfOrPermission(t *testing.T) {
	t.Run("a viewer connector may act on its own agent record", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleViewer)

		c, rec := connectorWithParam(agentID, wsID, userID, agentID.String())
		reached := runGuard(t, RequireConnectorSelfOrPermission("agent_id", PermDeleteAgent, repo, nil), c)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
	})
	t.Run("a member connector cannot write to another agent's record", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleMember)

		c, rec := connectorWithParam(agentID, wsID, userID, uuid.NewString())
		reached := runGuard(t, RequireConnectorSelfOrPermission("agent_id", PermDeleteAgent, repo, nil), c)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
	})
	t.Run("an admin connector may write to another agent's record", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleAdmin)

		c, rec := connectorWithParam(agentID, wsID, userID, uuid.NewString())
		reached := runGuard(t, RequireConnectorSelfOrPermission("agent_id", PermDeleteAgent, repo, nil), c)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
	})
	t.Run("a malformed id is never treated as self", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleViewer)

		c, rec := connectorWithParam(agentID, wsID, userID, "not-a-uuid")
		reached := runGuard(t, RequireConnectorSelfOrPermission("agent_id", PermDeleteAgent, repo, nil), c)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
	})
	t.Run("an X-Agent-Key agent writing to another agent is unchanged", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID := uuid.New()
		c, rec := newRBACAgentContextWithParam(uuid.New(), wsID, uuid.New())
		reached := runGuard(t, RequireConnectorSelfOrPermission("agent_id", PermDeleteAgent, repo, nil), c)
		assert.Equal(t, http.StatusOK, rec.Code, "this middleware clamps connectors only; agk_ behaviour on the route is not its business")
		assert.Equal(t, rec.Code == http.StatusOK, reached, "the handler must run if and only if the guard let the request through")
	})
}

// The new permissions must live in the role matrix, and (with one deliberate
// exception) nowhere else: not in agentPerms (nothing reads them for an agk_
// agent, and adding one would make RequirePermission start admitting agk_
// agents to it). PermMemoryIndex is that exception (task 440358b6): its
// routes moved to rbac(), and mesh-embed-backfill.sh / rechunk-prod-corpus.sh
// reach them via a plain X-Agent-Key, not a workspace role — so it must be
// in agentPerms, unlike its two siblings.
func TestNewConnectorPermissions_MatrixShape(t *testing.T) {
	for _, p := range []Permission{PermManageProject, PermWriteMemory} {
		assert.False(t, agentPerms[p], "%s must not be in agentPerms", p)
	}
	assert.True(t, agentPerms[PermMemoryIndex], "PermMemoryIndex must be in agentPerms: the reindex/backfill routes are called by X-Agent-Key fleet jobs, not just human roles")

	for _, p := range []Permission{PermManageProject, PermWriteMemory, PermMemoryIndex} {
		assert.False(t, hasPermission(domain.RoleViewer, p), "viewer must hold no write permission: %s", p)
		assert.True(t, hasPermission(domain.RoleOwner, p), "owner holds %s", p)
		assert.True(t, hasPermission(domain.RoleAdmin, p), "admin holds %s", p)
	}
	assert.False(t, hasPermission(domain.RoleMember, PermManageProject))
	assert.False(t, hasPermission(domain.RoleMember, PermMemoryIndex))
	assert.True(t, hasPermission(domain.RoleMember, PermWriteMemory), "a member's connector must keep memory writes: the MCP server uses them")
}

// RequirePermission (the rbac() path) shares clampConnector with the new
// middleware. The pre-existing connector tests assert only the status code, which
// cannot tell "denied" from "denied, then the handler ran anyway" — the state a
// refactor of the shared helper briefly produced. This pins the handler.
func TestRequirePermission_ConnectorDeniedNeverReachesTheHandler(t *testing.T) {
	cases := []struct {
		name string
		perm Permission
		role string
		want bool
	}{
		{"member connector, manage rules (not in the member role)", PermManageRules, domain.RoleMember, false},
		{"viewer connector, create task", PermCreateTask, domain.RoleViewer, false},
		{"member connector, create task", PermCreateTask, domain.RoleMember, true},
		{"admin connector, manage rules", PermManageRules, domain.RoleAdmin, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRBACMockMemberRepo()
			wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
			repo.addMember(wsID, userID, tc.role)

			c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
			reached := runGuard(t, RequirePermission(tc.perm, repo), c)
			assert.Equal(t, tc.want, reached, "handler reached")
			if tc.want {
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code)
			}
		})
	}
}

// The owner of a workspace whose own membership row was never written is
// admitted at consent (oauthService.resolveMemberRole) and by the workspace
// guard on every other route. The clamp must not 403 their connector on the
// routes it newly guards — and must not turn the fallback into a way in for
// someone who is not the owner.
func TestRequireConnectorPermission_OwnerWithoutMembershipRow(t *testing.T) {
	wsID, agentID, ownerID, strangerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	owns := func(_ context.Context, ws, user uuid.UUID) bool { return ws == wsID && user == ownerID }

	t.Run("owner with no row is admitted", func(t *testing.T) {
		repo := newRBACMockMemberRepo() // no membership rows at all
		c, rec := newRBACOAuthConnectorContext(agentID, wsID, ownerID)
		reached := runGuard(t, RequireConnectorPermission(PermManageProject, repo, owns), c)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, reached)
	})
	t.Run("a user who neither has a row nor owns the workspace is refused", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		c, rec := newRBACOAuthConnectorContext(agentID, wsID, strangerID)
		reached := runGuard(t, RequireConnectorPermission(PermWriteMemory, repo, owns), c)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, reached)
	})
	t.Run("the fallback does not override a real row", func(t *testing.T) {
		// A viewer row wins over "owns": if the owner check said yes for a user
		// who does have a (lower) role row, the row's role must still apply.
		repo := newRBACMockMemberRepo()
		repo.addMember(wsID, ownerID, domain.RoleViewer)
		c, rec := newRBACOAuthConnectorContext(agentID, wsID, ownerID)
		reached := runGuard(t, RequireConnectorPermission(PermWriteMemory, repo, owns), c)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, reached)
	})
}
