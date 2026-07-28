package middleware

import (
	"context"
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

// ContextKeyWorkspaceSource records how WorkspaceRLS arrived at the workspace it
// put in ContextKeyWorkspaceID. The distinction is what makes the group-level
// guard safe: a workspace named by the request (a path parameter) is a claim by
// the caller and has to be checked, while a workspace taken from the caller's own
// credentials is already theirs and checking it would only be tautological.
const ContextKeyWorkspaceSource = "workspace_id_source"

// Values stored under ContextKeyWorkspaceSource.
const (
	// WorkspaceSourceParam — resolved from a route parameter (see WorkspaceScopedParams).
	WorkspaceSourceParam = "param"
	// WorkspaceSourceAuth — taken from the caller's credentials (agent key auth).
	WorkspaceSourceAuth = "auth"
)

// WorkspaceScopedParams is the set of route parameters WorkspaceRLS can resolve a
// tenant from. It is the contract between the resolver below and
// RequireWorkspaceMemberScoped: a route carrying any of them names another
// tenant's data, so the guard must run on it.
//
// TestWorkspaceScopedParams_CoversEveryResolver reads this file and fails if a
// resolver is added to WorkspaceRLS without its parameter being listed here —
// otherwise a new resolver would silently widen the reach of the API without
// widening the guard, which is the exact shape of the bug this file exists to fix.
var WorkspaceScopedParams = []string{
	"ws_id",
	"proj_id",
	"project_id",
	"task_id",
	"artifact_id",
	"agent_id",
	"field_id",
	"init_id",
}

// workspaceScopeExemptRoutes lists routes that carry one of WorkspaceScopedParams
// in their path but whose parameter is not an identifier in this database, so no
// workspace can be resolved from it and requiring one would simply break them.
//
// Keys are Echo route paths (c.Path()), so an entry cannot silently match more
// than the one route it names.
//
// Spark's :agent_id is a catalog id in the external Spark marketplace, not an
// agents.id here. GET returns a public catalog manifest. POST .../install does
// touch a local workspace, but it takes it from the request body, so the guard
// could not see it either way — SparkHandler.Install checks membership and the
// register-agent permission itself.
var workspaceScopeExemptRoutes = map[string]bool{
	"/api/v1/spark/agents/:agent_id":         true,
	"/api/v1/spark/agents/:agent_id/install": true,
}

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
			// Steps 1 through 2e all read a route parameter, so anything they
			// resolve is a workspace the request named; step 3 overrides this.
			source := WorkspaceSourceParam

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

			// 2e. Try field_id route parameter -> look up workspace via custom_field_definitions → projects.
			if !resolved {
				if fieldIDStr := c.Param("field_id"); fieldIDStr != "" {
					if fieldID, err := uuid.Parse(fieldIDStr); err == nil {
						var resolvedWsID uuid.UUID
						err := db.QueryRowContext(c.Request().Context(),
							`SELECT p.workspace_id
							   FROM custom_field_definitions cf
							   JOIN projects p ON cf.project_id = p.id
							  WHERE cf.id = $1`,
							fieldID,
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
					source = WorkspaceSourceAuth
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
				c.Set(ContextKeyWorkspaceSource, source)

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
				if !AgentIsInWorkspace(c.Request().Context(), db, wsID, agentID) {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
				return next(c)
			}

			// Users: WorkspaceRLS sets workspace_role only when the user is a member.
			role, ok := c.Get(ContextKeyWorkspaceRole).(string)
			if ok && role != "" {
				return next(c)
			}

			// Fallback: the workspace owner whose workspace_members row is missing.
			//
			// The owner membership row is inserted best-effort after the workspace
			// itself is created (workspaceService.Create, auth.Service.Register), so a
			// partial failure leaves a workspace whose owner is not one of its members.
			// Such rows exist in the wild — without this branch, turning the guard on
			// would lock those owners out of their own workspace, turning a read-leak
			// fix into a data-loss-shaped outage. One extra query, only on the path
			// that was about to 403 anyway.
			userID, idErr := GetUserID(c)
			if idErr != nil || !UserOwnsWorkspace(c.Request().Context(), db, wsID, userID) {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			return next(c)
		}
	}
}

// UserOwnsWorkspace reports whether userID is the recorded owner of wsID.
//
// It exists as the owner-fallback half of the membership rule (see
// RequireWorkspaceMember) in a form that callers outside the Echo middleware
// chain — the WebSocket endpoint — can use, so the rule is written once.
func UserOwnsWorkspace(ctx context.Context, db *sqlx.DB, wsID, userID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var ownerID uuid.UUID
	if err := db.QueryRowContext(ctx,
		"SELECT owner_id FROM workspaces WHERE id = $1 AND deleted_at IS NULL",
		wsID,
	).Scan(&ownerID); err != nil {
		return false
	}
	return ownerID == userID
}

// UserIsWorkspaceMember reports whether userID may act inside wsID: a
// workspace_members row, or the owner fallback for the workspaces whose owner row
// was never written (see RequireWorkspaceMember for why those exist).
//
// RequireWorkspaceMember does not call this directly because WorkspaceRLS has
// already done the workspace_members lookup for it and stores the result in the
// Echo context; this is the same rule for callers that have no Echo context —
// today, the WebSocket handshake.
func UserIsWorkspaceMember(ctx context.Context, db *sqlx.DB, wsID, userID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var role string
	if err := db.QueryRowContext(ctx,
		"SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2",
		wsID, userID,
	).Scan(&role); err == nil && role != "" {
		return true
	}
	return UserOwnsWorkspace(ctx, db, wsID, userID)
}

// AgentIsInWorkspace reports whether the agent belongs to wsID.
func AgentIsInWorkspace(ctx context.Context, db *sqlx.DB, wsID, agentID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var agentWsID uuid.UUID
	if err := db.QueryRowContext(ctx,
		"SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL",
		agentID,
	).Scan(&agentWsID); err != nil {
		return false
	}
	return agentWsID == wsID
}

// RequireWorkspaceMemberScoped enforces workspace membership on every route whose
// path names another tenant's data — that is, every route carrying one of
// WorkspaceScopedParams — and does nothing on routes that carry none.
//
// It exists because the per-route form could be — and was — forgotten. Only 2 of
// the 46 :ws_id routes actually had the guard attached, which left any logged-in
// stranger able to read another tenant's member emails, team directory, analytics
// and full YAML config export, and — via PATCH /workspaces/:ws_id, which had
// neither this guard nor an RBAC check — to rename another tenant's workspace or
// change its slug, breaking every /w/<slug>/... link that team had.
//
// The first version of this guard keyed on the literal :ws_id parameter, which
// left the same hole one indirection further out: WorkspaceRLS resolves the tenant
// from :proj_id, :task_id, :artifact_id, :agent_id, :field_id and :init_id too, and
// those routes went unchecked. A stranger could read any agent by id and — via
// POST /agents/:agent_id/activity, which had no RBAC bar either — write a forged
// entry into another tenant's agent activity log. So the guard now keys on the
// workspace WorkspaceRLS actually resolved, not on the spelling of one parameter.
//
// Three cases, in order:
//
//  1. No workspace-scoping parameter in the path (/auth/me, /notifications,
//     /memories/:id): nothing to check, pass through.
//  2. A parameter is present and WorkspaceRLS resolved a workspace *from it*:
//     require membership of that workspace.
//  3. A parameter is present and no workspace came out of it: refuse. The
//     workspace in context, if any, is the caller's own (agent key auth), which
//     says nothing about the object they asked for — treating that as permission
//     is how case 2 gets bypassed. This is already how the per-route guard behaved
//     on /tasks/:task_id, so a soft-deleted or unknown id answers 403 rather than
//     404 — the same answer either way, which is also the answer that does not
//     confirm the id exists.
//
// Registered once on the authenticated API group, it covers every current and
// future workspace-scoped route by construction, so a new route cannot silently
// ship unguarded. Must run after DualAuth and WorkspaceRLS.
func RequireWorkspaceMemberScoped(db *sqlx.DB) echo.MiddlewareFunc {
	guard := RequireWorkspaceMember(db)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		guarded := guard(next)
		return func(c echo.Context) error {
			if !routeIsWorkspaceScoped(c) {
				return next(c)
			}
			if workspaceScopeExemptRoutes[c.Path()] {
				return next(c)
			}
			if src, _ := c.Get(ContextKeyWorkspaceSource).(string); src != WorkspaceSourceParam {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			return guarded(c)
		}
	}
}

// routeIsWorkspaceScoped reports whether the request names a tenant-owned object
// through one of the parameters WorkspaceRLS resolves a workspace from.
func routeIsWorkspaceScoped(c echo.Context) bool {
	for _, name := range WorkspaceScopedParams {
		if c.Param(name) != "" {
			return true
		}
	}
	return false
}
