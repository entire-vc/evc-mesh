package middleware

import (
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ContextKeyWorkspaceRole stores the workspace-level role of the current actor.
const ContextKeyWorkspaceRole = "workspace_role"

// WorkspaceRLS returns middleware that sets the PostgreSQL session variable
// app.current_workspace_id based on the request context. This enables
// Row-Level Security (RLS) policies at the database level.
//
// Resolution order:
//  1. ws_id route parameter (workspace routes)
//  2. proj_id route parameter -> look up project's workspace_id
//  3. workspace_id from auth context (agent key auth sets this)
//
// NOTE: set_config('app.current_workspace_id', $1, true) is used with the
// is_local flag set to true, which makes the value transaction-scoped (equivalent
// to SET LOCAL). Echo handlers do not run inside an explicit transaction, so the
// value effectively lasts until the end of the connection's implicit transaction.
// We set it on every request so connections reused from the pool always have a
// fresh value before handler execution.
func WorkspaceRLS(db *sqlx.DB, projectRepo repository.ProjectRepository) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var wsID uuid.UUID
			var resolved bool

			// 1. Try ws_id route parameter.
			if wsIDStr := c.Param("ws_id"); wsIDStr != "" {
				if id, err := uuid.Parse(wsIDStr); err == nil {
					wsID = id
					resolved = true
				}
			}

			// 2. Try proj_id route parameter -> look up project's workspace_id.
			if !resolved {
				projIDStr := c.Param("proj_id")
				if projIDStr == "" {
					// Also check project_id for backward compatibility.
					projIDStr = c.Param("project_id")
				}
				if projIDStr != "" {
					if projID, err := uuid.Parse(projIDStr); err == nil {
						proj, err := projectRepo.GetByID(c.Request().Context(), projID)
						if err == nil && proj != nil {
							wsID = proj.WorkspaceID
							resolved = true
						}
					}
				}
			}

			// 2b. Try task_id route parameter -> look up task's workspace via project.
			if !resolved {
				if taskIDStr := c.Param("task_id"); taskIDStr != "" {
					if taskID, err := uuid.Parse(taskIDStr); err == nil {
						var resolvedWsID uuid.UUID
						err := db.QueryRowContext(c.Request().Context(),
							"SELECT p.workspace_id FROM tasks t JOIN projects p ON t.project_id = p.id WHERE t.id = $1 AND t.deleted_at IS NULL",
							taskID,
						).Scan(&resolvedWsID)
						if err == nil {
							wsID = resolvedWsID
							resolved = true
						}
					}
				}
			}

			// 2b2. Try artifact_id route parameter -> look up workspace via artifacts → tasks → projects.
			if !resolved {
				if artifactIDStr := c.Param("artifact_id"); artifactIDStr != "" {
					if artifactID, err := uuid.Parse(artifactIDStr); err == nil {
						var resolvedWsID uuid.UUID
						err := db.QueryRowContext(c.Request().Context(),
							`SELECT p.workspace_id
							   FROM artifacts a
							   JOIN tasks t ON a.task_id = t.id AND t.deleted_at IS NULL
							   JOIN projects p ON t.project_id = p.id
							  WHERE a.id = $1`,
							artifactID,
						).Scan(&resolvedWsID)
						if err == nil {
							wsID = resolvedWsID
							resolved = true
						}
					}
				}
			}

			// 2c. Try agent_id route parameter -> look up agent's workspace_id.
			if !resolved {
				if agentIDStr := c.Param("agent_id"); agentIDStr != "" {
					if agentID, err := uuid.Parse(agentIDStr); err == nil {
						var resolvedWsID uuid.UUID
						err := db.QueryRowContext(c.Request().Context(),
							"SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL",
							agentID,
						).Scan(&resolvedWsID)
						if err == nil {
							wsID = resolvedWsID
							resolved = true
						}
					}
				}
			}

			// 2d. Try init_id route parameter -> look up initiative's workspace_id.
			if !resolved {
				if initIDStr := c.Param("init_id"); initIDStr != "" {
					if initID, err := uuid.Parse(initIDStr); err == nil {
						var resolvedWsID uuid.UUID
						err := db.QueryRowContext(c.Request().Context(),
							"SELECT workspace_id FROM initiatives WHERE id = $1",
							initID,
						).Scan(&resolvedWsID)
						if err == nil {
							wsID = resolvedWsID
							resolved = true
						}
					}
				}
			}

			// 3. Try workspace_id from auth context (set by agent key auth).
			if !resolved {
				if ctxWsID, err := GetWorkspaceID(c); err == nil {
					wsID = ctxWsID
					resolved = true
				}
			}

			// Set the session variable if we resolved a workspace ID.
			// Use set_config with the transaction-local flag (third arg = true) so the
			// value is scoped to the current transaction. The parameterized form prevents
			// any SQL injection through the workspace ID value.
			if resolved {
				var setCfgResult string
				if err := db.QueryRowContext(c.Request().Context(),
					"SELECT set_config('app.current_workspace_id', $1, true)",
					wsID.String(),
				).Scan(&setCfgResult); err != nil {
					log.Printf("ERROR: failed to set app.current_workspace_id for workspace %s: %v", wsID, err)
					return c.JSON(http.StatusInternalServerError, apierror.InternalError("workspace context unavailable"))
				}
				// Also store in Echo context so RBAC middleware (and handlers) can read it.
				c.Set(ContextKeyWorkspaceID, wsID)

				// Resolve workspace role for the current actor so downstream middleware
				// (e.g., RequireProjectMember) can check it without a second DB query.
				if !IsAgent(c) {
					if userID, err := GetUserID(c); err == nil {
						var role string
						err := db.QueryRowContext(c.Request().Context(),
							"SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2",
							wsID, userID,
						).Scan(&role)
						if err == nil {
							c.Set(ContextKeyWorkspaceRole, role)
						}
					}
				}
			}

			return next(c)
		}
	}
}

// RequireWorkspaceMember returns middleware that enforces workspace membership.
//
// For users: reads workspace_role from Echo context (populated by WorkspaceRLS with no
// extra DB query) — non-empty role means the user is a member.
// For agents: verifies agents.workspace_id matches the requested workspace (one SELECT).
//
// Must run after DualAuth and WorkspaceRLS.
func RequireWorkspaceMember(db *sqlx.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			wsID, err := GetWorkspaceID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}

			if IsAgent(c) {
				agentID, err := GetAgentID(c)
				if err != nil {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
				var agentWsID uuid.UUID
				if err := db.QueryRowContext(c.Request().Context(),
					"SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL",
					agentID,
				).Scan(&agentWsID); err != nil || agentWsID != wsID {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
				return next(c)
			}

			// Users: WorkspaceRLS sets workspace_role only when the user is a member.
			role, ok := c.Get(ContextKeyWorkspaceRole).(string)
			if !ok || role == "" {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			return next(c)
		}
	}
}
