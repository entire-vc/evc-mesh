package main

import (
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerOAuthRoutes mounts the OAuth 2.0 authorization server and the consent API.
func registerOAuthRoutes(deps *apiDeps) {
	cfg := deps.cfg
	oauthRepo := deps.oauthRepo
	oauthHandler := deps.oauthHandler
	e := deps.e
	ipTrusted := deps.ipTrusted
	sharedRedis := deps.sharedRedis
	api := deps.api

	// OAuth 2.0 Authorization Server (task MCP-OAuth 1/5). Public — no auth:
	// these are the spec-mandated RFC 8414/7591/6749 endpoints an anonymous
	// MCP client (or its browser) hits before it has any credential at all.
	// Task MCP-OAuth 4/5 routes /.well-known/oauth-* and /oauth/* straight to
	// this backend, past the SPA, on the production host — registered here
	// at the bare `e` level (not `api`) for the same reason the webhook
	// routes below are.
	// Each endpoint sits behind its own per-IP limiter — see OAuthRateLimits
	// for the sizing of each budget and why they differ. Same trust gate as
	// /auth/login: without MESH_TRUSTED_PROXIES the per-IP limiters are off
	// and DCR / first-seen CIMD fall back to one global bucket.
	handler.RegisterOAuthPublicRoutes(e, oauthHandler, oauthRepo, oauthRateLimits(cfg, ipTrusted, sharedRedis))

	// Consent + "your connected apps" API — the frontend SPA's own bridge
	// into the flow above (task MCP-OAuth 2/5). User-JWT-only: consenting to
	// or revoking a connector agent's access is a decision only the resource
	// owner makes for themselves, never reachable via an agk_ agent key or a
	// mot_ token this very flow is about to mint (see mw.RequireUserAuth's
	// doc comment).
	api.GET("/oauth/consent", oauthHandler.ConsentInfo, mw.RequireUserAuth())
	api.POST("/oauth/consent", oauthHandler.Decide, mw.RequireUserAuth())
	api.GET("/oauth/grants", oauthHandler.ListGrants, mw.RequireUserAuth())
	api.DELETE("/oauth/grants/:oauth_grant_id", oauthHandler.RevokeGrant, mw.RequireUserAuth())
}
