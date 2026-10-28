package middleware

import (
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// RequireRuntimeCatalogPermission guards the workspace runtime catalog
// (inventory, revisions, save, restore). People, OAuth connectors, chat
// sessions and extensions get RequirePermission(perm) unchanged. A plain
// X-Agent-Key agent is passed to RuntimeRepo, which admits it only under its
// operator's runtime_manager grant ("Can manage My Runner"), in a workspace the
// operator owns or administers and the agent is connected to; without the grant
// it answers 403 with that reason instead of rbac's generic agent refusal.
func RequireRuntimeCatalogPermission(perm Permission, memberRepo repository.WorkspaceMemberRepository) echo.MiddlewareFunc {
	guard := RequirePermission(perm, memberRepo)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		guarded := guard(next)
		return func(c echo.Context) error {
			if _, connector := GetOAuthConnectorUserID(c); IsAgent(c) && !connector && !IsChatSession(c) && !IsExtension(c) {
				return next(c)
			}
			return guarded(c)
		}
	}
}
