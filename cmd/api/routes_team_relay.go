package main

import (
	"net/http"

	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerTeamRelayRoutes mounts project integrations (Team Relay) and the
// Team Relay document routes.
func registerTeamRelayRoutes(deps *apiDeps) {
	vcsLinkRepo := deps.vcsLinkRepo
	extensionRegistry := deps.extensionRegistry
	projectService := deps.projectService
	projectIntegrationService := deps.projectIntegrationService
	projectIntegrationHandler := deps.projectIntegrationHandler
	trSearchHandler := deps.trSearchHandler
	trDocumentHandler := deps.trDocumentHandler
	trMountHandler := deps.trMountHandler
	api := deps.api
	rbac := deps.rbac
	projAccess := deps.projAccess

	// Project integrations (Team Relay).
	// NOTE: /integrations/team-relay MUST be registered before /integrations to avoid routing ambiguity.
	api.GET("/projects/:proj_id/integrations/team-relay", projectIntegrationHandler.GetTeamRelay, handler.DeprecatedAlias("/api/v1/projects/{p}/extensions/team-relay"), projAccess)
	api.PATCH("/projects/:proj_id/integrations/team-relay", projectIntegrationHandler.UpsertTeamRelay, handler.DeprecatedAlias("/api/v1/projects/{p}/extensions/team-relay"), projAccess, rbac(mw.PermManageWebhooks))
	api.DELETE("/projects/:proj_id/integrations/team-relay", projectIntegrationHandler.DeleteTeamRelay, handler.DeprecatedAlias("/api/v1/projects/{p}/extensions/team-relay"), projAccess, rbac(mw.PermManageWebhooks))
	// Project level of an extension (docs/extensions/api.md, "Project routes"): any
	// extension whose manifest has levels.project; the handler answers 404 for the rest.
	// Team Relay's search and mount stay on their own paths (below) and are also
	// offered as provider actions, so the interface calls one shape for every extension.
	extensionProjectHandler := handler.NewExtensionProjectHandler(extensionRegistry, projectIntegrationService).
		WithProjects(projectService).
		WithRepos(vcsLinkRepo).
		WithActions("team-relay",
			handler.ProjectAction{Name: "search", Method: http.MethodGet, Title: "Search the documents of the bound share", Run: trSearchHandler.Search},
			handler.ProjectAction{Name: "mount", Method: http.MethodPost, Title: "Mount the share into the project Docs now", Run: trMountHandler.Sync},
		)
	api.GET("/projects/:proj_id/extensions", extensionProjectHandler.List, projAccess)
	api.GET("/projects/:proj_id/extensions/:ext_id", extensionProjectHandler.Get, projAccess)
	api.PUT("/projects/:proj_id/extensions/:ext_id", extensionProjectHandler.Put, projAccess, rbac(mw.PermManageWebhooks))
	api.DELETE("/projects/:proj_id/extensions/:ext_id", extensionProjectHandler.Delete, projAccess, rbac(mw.PermManageWebhooks))
	api.GET("/projects/:proj_id/extensions/:ext_id/repos", extensionProjectHandler.Repos, projAccess)
	api.GET("/projects/:proj_id/extensions/:ext_id/actions", extensionProjectHandler.ListActions, projAccess)
	api.GET("/projects/:proj_id/extensions/:ext_id/actions/:action", extensionProjectHandler.Action, projAccess)
	api.POST("/projects/:proj_id/extensions/:ext_id/actions/:action", extensionProjectHandler.Action, projAccess, rbac(mw.PermManageWebhooks))
	api.GET("/projects/:proj_id/integrations", projectIntegrationHandler.List, projAccess)
	// TR file search — share_id (slug) passed as query param, not project path param.
	api.GET("/tr/search", projectIntegrationHandler.TrSearch)

	// TR document search and authenticated preview-url resolution (Team Relay share contents).
	api.GET("/projects/:proj_id/tr/search", trSearchHandler.Search, projAccess)
	// The Team Relay document is read server-side and rendered by our own editor
	// (D10). The route it replaces resolved an iframe src for an embedded
	// TeamRelay page; the iframe, its 6s timeout and its dead end are gone with it.
	api.GET("/projects/:proj_id/tr/document", trDocumentHandler.Get, projAccess)
	// R3: materialize the configured share into this project's Docs tree.
	// Explicit action, not implicit on a tree read — see TrMountHandler's doc
	// comment.
	api.POST("/projects/:proj_id/tr/mount", trMountHandler.Sync, projAccess, rbac(mw.PermManageWebhooks))
}
