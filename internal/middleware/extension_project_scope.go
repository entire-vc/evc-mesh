package middleware

import (
	"log"
	"net/http"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// extensionWorkspaceLevelRoutes are the extension routes that address no
// project and stay open to a project-bound connection: who it is, the
// workspace itself, its people and agents, its own token. The project list and
// the hierarchy export are here too; their handlers narrow the projects they
// return to the bound ones. The event feed and redelivery address a delivery,
// not a project: the service checks every event's project against the scope
// (and hides projectless events from a bound connection).
//
// Every route an extension can reach is either project-addressed (a parameter
// RequestProjects resolves) or listed here. Anything else — a new route, or one
// addressing projects through its body like PATCH /vcs-links/ship-sha — is
// refused to a bound connection, and TestExtensionRoutes_ProjectScopeClassified
// fails until it is classified.
var extensionWorkspaceLevelRoutes = map[string]bool{
	"GET /api/v1/ext/me":                                true,
	"GET /api/v1/ext/me/events":                         true,
	"POST /api/v1/ext/me/events/:delivery_id/redeliver": true,
	"GET /api/v1/extensions/events":                     true,
	"GET /api/v1/workspaces/:ws_id":                     true,
	"GET /api/v1/workspaces/:ws_id/projects":            true,
	"GET /api/v1/workspaces/:ws_id/export/hierarchy":    true,
	"GET /api/v1/workspaces/:ws_id/members":             true,
	"GET /api/v1/workspaces/:ws_id/agents":              true,
	"GET /api/v1/agents/:agent_id":                      true,
	"GET /api/v1/agents/:agent_id/sub-agents":           true,
	"POST /api/v1/ext/me/token/rotate":                  true,
	"DELETE /api/v1/ext/me/token/previous":              true,
	"GET /api/v1/agents/:agent_id/keys":                 true,
	"POST /api/v1/agents/:agent_id/keys":                true,
	"DELETE /api/v1/agents/:agent_id/keys/:key_id":      true,
}

// ExtensionWorkspaceLevelRoutes returns a copy of the workspace-level route set,
// for the classification test.
func ExtensionWorkspaceLevelRoutes() map[string]bool {
	out := make(map[string]bool, len(extensionWorkspaceLevelRoutes))
	for k, v := range extensionWorkspaceLevelRoutes {
		out[k] = v
	}
	return out
}

// ExtensionProjectScopeGuard limits an extension actor whose connection is
// project-bound (integration_configs.project_scope = 'bound') to the projects
// with an enabled binding for its extension. Every project the route addresses
// (:proj_id, :task_id, :short, :doc_id, :artifact_id, :event_id, :link_id) must
// be allowed by scope.AllowsProject, otherwise 403 and a violation is recorded
// on the connection. A missing or ambiguous id is the same 403: the answer does
// not confirm whether a row exists in a project the extension cannot see.
//
// A workspace-scoped connection and every non-extension request pass untouched.
// Must run after ExtensionCapabilityGuard (route matched, capability consented).
func ExtensionProjectScopeGuard(db *sqlx.DB, scope repository.ExtensionProjectScopeRepository, svc service.ExtensionActorService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := GetExtensionPrincipal(c)
			if p == nil || !p.ProjectBound {
				return next(c)
			}
			ctx := c.Request().Context()
			method := c.Request().Method
			if method == http.MethodHead {
				method = http.MethodGet
			}
			key := method + " " + c.Path()
			deny := func(what string) error {
				if svc != nil {
					svc.RecordViolation(ctx, p.ConnectionID, what)
				}
				return c.JSON(http.StatusForbidden, apierror.Forbidden("this project is not available to the extension"))
			}

			projects, found, err := RequestProjects(ctx, db, c)
			if err != nil {
				if isNoProject(err) {
					return deny(key + " addresses no project bound to the extension")
				}
				log.Printf("[ext-scope] resolve project for %s: %v", key, err)
				return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to check the extension project scope"))
			}
			if !found {
				if extensionWorkspaceLevelRoutes[key] {
					return next(c)
				}
				return deny(key + " is not available to a project-bound extension")
			}
			for _, pid := range projects {
				ok, aerr := scope.AllowsProject(ctx, p.ConnectionID, pid)
				if aerr != nil {
					log.Printf("[ext-scope] check project %s for connection %s: %v", pid, p.ConnectionID, aerr)
					return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to check the extension project scope"))
				}
				if !ok {
					return deny(key + " addresses project " + pid.String() + " which is not bound to the extension")
				}
			}
			return next(c)
		}
	}
}
