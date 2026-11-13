package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerGovernanceRoutes mounts governance and auto-transition rules,
// notifications and Web Push subscriptions.
func registerGovernanceRoutes(deps *apiDeps) {
	ruleHandler := deps.ruleHandler
	notificationHandler := deps.notificationHandler
	pushSubscriptionHandler := deps.pushSubscriptionHandler
	autoTransHandler := deps.autoTransHandler
	api := deps.api
	rbac := deps.rbac
	projAccess := deps.projAccess
	bodyWS := deps.bodyWS

	// Governance rule routes.
	api.POST("/workspaces/:ws_id/rules", ruleHandler.CreateWorkspaceRule, rbac(mw.PermManageRules))
	api.GET("/workspaces/:ws_id/rules", ruleHandler.ListWorkspaceRules)
	api.GET("/workspaces/:ws_id/rules/effective", ruleHandler.GetWorkspaceEffectiveRules)
	api.POST("/projects/:proj_id/rules", ruleHandler.CreateProjectRule, projAccess, rbac(mw.PermManageRules))
	api.GET("/projects/:proj_id/rules", ruleHandler.ListProjectRules, projAccess)
	api.GET("/projects/:proj_id/rules/effective", ruleHandler.GetProjectEffectiveRules, projAccess)
	api.GET("/rules/:rule_id", ruleHandler.GetRule)
	api.PATCH("/rules/:rule_id", ruleHandler.UpdateRule, rbac(mw.PermManageRules))
	api.DELETE("/rules/:rule_id", ruleHandler.DeleteRule, rbac(mw.PermManageRules))
	// The tenant of this one is in the body, not the path: bodyWS is what checks it.
	// Without it any authenticated caller could paste another workspace's id in and
	// read that tenant's rule names and violation messages back.
	api.POST("/rules/evaluate", ruleHandler.EvaluateRules, bodyWS)

	// Auto-transition rule routes.
	api.GET("/projects/:proj_id/auto-transition-rules", autoTransHandler.List, projAccess)
	api.POST("/projects/:proj_id/auto-transition-rules", autoTransHandler.Create, projAccess, rbac(mw.PermManageRules))
	api.PUT("/projects/:proj_id/auto-transition-rules/:atr_id", autoTransHandler.Update, projAccess, rbac(mw.PermManageRules))
	api.DELETE("/projects/:proj_id/auto-transition-rules/:atr_id", autoTransHandler.Delete, projAccess, rbac(mw.PermManageRules))

	// Notification routes.
	api.GET("/notifications", notificationHandler.List)
	api.POST("/notifications/mark-read", notificationHandler.MarkRead)
	api.GET("/notifications/preferences", notificationHandler.GetPreferences)
	api.GET("/notifications/email-availability", notificationHandler.GetEmailAvailability)
	// requireWorkspaceMember (called inside the handler) is the tenancy guard
	// here — see declaredQueryTenantParams in internal/middleware for why a
	// query-string workspace_id needs one of its own.
	api.GET("/notifications/telegram-bot-info", notificationHandler.GetTelegramBotInfo)
	// bodyWS: the workspace being subscribed to is named in the request body, so
	// RequireWorkspaceMemberScoped sees a path with nothing in it to resolve and
	// waves it through. Without this guard any authenticated caller could
	// subscribe to any workspace and be delivered its comment bodies.
	api.PUT("/notifications/preferences", notificationHandler.UpdatePreferences, bodyWS)
	api.DELETE("/notifications/preferences/:pref_id", notificationHandler.DeletePreference)

	// Web Push subscription routes.
	// NOTE: /me/push-subscriptions/vapid-key MUST be before /me/push-subscriptions to avoid routing conflict.
	api.GET("/me/push-subscriptions/vapid-key", pushSubscriptionHandler.GetVAPIDKey)
	api.GET("/me/push-subscriptions", pushSubscriptionHandler.List)
	api.POST("/me/push-subscriptions", pushSubscriptionHandler.Subscribe)
	api.DELETE("/me/push-subscriptions", pushSubscriptionHandler.Unsubscribe)
}
