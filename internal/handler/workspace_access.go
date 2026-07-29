package handler

import (
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// workspaceAccess authorizes a caller against a workspace they named themselves.
//
// It exists for the routes the central guard cannot see. RequireWorkspaceMemberScoped
// resolves the tenant from the request PATH, so a handler that instead takes its
// workspace from a JSON body field or a query parameter is invisible to it — the
// route looks like /auth/me, a route with nothing to check, and the caller's own
// claim about which tenant they are acting on goes unexamined.
//
// MemoryHandler.workspaceAllowed was the first instance of this rule; this is the
// same rule with one implementation, so a second handler in the same shape cannot
// get a subtly different version of it.
//
// Both credential types have to be handled, and the reason is the trap in this
// codebase: rbac() looks like a second line of defence and is not one. On an agent
// key it short-circuits to a static capability map and never looks at the target
// object's workspace at all, so an ordinary agent key — which any self-hoster's
// users can mint in their own workspace — walks straight through a route that only
// has rbac() in front of it.
type workspaceAccess struct {
	members repository.WorkspaceMemberRepository
}

// allows reports whether the caller may act inside wsID.
func (g workspaceAccess) allows(c echo.Context, wsID uuid.UUID) bool {
	if wsID == uuid.Nil || g.members == nil {
		return false
	}

	if mw.IsAgent(c) {
		own, err := mw.GetWorkspaceID(c)
		return err == nil && own == wsID
	}

	userID, err := mw.GetUserID(c)
	if err != nil {
		return false
	}
	// GetRole errors (incl. sql.ErrNoRows) when no membership row exists.
	_, err = g.members.GetRole(c.Request().Context(), wsID, userID)
	return err == nil
}

// require resolves the workspace the caller is authorized to act on, given the one
// they asked for.
//
// A client-supplied workspace id is never trusted on its own — it is honoured only
// after the caller proves access to it. When the request names none, the workspace
// on the auth context is used instead (agent keys set this; user tokens do not),
// which is the caller's own and needs no check.
func (g workspaceAccess) require(c echo.Context, requested uuid.UUID) (uuid.UUID, error) {
	if requested == uuid.Nil {
		own, err := mw.GetWorkspaceID(c)
		if err != nil {
			return uuid.Nil, apierror.BadRequest("workspace_id is required")
		}
		requested = own
	}

	if !g.allows(c, requested) {
		return uuid.Nil, apierror.Forbidden("workspace access denied")
	}
	return requested, nil
}
