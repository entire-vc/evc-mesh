package middleware

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AuthTypeExtension is the principal type of an extension's service actor
// (`ext:<id>@<workspace>`): not a user, not an agent. It authenticates with an
// ext_ bearer token and may only call what its consented capabilities allow.
const AuthTypeExtension = "extension"

// Context keys set by ExtensionAuth.
const (
	// ContextKeyExtensionPrincipal holds the *service.ExtensionPrincipal.
	ContextKeyExtensionPrincipal = "extension_principal"
	// ContextKeyExtensionWorkspaceID is the workspace the token is scoped to. Like
	// ContextKeyAgentAuthWorkspaceID it is set once and never overwritten by the
	// route's own workspace parameter, so "which tenant is this request ABOUT"
	// and "which tenant does this token BELONG to" stay different questions.
	ContextKeyExtensionWorkspaceID = "extension_auth_workspace_id"
)

// IsExtension reports whether the request authenticated as an extension actor.
func IsExtension(c echo.Context) bool {
	v, _ := c.Get(ContextKeyAuthType).(string)
	return v == AuthTypeExtension
}

// GetExtensionPrincipal returns the authenticated extension actor, nil for any
// other kind of request.
func GetExtensionPrincipal(c echo.Context) *service.ExtensionPrincipal {
	p, _ := c.Get(ContextKeyExtensionPrincipal).(*service.ExtensionPrincipal)
	return p
}

// extensionBearerToken returns the ext_ bearer token of the request, if any.
func extensionBearerToken(c echo.Context) (string, bool) {
	header := c.Request().Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(token, service.ExtensionTokenPrefix) {
		return "", false
	}
	return token, true
}

// ExtensionBearerToken returns the raw ext_ token of an authenticated extension
// request. The self-rotation endpoint needs it to keep the presented token as the
// previous one; nothing else should.
func ExtensionBearerToken(c echo.Context) (string, bool) {
	if GetExtensionPrincipal(c) == nil {
		return "", false
	}
	return extensionBearerToken(c)
}

// ExtensionAuth authenticates an `Authorization: Bearer ext_…` token as the
// service actor of an extension connection. It must run BEFORE DualAuth, which
// skips a request already authenticated here. A request without an ext_ token
// passes through untouched; one with an invalid, revoked or disabled token is a
// 401 right here — it never falls through to another credential.
//
// svc nil (never wired) makes this a no-op: ext_ tokens then fail in DualAuth as
// an unparseable JWT, i.e. they are not accepted.
func ExtensionAuth(svc service.ExtensionActorService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if svc == nil {
				return next(c)
			}
			token, ok := extensionBearerToken(c)
			if !ok {
				return next(c)
			}
			p, err := svc.Authenticate(c.Request().Context(), token)
			if err != nil || p == nil {
				return unauthorizedJSON(c, "Invalid extension token")
			}
			c.Set(ContextKeyAuthType, AuthTypeExtension)
			c.Set(ContextKeyExtensionPrincipal, p)
			c.Set(ContextKeyExtensionWorkspaceID, p.WorkspaceID)
			c.Set(ContextKeyWorkspaceID, p.WorkspaceID)

			goCtx := actorctx.WithActor(c.Request().Context(), p.ActorID, domain.ActorTypeExtension)
			name := p.DisplayName
			if name == "" {
				name = p.ExtensionID
			}
			goCtx = actorctx.WithActorName(goCtx, name)
			c.SetRequest(c.Request().WithContext(goCtx))
			return next(c)
		}
	}
}

// extensionOpenRoutes need no capability: every connected extension may ask who
// it is and which public events exist, and read or resend the events of its own
// subscriptions (the handler filters them by capabilities.events at read time).
var extensionOpenRoutes = map[string]bool{
	"GET /api/v1/ext/me":                                true,
	"GET /api/v1/extensions/events":                     true,
	"GET /api/v1/ext/me/events":                         true,
	"POST /api/v1/ext/me/events/:delivery_id/redeliver": true,
}

