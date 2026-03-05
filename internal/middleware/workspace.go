package middleware

import (
	"log"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
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
					log.Printf("WARNING: failed to set app.current_workspace_id: %v", err)
					// Non-fatal: continue without RLS context rather than blocking the request.
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
