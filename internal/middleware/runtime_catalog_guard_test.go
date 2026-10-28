package middleware

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The runtime catalog guard passes a plain agent key to the repository (which
// checks the runtime_manager grant) and keeps rbac for everyone else.
func TestRequireRuntimeCatalogPermission(t *testing.T) {
	guard := func(repo *rbacMockMemberRepo) echo.MiddlewareFunc {
		return RequireRuntimeCatalogPermission(PermManageWebhooks, repo)
	}
	t.Run("a plain agent key reaches the handler", func(t *testing.T) {
		c, _ := newRBACAgentContext(uuid.New(), uuid.New())
		assert.True(t, runGuard(t, guard(newRBACMockMemberRepo()), c))
	})
	t.Run("an OAuth connector keeps the rbac bar", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, userID := uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleOwner)
		c, rec := newRBACOAuthConnectorContext(uuid.New(), wsID, userID)
		assert.False(t, runGuard(t, guard(repo), c))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("a chat session keeps the rbac bar", func(t *testing.T) {
		c, rec := newRBACAgentContext(uuid.New(), uuid.New())
		c.Set(ContextKeyChatSession, &service.ChatSessionPrincipal{})
		assert.False(t, runGuard(t, guard(newRBACMockMemberRepo()), c))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("a member who is not admin is refused", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, userID := uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleMember)
		c, rec := newRBACEchoContext(userID, wsID)
		assert.False(t, runGuard(t, guard(repo), c))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("an admin passes", func(t *testing.T) {
		repo := newRBACMockMemberRepo()
		wsID, userID := uuid.New(), uuid.New()
		repo.addMember(wsID, userID, domain.RoleAdmin)
		c, _ := newRBACEchoContext(userID, wsID)
		assert.True(t, runGuard(t, guard(repo), c))
	})
}
