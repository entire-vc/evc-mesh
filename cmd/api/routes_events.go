package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerEventRoutes mounts event bus, webhook, saved view, activity log and
// VCS link routes.
func registerEventRoutes(deps *apiDeps) {
	eventHandler := deps.eventHandler
	activityHandler := deps.activityHandler
	webhookHandler := deps.webhookHandler
	savedViewHandler := deps.savedViewHandler
	vcsLinkHandler := deps.vcsLinkHandler
	api := deps.api
	rbac := deps.rbac
	projAccess := deps.projAccess

	// Event bus routes.
	api.GET("/projects/:proj_id/events", eventHandler.List, projAccess)
	api.POST("/projects/:proj_id/events", eventHandler.Create, projAccess, rbac(mw.PermPublishEvent))
	api.GET("/events/:event_id", eventHandler.GetByID)

	// Webhook routes.
	api.POST("/workspaces/:ws_id/webhooks", webhookHandler.Create, rbac(mw.PermManageWebhooks))
	api.GET("/workspaces/:ws_id/webhooks", webhookHandler.List, rbac(mw.PermManageWebhooks))
	api.GET("/webhooks/:webhook_id", webhookHandler.GetByID, rbac(mw.PermManageWebhooks))
	api.PATCH("/webhooks/:webhook_id", webhookHandler.Update, rbac(mw.PermManageWebhooks))
	api.DELETE("/webhooks/:webhook_id", webhookHandler.Delete, rbac(mw.PermManageWebhooks))
	api.GET("/webhooks/:webhook_id/deliveries", webhookHandler.ListDeliveries, rbac(mw.PermManageWebhooks))
	api.POST("/webhooks/:webhook_id/test", webhookHandler.Test, rbac(mw.PermManageWebhooks))

	// Saved view routes.
	api.GET("/projects/:proj_id/views", savedViewHandler.List, projAccess)
	api.POST("/projects/:proj_id/views", savedViewHandler.Create, projAccess)
	api.GET("/views/:view_id", savedViewHandler.GetByID)
	api.PATCH("/views/:view_id", savedViewHandler.Update)
	api.DELETE("/views/:view_id", savedViewHandler.Delete)

	// Activity log routes.
	api.GET("/workspaces/:ws_id/activity", activityHandler.ListByWorkspace, rbac(mw.PermExportAuditLog))
	api.GET("/workspaces/:ws_id/activity/export", activityHandler.Export, rbac(mw.PermExportAuditLog))
	api.GET("/tasks/:task_id/activity", activityHandler.ListByTask)

	// VCS link routes.
	api.GET("/tasks/:task_id/vcs-links", vcsLinkHandler.List)
	api.POST("/tasks/:task_id/vcs-links", vcsLinkHandler.Create, rbac(mw.PermUpdateTask))
	api.DELETE("/vcs-links/:link_id", vcsLinkHandler.Delete, rbac(mw.PermUpdateTask))
	api.PATCH("/tasks/:task_id/vcs-links/:link_id/metadata", vcsLinkHandler.PatchMetadata, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/vcs-links/:link_id/deployment", vcsLinkHandler.RecordDeployment, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/vcs-links/:link_id/sync", vcsLinkHandler.Sync, rbac(mw.PermUpdateTask))
	api.PATCH("/vcs-links/ship-sha", vcsLinkHandler.PatchShipSHAByURL, rbac(mw.PermUpdateTask))
}
