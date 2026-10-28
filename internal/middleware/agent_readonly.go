package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ReadOnlyAgentMessage is the 403 text a viewer-role agent credential gets on
// any state-changing request. Exported so tests assert the guard that fired,
// not just a status code another middleware could also produce.
const ReadOnlyAgentMessage = "read-only agent credential: this key's workspace role is viewer, which permits GET/HEAD only"

// IsReadOnlyAgent reports whether the caller is an agent whose credential
// resolved to the viewer role in this workspace. The role comes from the
// agent_workspace_grants connection the key matched (agentService.Authenticate,
// set by setAgentAuthContext); a legacy key with no connection resolves to
// member, OAuth connectors and chat-session tokens to member, so none of them
// is affected.
//
// Before an internal ticket that role was set and then ignored: RequirePermission
// checked the fixed agentPerms by actor type, so a viewer-grant agent could
// still comment, create tasks and write memory.
func IsReadOnlyAgent(c echo.Context) bool {
	if !IsAgent(c) {
		return false
	}
	role, _ := c.Get(ContextKeyWorkspaceRole).(string)
	return role == domain.RoleViewer
}

// AgentReadOnlyGuard refuses every non-read method from a read-only agent
// credential (see IsReadOnlyAgent). It is mounted once on the whole
// authenticated /api/v1 group, right after DualAuth, so it also covers the
// routes that carry no rbac() — comments, memory, human-gate decisions,
// heartbeats, read-only POST computations — rather than relying on each route
// to name a permission. Strict by method on purpose: "read-only" is a promise
// about the HTTP verbs the key may use, not a per-handler judgment of which
// POSTs happen to persist nothing.
//
// Inert for humans, extensions and agents with any other role.
func AgentReadOnlyGuard() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if IsReadOnlyAgent(c) && !isReadMethod(c.Request().Method) {
				return c.JSON(http.StatusForbidden, apierror.Forbidden(ReadOnlyAgentMessage))
			}
			return next(c)
		}
	}
}

func isReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}
