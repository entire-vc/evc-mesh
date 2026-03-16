package middleware

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RequireProjectMember returns Echo middleware that enforces project-level membership.
//
// Resolution:
//   - Extracts project_id from :proj_id route param.
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
				// No project in route — skip check (non-project route).
				return next(c)
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
				agentID, agentErr := GetAgentID(c)
				if agentErr != nil {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("agent context required"))
				}

				var exists bool
				dbErr := db.QueryRowContext(c.Request().Context(),
					"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND agent_id = $2)",
					projID, agentID,
				).Scan(&exists)
				if dbErr != nil || !exists {
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
