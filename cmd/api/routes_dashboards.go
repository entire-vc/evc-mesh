package main

import (
	"log"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/dashboards"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// registerDashboardRoutes mounts the Dashboards extension (PRD Dashboards
// §6.2): the workspace home screen. Every route is a person's own screen, so
// every route is human-only; tenancy is the group's workspace guard (ws_id,
// and dashboard_id is checked to belong to the workspace), the read / edit /
// manage / share rules are the service's.
//
// "default" in :dashboard_id names the caller's default dashboard, virtual
// until their first change; it is not a uuid, so the pair-param guard leaves
// it to the service, which resolves it per caller.
func registerDashboardRoutes(deps *apiDeps) {
	svc := dashboards.NewService(deps.db, handler.ThemeDashboardSource{Themes: deps.themeService}, deps.wsPublisher).
		WithExtensionWidgets(extensions.NewDashboardWidgets(deps.extensionRegistry, deps.extensionProjectScopeRepo))
	// Leaving the workspace clears the author of the member's shared
	// dashboards in the database; the hook tells the open tabs.
	if r, ok := deps.workspaceMemberService.(interface {
		AddMemberRemovalHook(service.MemberRemovalHook)
	}); ok {
		r.AddMemberRemovalHook(svc)
	} else {
		log.Printf("[dashboards] WARNING: member removal hook not wired; shared dashboards will not announce a departed author")
	}
	// dashboard.widget context tokens name a dashboard and an instance: the UI handler checks
	// both against this service before it signs one.
	if deps.extensionUIHandler != nil {
		deps.extensionUIHandler.WithDashboards(svc)
	}
	h := handler.NewDashboardHandler(svc)
	api := deps.api

	api.GET("/workspaces/:ws_id/dashboard-widgets", h.Widgets, mw.RequireUserAuth())
	api.GET("/workspaces/:ws_id/dashboard-templates", h.Templates, mw.RequireUserAuth())

	api.GET("/workspaces/:ws_id/dashboards", h.List, mw.RequireUserAuth())
	api.POST("/workspaces/:ws_id/dashboards", h.Create, mw.RequireUserAuth())
	api.PUT("/workspaces/:ws_id/dashboards/order", h.Order, mw.RequireUserAuth())
	api.GET("/workspaces/:ws_id/dashboards/:dashboard_id", h.Get, mw.RequireUserAuth())
	api.PATCH("/workspaces/:ws_id/dashboards/:dashboard_id", h.Patch, mw.RequireUserAuth())
	api.PATCH("/workspaces/:ws_id/dashboards/:dashboard_id/sharing", h.Sharing, mw.RequireUserAuth())
	api.DELETE("/workspaces/:ws_id/dashboards/:dashboard_id", h.Delete, mw.RequireUserAuth())
	api.PUT("/workspaces/:ws_id/dashboards/:dashboard_id/layout", h.PutLayout, mw.RequireUserAuth())
	api.PUT("/workspaces/:ws_id/dashboards/:dashboard_id/hidden", h.SetHidden, mw.RequireUserAuth())
	api.POST("/workspaces/:ws_id/dashboards/:dashboard_id/duplicate", h.Duplicate, mw.RequireUserAuth())
	// /copy is §6.2's name of /duplicate; both take {name?, placement?}.
	api.POST("/workspaces/:ws_id/dashboards/:dashboard_id/copy", h.Duplicate, mw.RequireUserAuth())
	api.POST("/workspaces/:ws_id/dashboards/:dashboard_id/reset", h.Reset, mw.RequireUserAuth())
}

// The dashboards service is the member removal hook registered above.
var _ service.MemberRemovalHook = (*dashboards.Service)(nil)
