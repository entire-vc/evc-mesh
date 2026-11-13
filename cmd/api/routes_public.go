package main

import (
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/themes"
	wsHub "github.com/entire-vc/evc-mesh/internal/ws"
)

// registerPublicRoutes mounts the WebSocket endpoint, the /api/v1 group and
// the routes that need no authentication.
func registerPublicRoutes(deps *apiDeps) {
	cfg := deps.cfg
	db := deps.db
	workspaceRepo := deps.workspaceRepo
	workspaceMemberRepo := deps.workspaceMemberRepo
	instanceSettingRepo := deps.instanceSettingRepo
	extensionPolicyRepo := deps.extensionPolicyRepo
	authService := deps.authService
	agentService := deps.agentService
	oauthService := deps.oauthService
	s3Client := deps.s3Client
	authHandler := deps.authHandler
	workspaceHandler := deps.workspaceHandler
	extensionUIHandler := deps.extensionUIHandler
	inviteHandler := deps.inviteHandler
	e := deps.e
	ipTrusted := deps.ipTrusted
	sharedRedis := deps.sharedRedis
	hub := deps.hub

	// WebSocket upgrade endpoint. It sits on the root instance, ahead of the
	// /api/v1 group, so none of the group middleware below (WorkspaceRLS,
	// RequireWorkspaceMemberScoped) can ever run on it: both authentication and
	// the cross-tenant check are the handler's own responsibility. The authorizer
	// is what lets it apply the same membership rule as the REST API instead of a
	// second, divergent copy.
	e.GET("/ws", wsHub.Handler(hub, authService, agentService, wsHub.NewDBAuthorizer(db)))

	// 9. Register all routes.
	v1 := e.Group("/api/v1")

	// JWKS of the instance signing key: public keys only, fetched by external
	// extensions to verify the context token of their iframe screens.
	v1.GET("/.well-known/jwks.json", extensionUIHandler.JWKS)

	// --- Public routes (no auth required) ---

	// register: tight per-IP brute-force/spam protection (5 RPM default).
	// Unlike /login (below), there is no account to key a failure-lockout
	// counter on before one exists — per-IP is the only defense here, so
	// its Enabled flag is left as-is (cfg.RateLimit.Enabled only) rather
	// than additionally gated on trusted-proxy config. On an untrustworthy
	// topology this keeps register's existing (imperfect: shared-bucket)
	// protection rather than leaving it with none at all — a call this
	// card does not need to make, since register brute-forcing isn't the
	// credential-stuffing threat an internal ticket is about.
	registerGroup := v1.Group("/auth")
	registerGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "auth-register",
	}))
	registerGroup.POST("/register", authHandler.Register)

	// login: per-IP limiting here is now a SECONDARY layer, only enabled
	// when the client IP can actually be trusted (ipTrusted, computed at
	// Echo-instance setup above from MESH_TRUSTED_PROXIES). The PRIMARY
	// defense is the per-account failed-login lockout wired into
	// authHandler above (mw.LoginLockout) — it works regardless of
	// reverse-proxy topology, which per-IP limiting fundamentally cannot.
	// Left enabled with an untrustworthy IP, this middleware reproduces the
	// exact defect an internal ticket reports: c.RealIP() resolves to ONE address
	// for all external traffic, so this "per-IP" limiter becomes a single
	// shared 5 RPM bucket for the entire internet — worse than no limiter,
	// since it also denies real users. Disabling it here is what "IP-ключ
	// НЕ включается" (the accepted decision, task comment) means in code.
	loginGroup := v1.Group("/auth")
	loginGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled && ipTrusted,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "auth-login",
	}))
	loginGroup.POST("/login", authHandler.Login)

	// GET /auth/config: unauthenticated, no side effects — the login/register
	// pages poll this once to decide whether to show the "Create an account"
	// link. No rate limit needed (read-only, no brute-force surface).
	v1.GET("/auth/config", authHandler.Config)

	// /auth/refresh: separate, fleet-safe per-IP limit (60 RPM default).
	// Refresh requires a valid refresh token — credential brute-force is not
	// a concern. A fleet of agents on a shared egress IP must all be able to
	// refresh their JWT tokens concurrently without hitting a shared 5-RPM cap.
	refreshGroup := v1.Group("/auth")
	refreshGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.RefreshRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "auth-refresh",
	}))
	refreshGroup.POST("/refresh", authHandler.Refresh)

	// Public invite acceptance endpoints (rate-limited same as auth).
	invitePublicGroup := v1.Group("/invites")
	invitePublicGroup.Use(mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.AuthRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "invite-accept",
	}))
	invitePublicGroup.GET("/:token", inviteHandler.GetByToken)
	invitePublicGroup.POST("/:token/accept", inviteHandler.Accept)

	// Workspace icon (read). Unauthenticated on purpose, and it has to be: the
	// UI renders it as <img src="/api/v1/workspaces/{id}/icon"> and hands the
	// same URL to <link rel=icon>. Neither carries an Authorization header, so
	// behind the authenticated group this endpoint answered every browser with
	// 401 and the icon could never render — the upload was only the first of
	// the two walls.
	//
	// Mesh has no cookie auth — the server never sets one, the token lives only
	// in a header — so there is no way for a browser subresource to
	// authenticate at all. A signed token in the URL was rejected as the
	// alternative: it leaks through logs and Referer headers, for a logo.
	//
	// The exposure is a workspace logo to whoever already knows the workspace
	// UUID, which is only ever handed out in authenticated responses; the UUID
	// is the capability, exactly as it is for the presigned artifact URLs this
	// endpoint used to redirect to (those were unauthenticated too, and for far
	// more sensitive content). A missing workspace and a workspace with no icon
	// answer byte-identically, so this is not an existence oracle either.
	// Uploading (PUT) stays members-only and admin-gated.
	//
	// This exception is registered in tests/integration/cross_tenant_test.go
	// (wsPublicRoutes) and its behaviour is pinned there. Do not add to that
	// list without the same kind of justification.
	//
	// Registered per route with explicit middleware rather than on a
	// v1.Group("/workspaces"): a group would make "which paths are public"
	// depend on which registrations happen to sit under it, and this must be
	// exactly two routes and nothing else. Written with the full literal path
	// so it reads identically to the guarded routes below — the completeness
	// test greps for both forms and fails on any :ws_id route that appears in
	// neither table.
	iconRateLimit := mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.APIRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "workspace-icon",
	})
	// Themes: what a browser paints, and the bundle files it loads. Both are
	// public by design — the sign-in screen is themed before anyone is signed
	// in. GET /theme?workspace=<slug> authenticates itself (OptionalAuth) and
	// answers only members; the bare form carries the instance theme alone.
	themeService := &themes.Service{
		Installed: postgres.NewInstalledExtensionRepo(db),
		Themes:    postgres.NewThemeRepo(db),
		Journal:   extensionPolicyRepo,
		Allow:     extensionPolicyRepo,
	}
	if s3Client != nil {
		themeService.Store = s3Client
	}
	themeHandler := handler.NewThemeHandler(themeService, instanceSettingRepo, workspaceRepo, workspaceMemberRepo)
	themeRateLimit := mw.RateLimit(mw.RateLimitConfig{
		Enabled:     cfg.RateLimit.Enabled,
		RPM:         cfg.RateLimit.APIRPM,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: sharedRedis,
		Name:        "theme",
	})
	v1.GET("/theme", themeHandler.Get, mw.OptionalAuth(authService, agentService, oauthService), themeRateLimit)
	v1.GET("/theme/assets/:file", themeHandler.Asset, themeRateLimit)
	v1.GET("/theme/manifest.json", themeHandler.Manifest, themeRateLimit)

	v1.GET("/workspaces/:ws_id/icon", workspaceHandler.GetIcon, iconRateLimit)
	// HEAD as well: it is what `curl -I` and cache validators send, and Echo
	// does not derive it from the GET route. net/http drops the body itself.
	v1.HEAD("/workspaces/:ws_id/icon", workspaceHandler.GetIcon, iconRateLimit)

	deps.v1 = v1
	deps.themeService = themeService
	deps.themeHandler = themeHandler
}
