package main

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerProtectedGroup builds the authenticated api group with its
// middleware chain and the shared route guards (rbac, connectorRBAC,
// runtimeCatalogRBAC). Every module after it adds routes to that group.
func registerProtectedGroup(deps *apiDeps) {
	cfg := deps.cfg
	db := deps.db
	projectRepo := deps.projectRepo
	activityLogRepo := deps.activityLogRepo
	workspaceMemberRepo := deps.workspaceMemberRepo
	authService := deps.authService
	extensionProjectScopeRepo := deps.extensionProjectScopeRepo
	agentService := deps.agentService
	extensionActorService := deps.extensionActorService
	oauthService := deps.oauthService
	chatSessionService := deps.chatSessionService
	authHandler := deps.authHandler
	sharedRedis := deps.sharedRedis
	activityTracker := deps.activityTracker
	toolBreakdownTracker := deps.toolBreakdownTracker
	v1 := deps.v1

	// --- Protected routes (JWT or Agent Key) ---
	api := v1.Group("")
	// ExtensionAuth first: an ext_ token is resolved here (invalid one = 401, no
	// fall-through) and DualAuth then skips the request.
	// The Keep metadata probe answers in its own closed error envelope, so this is
	// first: it must see the 401/403/404/429 the guards below produce.
	api.Use(mw.KeepMetadataEnvelope())
	api.Use(mw.ExtensionAuth(extensionActorService))
	// A cst_ chat-session token is resolved here too (invalid = 401, no
	// fall-through) and DualAuth then skips the request.
	api.Use(mw.ChatSessionAuth(chatSessionService))
	api.Use(mw.DualAuth(authService, agentService, oauthService))
	// A viewer-role agent credential is read-only: every non-GET/HEAD request is
	// refused here, once for the whole group, including routes with no rbac().
	api.Use(mw.AgentReadOnlyGuard())
	// A refused chat-token call leaves an audit row (it changes nothing else).
	api.Use(mw.ChatSessionDenialAudit(activityLogRepo))
	// Deny-by-default route -> capability check for extension actors; inert for
	// every other principal.
	api.Use(mw.ExtensionCapabilityGuard(extensionActorService))
	// A project-bound extension connection reaches only its bound projects;
	// inert for workspace-scoped connections and every other principal.
	api.Use(mw.ExtensionProjectScopeGuard(db, extensionProjectScopeRepo, extensionActorService))
	api.Use(activityTracker.Middleware())
	api.Use(toolBreakdownTracker.Middleware())
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	// Cross-tenant guard. Applies to every route in this group that carries a
	// :ws_id parameter, and is inert on the rest. Group-level on purpose: the
	// per-route form was attached to 2 of 46 :ws_id routes, and the 44 that were
	// missed leaked member emails, the team directory, analytics and the full
	// workspace config export to any logged-in stranger — and let one rename
	// another tenant's workspace. Placed here, a newly added :ws_id route is
	// guarded by construction rather than by the author remembering.
	// Must run after WorkspaceRLS, which resolves and stores the workspace role.
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	// Project membership for every route naming a task or an object of one. Those
	// routes have no :proj_id, so without this a workspace member outside the
	// project read and edited its tasks by id. Group-level for the same reason as
	// the workspace guard above. Must run after it: it relies on the workspace
	// role WorkspaceRLS resolved and on the 404 for a task that does not exist.
	api.Use(mw.RequireTaskProjectMember(db))
	// A chat token reaches only its own project; refuses what it cannot place.
	// After the workspace guards: it is a narrowing, never a substitute for them.
	api.Use(mw.ChatSessionProjectScope(db))
	// Rate-limit API endpoints by authenticated actor (user/agent ID).
	// Uses the Redis-backed sliding window limiter for multi-instance deployments.
	api.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.APIRPM,
		KeyFunc:     mw.RateLimitKeyByActor,
		RedisClient: sharedRedis,
		Name:        "api",
	}))

	// Auth - protected.
	api.GET("/auth/me", authHandler.Me)
	api.PATCH("/auth/me", authHandler.UpdateMe)
	api.GET("/auth/check-username", authHandler.CheckUsername)
	api.POST("/auth/logout", authHandler.Logout)

	// rbac is a shorthand helper to create per-route RBAC middleware.
	rbac := func(perm mw.Permission) echo.MiddlewareFunc {
		return mw.RequirePermission(perm, workspaceMemberRepo)
	}

	// connectorRBAC is the role bar for a route that has no rbac(): it clamps an
	// OAuth connector (mot_) to its consenting user's current workspace role and
	// is a no-op for humans and X-Agent-Key agents, whose access on those routes
	// is deliberately unchanged. See mw.RequireConnectorPermission for why this
	// is a separate middleware rather than rbac(). A mutating route needs one of
	// rbac(), connectorRBAC(), connectorSelfOrRBAC(), mw.RequireUserAuth(), or an
	// entry in the allow-list of cmd/api/rbac_routes_audit_test.go.
	ownsWorkspace := func(ctx context.Context, wsID, userID uuid.UUID) bool {
		return mw.UserOwnsWorkspace(ctx, db, wsID, userID)
	}
	connectorRBAC := func(perm mw.Permission) echo.MiddlewareFunc {
		return mw.RequireConnectorPermission(perm, workspaceMemberRepo, ownsWorkspace)
	}
	connectorSelfOrRBAC := func(agentIDParam string, perm mw.Permission) echo.MiddlewareFunc {
		return mw.RequireConnectorSelfOrPermission(agentIDParam, perm, workspaceMemberRepo, ownsWorkspace)
	}

	// runtimeCatalogRBAC is rbac(PermManageWebhooks), except that a plain agent key
	// goes on to RuntimeRepo, which admits it only under its operator's
	// runtime_manager grant.
	runtimeCatalogRBAC := mw.RequireRuntimeCatalogPermission(mw.PermManageWebhooks, workspaceMemberRepo)

	deps.api = api
	deps.rbac = rbac
	deps.connectorRBAC = connectorRBAC
	deps.connectorSelfOrRBAC = connectorSelfOrRBAC
	deps.runtimeCatalogRBAC = runtimeCatalogRBAC
}
