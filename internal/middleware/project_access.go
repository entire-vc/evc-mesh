package middleware

import (
	"context"
	"database/sql"
	"errors"
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
// Task-scoped routes are covered by RequireTaskProjectMember, registered once on
// the API group.
//
// Resolution:
//   - Extracts project_id from :proj_id route param; absent → 500 (misconfiguration).
//   - Human workspace owners/admins bypass (they have access to all projects).
//   - Agents never get that bypass, even with an owner/admin-role connection: always
//     checks project_members. See the human-only guard in the implementation.
//   - For members/viewers/agents: checks project_members table.
//   - Returns 403 if the actor is not a project member.
//
// Must run after DualAuth and WorkspaceRLS/AgentKeyAuth (which set ContextKeyWorkspaceRole).
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
					"task-scoped routes are covered by the group-level RequireTaskProjectMember", c.Path())
				return c.JSON(http.StatusInternalServerError,
					apierror.InternalError("route misconfiguration: project guard applied to a route without a project"))
			}

			projID, err := uuid.Parse(projIDStr)
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
			}

			if msg := projectAccessDenial(c, dbProjectMembership{db}, projID); msg != "" {
				return c.JSON(http.StatusForbidden, apierror.Forbidden(msg))
			}
			return next(c)
		}
	}
}

// ProjectMembership answers whether a user or an agent has a project_members row
// for a project. postgres.ProjectMemberRepo implements it.
type ProjectMembership interface {
	ExistsMember(ctx context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error)
}

// dbProjectMembership is ProjectMembership over a bare *sqlx.DB, for the guards
// that are built before any repository exists.
type dbProjectMembership struct{ db *sqlx.DB }

func (m dbProjectMembership) ExistsMember(ctx context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error) {
	var exists bool
	switch {
	case userID != nil:
		err := m.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)",
			projectID, *userID,
		).Scan(&exists)
		return exists, err
	case agentID != nil:
		err := m.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND agent_id = $2)",
			projectID, *agentID,
		).Scan(&exists)
		return exists, err
	}
	return false, nil
}

// UserMayAccessProject is the human half of the project access rule: a
// workspace owner or admin reaches every project of the workspace, anyone else
// only the projects they are a member of. A lookup error is a refusal.
//
// It is exported for the one caller outside the middleware chain that hands out
// project access in another form — the extension ui-context token — so that the
// rule is written once and a token never reaches further than the API would.
func UserMayAccessProject(ctx context.Context, members ProjectMembership, projectID, userID uuid.UUID, workspaceRole string) bool {
	if workspaceRole == domain.RoleOwner || workspaceRole == domain.RoleAdmin {
		return true
	}
	ok, err := members.ExistsMember(ctx, projectID, &userID, nil)
	return err == nil && ok
}

// projectAccessDenial applies the project access rule to the request's actor and
// returns why it is refused, or "" when it may proceed. RequireProjectMember and
// RequireTaskProjectMember both answer from it, so a project reached by :proj_id
// and the same project reached through one of its tasks are gated alike.
func projectAccessDenial(c echo.Context, members ProjectMembership, projID uuid.UUID) string {
	// An extension actor is not a project member; its reach is the
	// workspace it was connected to, already enforced by WorkspaceRLS +
	// RequireWorkspaceMemberScoped, and the capability guard decided whether
	// this route is open to it at all.
	if IsExtension(c) {
		return ""
	}
	ctx := c.Request().Context()

	// Agents never get the owner/admin bypass, even with an owner/admin-role
	// connection. Since agent-conn-auth-u2, ContextKeyWorkspaceRole is also
	// populated for agents (from their agent_workspace_grants role), and unlike a
	// human owner, a workspace-scoped agent connection is not meant to imply access
	// to every project in the workspace, only the ones it is an explicit member of.
	// Every other consumer of this context value (rbac.go's RequirePermission,
	// activity_handler.go, project_handler.go) special-cases IsAgent(c) before
	// trusting it too.
	if IsAgent(c) {
		agentID, err := GetAgentID(c)
		if err != nil {
			return "agent context required"
		}
		if ok, err := members.ExistsMember(ctx, projID, nil, &agentID); err != nil || !ok {
			return "agent is not a member of this project"
		}
		return ""
	}

	userID, err := GetUserID(c)
	if err != nil {
		return "user context required"
	}
	role, _ := c.Get(ContextKeyWorkspaceRole).(string)
	if !UserMayAccessProject(ctx, members, projID, userID, role) {
		return "not a member of this project"
	}
	return ""
}

// UserProjectRole returns the project role of a user from project_members, read
// now: member is false when the user holds no row in the project. It does not
// apply the workspace owner/admin bypass; callers that want it check the
// workspace role first (see UserMayAccessProject).
func UserProjectRole(ctx context.Context, db sqlx.QueryerContext, projID, userID uuid.UUID) (role string, member bool, err error) {
	err = db.QueryRowxContext(ctx,
		"SELECT role::text FROM project_members WHERE project_id = $1 AND user_id = $2",
		projID, userID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role, true, nil
}
