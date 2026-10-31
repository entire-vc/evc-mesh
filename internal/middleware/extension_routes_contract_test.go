package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/examples/external-extension/refext"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Contract tests for the API surface of the extension actor (ext_ token):
// which routes it reaches and under which capability. External extensions are
// built against this table; docs/extensions/extension-actor-contract.md is
// the policy. A route may be added, never removed or moved to another
// capability without the ADR and version bump from api-version.md. Never edit
// the pinned table to match a change without one of the two.
//
// Run all of them with: go test ./internal/... -run Contract

// pinnedExtensionRoutes is the extension route table as published with
// extensions API version 1.
var pinnedExtensionRoutes = map[string]string{
	"GET /api/v1/workspaces/:ws_id":                  "workspaces:read",
	"GET /api/v1/workspaces/:ws_id/projects":         "projects:read",
	"GET /api/v1/projects/:proj_id":                  "projects:read",
	"POST /api/v1/workspaces/:ws_id/projects":        "projects:write",
	"PATCH /api/v1/projects/:proj_id":                "projects:write",
	"GET /api/v1/workspaces/:ws_id/export/hierarchy": "workspaces:read",

	"GET /api/v1/workspaces/:ws_id/members": "members:read",
	"GET /api/v1/projects/:proj_id/members": "members:read",

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
	"POST /api/v1/tasks/:task_id/human-gate":             "tasks:write",

	"GET /api/v1/tasks/:task_id/comments":  "comments:read",
	"POST /api/v1/tasks/:task_id/comments": "comments:write",

	"GET /api/v1/tasks/:task_id/artifacts":                       "artifacts:read",
	"GET /api/v1/artifacts/:artifact_id":                         "artifacts:read",
	"GET /api/v1/artifacts/:artifact_id/download":                "artifacts:read",
	"GET /api/v1/tasks/:task_id/artifacts/:artifact_id/download": "artifacts:read",
	"POST /api/v1/tasks/:task_id/artifacts":                      "artifacts:write",

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

	"GET /api/v1/tasks/:task_id/vcs-links":                      "vcs_links:read",
	"POST /api/v1/tasks/:task_id/vcs-links":                     "vcs_links:write",
	"DELETE /api/v1/vcs-links/:link_id":                         "vcs_links:write",
	"PATCH /api/v1/tasks/:task_id/vcs-links/:link_id/metadata":  "vcs_links:write",
	"POST /api/v1/tasks/:task_id/vcs-links/:link_id/deployment": "vcs_links:write",
	"PATCH /api/v1/vcs-links/ship-sha":                          "vcs_links:write",

	"GET /api/v1/projects/:proj_id/events":  "events:read",
	"GET /api/v1/events/:event_id":          "events:read",
	"POST /api/v1/projects/:proj_id/events": "events:write",

	"GET /api/v1/workspaces/:ws_id/agents":    "agents:read",
	"GET /api/v1/agents/:agent_id":            "agents:read",
	"GET /api/v1/agents/:agent_id/sub-agents": "agents:read",

	"POST /api/v1/ext/me/token/rotate":     "ext_token:self_rotate",
	"DELETE /api/v1/ext/me/token/previous": "ext_token:self_rotate",

	"GET /api/v1/agents/:agent_id/keys":            "agent_key:rotate",
	"POST /api/v1/agents/:agent_id/keys":           "agent_key:rotate",
	"DELETE /api/v1/agents/:agent_id/keys/:key_id": "agent_key:rotate",
}

// pinnedOpenRoutes need no capability.
var pinnedOpenRoutes = []string{"GET /api/v1/ext/me", "GET /api/v1/extensions/events"}

func TestContract_ExtensionRoutes_PinnedTableKept(t *testing.T) {
	got := ExtensionRouteCapabilities()
	for route, capability := range pinnedExtensionRoutes {
		have, ok := got[route]
		if assert.True(t, ok, "route %q was removed from the extension table: breaking", route) {
			assert.Equal(t, capability, have, "route %q moved to another capability: breaking", route)
		}
	}
	for _, route := range pinnedOpenRoutes {
		assert.True(t, extensionOpenRoutes[route], "open route %q closed: breaking", route)
	}
}

// Closed by design: an extension never decides a gate, takes a checkout or
// administers a workspace. Opening one of these is a security decision, not a
// table edit.
func TestContract_ExtensionRoutes_StayClosed(t *testing.T) {
	got := ExtensionRouteCapabilities()
	for _, route := range []string{
		"DELETE /api/v1/tasks/:task_id/human-gate",
		"POST /api/v1/tasks/:task_id/checkout",
		"DELETE /api/v1/tasks/:task_id/checkout",
		"POST /api/v1/workspaces/:ws_id/members",
		"POST /api/v1/integrations/:int_id/rotate-token",
	} {
		_, mapped := got[route]
		assert.False(t, mapped, "%s must stay closed to extensions", route)
		assert.False(t, extensionOpenRoutes[route], "%s must stay closed to extensions", route)
	}
}

// Every call the reference extension makes is open to it under a capability
// its manifest declares: a partner copying it gets no 403.
func TestContract_ExtensionRoutes_ReferenceExtensionReachesItsRoutes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "external-extension", "mesh-extension.json"))
	require.NoError(t, err)
	var m struct {
		Capabilities struct {
			API []string `json:"api"`
		} `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(raw, &m))
	p := &service.ExtensionPrincipal{Capabilities: map[string][]string{"api": m.Capabilities.API}}

	got := ExtensionRouteCapabilities()
	require.NotEmpty(t, refext.APIRoutes)
	for _, route := range refext.APIRoutes {
		if extensionOpenRoutes[route] {
			continue
		}
		need, mapped := got[route]
		if assert.True(t, mapped, "reference extension calls %s, which extensions cannot reach", route) {
			assert.True(t, p.HasAPI(need), "%s needs api:%s, not in the reference manifest", route, need)
		}
	}
}

// The guard's answers an extension branches on: 403 for a route outside the
// table and for a mapped route without consent; the handler for the rest.
func TestContract_ExtensionGuard_Answers(t *testing.T) {
	guard := ExtensionCapabilityGuard(nil)
	call := func(method, path string, api ...string) int {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(method, "/", http.NoBody), rec)
		c.SetPath(path)
		c.Set(ContextKeyAuthType, AuthTypeExtension)
		c.Set(ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{Capabilities: map[string][]string{"api": api}})
		require.NoError(t, guard(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })(c))
		return rec.Code
	}

	assert.Equal(t, http.StatusNoContent, call(http.MethodGet, "/api/v1/ext/me"), "open route")
	assert.Equal(t, http.StatusNoContent, call(http.MethodGet, "/api/v1/tasks/:task_id", "tasks:read"))
	assert.Equal(t, http.StatusNoContent, call(http.MethodHead, "/api/v1/tasks/:task_id", "tasks:read"), "HEAD is checked as GET")
	assert.Equal(t, http.StatusForbidden, call(http.MethodPatch, "/api/v1/tasks/:task_id", "tasks:read"), "mapped, not consented")
	assert.Equal(t, http.StatusForbidden, call(http.MethodPost, "/api/v1/tasks/:task_id/checkout", "tasks:write"), "not in the table")
}