// extensionRouteCapabilities maps "METHOD route-pattern" (exactly what
// echo.Context.Path() returns) to the `capabilities.api` entry the extension must
// hold. The map is an ALLOW-list: a route that is not in it is not reachable by an
// extension at all, whatever the extension consented to. That is deliberate —
// human-gate decisions, checkout, workspace administration, member changes and
// every route added after this file stay closed until someone maps them on
// purpose. Capability names are the manifest vocabulary
// (internal/extensions/schema/manifest.v1.json).
var extensionRouteCapabilities = map[string]string{
	// workspaces / projects
	"GET /api/v1/workspaces/:ws_id":           "workspaces:read",
	"GET /api/v1/workspaces/:ws_id/projects":  "projects:read",
	"GET /api/v1/projects/:proj_id":           "projects:read",
	"POST /api/v1/workspaces/:ws_id/projects": "projects:write",
	"PATCH /api/v1/projects/:proj_id":         "projects:write",

	// hierarchy export (the handler additionally requires projects:read,
	// members:read and agents:read: it carries all of them)
	"GET /api/v1/workspaces/:ws_id/export/hierarchy": "workspaces:read",

	// members (read only)
	"GET /api/v1/workspaces/:ws_id/members": "members:read",
	"GET /api/v1/projects/:proj_id/members": "members:read",

	// tasks
	"GET /api/v1/projects/:proj_id/tasks":                "tasks:read",
	"GET /api/v1/tasks/by-short-id/:short":               "tasks:read",
	"GET /api/v1/tasks/:task_id":                         "tasks:read",
	"GET /api/v1/tasks/:task_id/subtasks":                "tasks:read",
	"GET /api/v1/tasks/:task_id/dependencies":            "tasks:read",
	"GET /api/v1/tasks/:task_id/statuses":                "tasks:read",
	"GET /api/v1/tasks/:task_id/context":                 "tasks:read",
	"GET /api/v1/tasks/:task_id/activity":                "tasks:read",
	"POST /api/v1/projects/:proj_id/tasks":               "tasks:write",
	"PATCH /api/v1/tasks/:task_id":                       "tasks:write",
	"POST /api/v1/tasks/:task_id/move":                   "tasks:write",
	"POST /api/v1/tasks/:task_id/assign":                 "tasks:write",
	"POST /api/v1/tasks/:task_id/subtasks":               "tasks:write",
	"POST /api/v1/tasks/:task_id/dependencies":           "tasks:write",
	"PATCH /api/v1/tasks/:task_id/dependencies/:dep_id":  "tasks:write",
	"DELETE /api/v1/tasks/:task_id/dependencies/:dep_id": "tasks:write",
	"GET /api/v1/tasks/:task_id/external-ref":            "tasks:read",
	"PUT /api/v1/tasks/:task_id/external-ref":            "tasks:write",
	// Arming only: an extension opens a gate card (Keep). Clearing the gate and
	// deciding it stay human-only, so DELETE …/human-gate is NOT mapped.
	"POST /api/v1/tasks/:task_id/human-gate": "tasks:write",

	// comments
	"GET /api/v1/tasks/:task_id/comments":  "comments:read",
	"POST /api/v1/tasks/:task_id/comments": "comments:write",

	// artifacts
	"GET /api/v1/tasks/:task_id/artifacts":                       "artifacts:read",
	"GET /api/v1/artifacts/:artifact_id":                         "artifacts:read",
	"GET /api/v1/artifacts/:artifact_id/download":                "artifacts:read",
	"GET /api/v1/tasks/:task_id/artifacts/:artifact_id/download": "artifacts:read",
	"POST /api/v1/tasks/:task_id/artifacts":                      "artifacts:write",

	// docs
	"GET /api/v1/projects/:proj_id/documents":           "docs:read",
	"GET /api/v1/projects/:proj_id/documents/search":    "docs:read",
	"GET /api/v1/projects/:proj_id/documents/by-path/*": "docs:read",
	"GET /api/v1/documents/:doc_id":                     "docs:read",
	"GET /api/v1/documents/:doc_id/outline":             "docs:read",
	"GET /api/v1/documents/:doc_id/section":             "docs:read",
	"GET /api/v1/documents/:doc_id/comments":            "docs:read",
	"POST /api/v1/projects/:proj_id/documents":          "docs:write",
	"PATCH /api/v1/documents/:doc_id":                   "docs:write",
	"POST /api/v1/documents/:doc_id/comments":           "docs:write",

	// vcs links
	"GET /api/v1/tasks/:task_id/vcs-links":                      "vcs_links:read",
	"POST /api/v1/tasks/:task_id/vcs-links":                     "vcs_links:write",
	"DELETE /api/v1/vcs-links/:link_id":                         "vcs_links:write",
	"PATCH /api/v1/tasks/:task_id/vcs-links/:link_id/metadata":  "vcs_links:write",
	"POST /api/v1/tasks/:task_id/vcs-links/:link_id/deployment": "vcs_links:write",
	"PATCH /api/v1/vcs-links/ship-sha":                          "vcs_links:write",

	// events
	"GET /api/v1/projects/:proj_id/events":  "events:read",
	"GET /api/v1/events/:event_id":          "events:read",
	"POST /api/v1/projects/:proj_id/events": "events:write",

	// agents
	"GET /api/v1/workspaces/:ws_id/agents":    "agents:read",
	"GET /api/v1/agents/:agent_id":            "agents:read",
	"GET /api/v1/agents/:agent_id/sub-agents": "agents:read",

	// own token (Keep's service account rotates itself): the actor is the caller,
	// there is no id in the path to point at anybody else
	"POST /api/v1/ext/me/token/rotate":     "ext_token:self_rotate",
	"DELETE /api/v1/ext/me/token/previous": "ext_token:self_rotate",

	// agent keys (Keep's rotator): one narrow right for exactly these three calls
	"GET /api/v1/agents/:agent_id/keys":            "agent_key:rotate",
	"POST /api/v1/agents/:agent_id/keys":           "agent_key:rotate",
	"DELETE /api/v1/agents/:agent_id/keys/:key_id": "agent_key:rotate",
}

