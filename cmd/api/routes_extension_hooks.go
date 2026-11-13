package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerExtensionHookRoutes mounts instance extension settings, the routes
// extensions contribute and the GitHub/GitLab webhook receivers.
func registerExtensionHookRoutes(deps *apiDeps) {
	extensionRegistry := deps.extensionRegistry
	extensionInstanceSettingsHandler := deps.extensionInstanceSettingsHandler
	extensionInstanceAdminHandler := deps.extensionInstanceAdminHandler
	githubExt := deps.githubExt
	gitlabExt := deps.gitlabExt
	vcsLinkHandler := deps.vcsLinkHandler
	e := deps.e
	api := deps.api

	// Instance-level extension settings: human instance administrators only
	// (the handler checks the role; RequireUserAuth refuses agents/connectors).
	api.GET("/extensions/:ext_id/instance-settings", extensionInstanceSettingsHandler.Get, mw.RequireUserAuth())
	api.PUT("/extensions/:ext_id/instance-settings", extensionInstanceSettingsHandler.Put, mw.RequireUserAuth())
	api.GET("/extensions/instance/overview", extensionInstanceAdminHandler.Overview, mw.RequireUserAuth())
	api.GET("/extensions/instance/journal", extensionInstanceAdminHandler.Journal, mw.RequireUserAuth())
	api.PUT("/extensions/instance/auto-update", extensionInstanceAdminHandler.SetAutoUpdate, mw.RequireUserAuth())
	api.PUT("/extensions/:ext_id/allowed", extensionInstanceAdminHandler.SetAllowed, mw.RequireUserAuth())

	// Routes contributed by registered extensions (Hooks()). GitHub's webhook
	// route is canonical at /webhooks/github/github; the two older paths below
	// stay registered as aliases of the same handler.
	githubExt.WebhookHandler = vcsLinkHandler.GitHubWebhook
	gitlabExt.WebhookHandler = vcsLinkHandler.GitLabWebhook
	extensionRegistry.RegisterRoutes(e)

	// GitHub webhook receiver (public — no auth, HMAC validated when configured).
	// Two equivalent routes: the legacy /webhooks/github path that existing repos
	// are configured against, and the canonical /api/v1/integrations/github/webhook
	// alias documented in the integrations UI.
	e.POST("/webhooks/github", vcsLinkHandler.GitHubWebhook)
	e.POST("/api/v1/integrations/github/webhook", vcsLinkHandler.GitHubWebhook)

	// GitLab webhook receiver (public — no auth, X-Gitlab-Token validated
	// when configured). Canonical route is /webhooks/gitlab/gitlab (extension
	// Hooks); /webhooks/gitlab is what every configured project points at and
	// stays registered as an alias of the same handler.
	e.POST("/webhooks/gitlab", vcsLinkHandler.GitLabWebhook)
}
