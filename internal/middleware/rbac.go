package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Permission represents a specific action that may be restricted by role.
type Permission string

const (
	PermDeleteWorkspace Permission = "delete_workspace"
	PermManageMembers   Permission = "manage_members"
	PermCreateProject   Permission = "create_project"
	PermDeleteProject   Permission = "delete_project"
	PermRegisterAgent   Permission = "register_agent"
	PermDeleteAgent     Permission = "delete_agent"
	PermCreateTask      Permission = "create_task"
	PermUpdateTask      Permission = "update_task"
	PermDeleteTask      Permission = "delete_task"
	PermAddComment      Permission = "add_comment"
	PermUploadArtifact  Permission = "upload_artifact"
	PermPublishEvent    Permission = "publish_event"
	PermManageCF        Permission = "manage_custom_fields"
	PermExportAuditLog  Permission = "export_audit_log"
	PermManageWebhooks  Permission = "manage_webhooks"
)

// permissionMatrix maps a role name to the set of permissions it holds.
// owner has all permissions.
// admin mirrors owner for now (same as owner); add distinctions as product evolves.
// member can create/update/delete tasks, comments, artifacts, events, and manage CF.
// viewer has no write permissions (read-only access is handled at the route level).
// agent can perform task/comment/artifact/event operations only.
var permissionMatrix = map[string]map[Permission]bool{
	domain.RoleOwner: {
		PermDeleteWorkspace: true,
		PermManageMembers:   true,
		PermCreateProject:   true,
		PermDeleteProject:   true,
		PermRegisterAgent:   true,
		PermDeleteAgent:     true,
		PermCreateTask:      true,
		PermUpdateTask:      true,
		PermDeleteTask:      true,
		PermAddComment:      true,
		PermUploadArtifact:  true,
		PermPublishEvent:    true,
		PermManageCF:        true,
		PermExportAuditLog:  true,
		PermManageWebhooks:  true,
	},
	domain.RoleAdmin: {
		// Admin has the same permissions as owner.
		PermDeleteWorkspace: true,
		PermManageMembers:   true,
		PermCreateProject:   true,
		PermDeleteProject:   true,
		PermRegisterAgent:   true,
		PermDeleteAgent:     true,
		PermCreateTask:      true,
		PermUpdateTask:      true,
		PermDeleteTask:      true,
		PermAddComment:      true,
		PermUploadArtifact:  true,
		PermPublishEvent:    true,
		PermManageCF:        true,
		PermExportAuditLog:  true,
		PermManageWebhooks:  true,
	},
	domain.RoleMember: {
		PermCreateProject:  true,
		PermCreateTask:     true,
		PermUpdateTask:     true,
		PermDeleteTask:     true,
		PermAddComment:     true,
		PermUploadArtifact: true,
		PermPublishEvent:   true,
		PermManageCF:       true,
	},
	domain.RoleViewer: {
		// Viewer: read-only, no write actions.
	},
}

// agentPerms defines which permissions agents (authenticated via X-Agent-Key) hold.
// Agents can perform task/comment/artifact/event operations only.
var agentPerms = map[Permission]bool{
	PermCreateTask:     true,
	PermUpdateTask:     true,
	PermDeleteTask:     true,
	PermAddComment:     true,
	PermUploadArtifact: true,
	PermPublishEvent:   true,
}

// RequirePermission returns Echo middleware that enforces a specific permission.
//
// For agents (authenticated via X-Agent-Key): checks agentPerms map — no DB lookup.
// For users (authenticated via JWT): looks up the workspace role from workspace_members
// and checks the permissionMatrix — one SELECT per request.
//
// The workspace_id is resolved from the Echo context (set by WorkspaceRLS middleware
// or by AgentKeyAuth). Routes that do not have a workspace in context will return 403.
func RequirePermission(perm Permission, memberRepo repository.WorkspaceMemberRepository) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// --- Agents: fast-path, no DB lookup. ---
			if IsAgent(c) {
				if !agentPerms[perm] {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("agents cannot perform this action"))
				}
				return next(c)
			}

			// --- Users: resolve workspace_id and look up role. ---
			wsID, err := GetWorkspaceID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace context required"))
			}

			userID, err := GetUserID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
			}

			role, err := memberRepo.GetRole(c.Request().Context(), wsID, userID)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("not a workspace member"))
			}

			if !hasPermission(role, perm) {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("insufficient permissions"))
			}

			return next(c)
		}
	}
}

// hasPermission returns true if the given role holds the given permission.
func hasPermission(role string, perm Permission) bool {
	perms, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	return perms[perm]
}