// ExtensionRouteCapabilities returns a copy of the route → capability map, for
// the test that checks it against the real route table.
func ExtensionRouteCapabilities() map[string]string {
	out := make(map[string]string, len(extensionRouteCapabilities))
	for k, v := range extensionRouteCapabilities {
		out[k] = v
	}
	return out
}

// ExtensionCapabilityGuard enforces `capabilities.api` on every /api/v1 call an
// extension actor makes: the route must be mapped and the capability consented
// to, otherwise 403 and the violation counter of the connection (shown in its
// health) is incremented. Requests of any other principal pass untouched.
//
// Must run after ExtensionAuth/DualAuth. Role middleware further down the chain
// (RequirePermission and friends) steps aside for an extension actor because
// this guard, not a workspace role, is what authorizes it.
func ExtensionCapabilityGuard(svc service.ExtensionActorService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := GetExtensionPrincipal(c)
			if p == nil {
				if IsExtension(c) {
					// Authenticated as an extension without a principal: never allow.
					return c.JSON(http.StatusForbidden, apierror.Forbidden("extension principal missing"))
				}
				return next(c)
			}
			method := c.Request().Method
			if method == http.MethodHead {
				method = http.MethodGet
			}
			key := method + " " + c.Path()
			if extensionOpenRoutes[key] {
				return next(c)
			}
			need, mapped := extensionRouteCapabilities[key]
			if mapped && p.HasAPI(need) {
				return next(c)
			}

			what := key + " is not available to extensions"
			msg := "this route is not available to extensions"
			if mapped {
				what = key + " requires api:" + need
				msg = "extension lacks the consented capability api:" + need
			}
			if svc != nil {
				svc.RecordViolation(c.Request().Context(), p.ConnectionID, what)
			}
			return c.JSON(http.StatusForbidden, apierror.Forbidden(msg))
		}
	}
}
