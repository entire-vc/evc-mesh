package middleware

import (
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RequireProjectMember returns Echo middleware that enforces project-level membership.
//
// It is only meaningful on routes whose pattern contains :proj_id. Mounted anywhere
// else it fails closed with a 500 and a logged error, because there is no project to
// check and passing the request through would present an absent gate as a present one.
// Task-scoped routes want RequireWorkspaceMember instead.
//
// Resolution:
//   - Extracts project_id from :proj_id route param; absent → 500 (misconfiguration).
//   - Workspace owners and admins bypass (they have access to all projects).
//   - For members/viewers/agents: checks project_members table.
//   - Returns 403 if the actor is not a project member.
//
// Must run after DualAuth and WorkspaceRLS (which sets ContextKeyWorkspaceRole).
func RequireProjectMember(db *sqlx.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract project_id from route.
			projIDStr := c.Param("proj_id")
			if projIDStr == "" {
				// This middleware is mounted on a route whose pattern carries no
				// :proj_id, so there is no project whose membership could be checked.
				//
				// This used to `return next(c)`, which made the guard a silent no-op:
				// a route reading `api.GET("/tasks/:task_id/x", h, projAccess)` looked
				// in the route table like it was locked behind project membership while
				// in fact running with NO gate at all — the workspace check isn't in
				// this chain either. That is strictly worse than an obviously ungated
				// route, because it is believed and then stops being reviewed.
				//
				// It is a wiring mistake, not a runtime condition: Echo route patterns
				// are static, so this either never fires or fires on every request to
				// that route. Fail closed and say so.
				//
				// 500, not 403, deliberately: the caller is not forbidden — the server
				// is misconfigured. Answering 403 would send whoever debugs it hunting
				// for a membership problem that does not exist, which is the same
				// "error names the wrong thing" failure this guard is meant to prevent.
				log.Printf("ERROR: RequireProjectMember is mounted on route %q which has no :proj_id parameter — "+
					"this guard cannot check anything there and would otherwise pass every caller through; "+
					"use RequireWorkspaceMember for task-scoped routes", c.Path())
				return c.JSON(http.StatusInternalServerError,
					apierror.InternalError("route misconfiguration: project guard applied to a route without a project"))
			}

			projID, err := uuid.Parse(projIDStr)
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
			}

			// Workspace owners and admins bypass project membership check.
			if role, ok := c.Get(ContextKeyWorkspaceRole).(string); ok {
				if role == domain.RoleOwner || role == domain.RoleAdmin {
					return next(c)
				}
			}

			// For agents: check agent_id in project_members.
			if IsAgent(c) {
				var agentID uuid.UUID
				agentID, err = GetAgentID(c)
				if err != nil {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("agent context required"))
				}

				var exists bool
				err = db.QueryRowContext(c.Request().Context(),
					"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND agent_id = $2)",
					projID, agentID,
				).Scan(&exists)
				if err != nil || !exists {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("agent is not a member of this project"))
				}
				return next(c)
			}

			// For users: check user_id in project_members.
			userID, err := GetUserID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
			}

			var exists bool
			err = db.QueryRowContext(c.Request().Context(),
				"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)",
				projID, userID,
			).Scan(&exists)
			if err != nil || !exists {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("not a member of this project"))
			}

			return next(c)
		}
	}
}
