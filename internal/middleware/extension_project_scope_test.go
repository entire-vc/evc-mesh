package middleware

import (
	"strings"
	"testing"
)

// extensionBoundDeniedRoutes are mapped extension routes that address projects
// through their body or create one, so a project-bound connection cannot be
// checked on them and is refused (see ExtensionProjectScopeGuard).
var extensionBoundDeniedRoutes = map[string]bool{
	"PATCH /api/v1/vcs-links/ship-sha":        true,
	"POST /api/v1/workspaces/:ws_id/projects": true,
}

// Every route open to extensions must be classified for the project scope:
// project-addressed by a parameter the guard resolves, workspace-level, or
// knowingly refused to a bound connection. A route mapped later without a
// decision fails here instead of silently reaching every project.
func TestExtensionRoutes_ProjectScopeClassified(t *testing.T) {
	params := make([]string, 0, len(requestProjectParams))
	for _, rp := range requestProjectParams {
		params = append(params, ":"+rp.param)
	}
	addressed := func(route string) bool {
		for _, seg := range strings.Split(route, "/") {
			for _, p := range params {
				if seg == p {
					return true
				}
			}
		}
		return false
	}
	routes := ExtensionRouteCapabilities()
	for k := range extensionOpenRoutes {
		routes[k] = "open"
	}
	for route := range routes {
		classes := 0
		if addressed(route) {
			classes++
		}
		if extensionWorkspaceLevelRoutes[route] {
			classes++
		}
		if extensionBoundDeniedRoutes[route] {
			classes++
		}
		if classes != 1 {
			t.Errorf("%s: classified %d times for the extension project scope, want exactly 1 "+
				"(project parameter, extensionWorkspaceLevelRoutes or extensionBoundDeniedRoutes)", route, classes)
		}
	}
	for route := range extensionWorkspaceLevelRoutes {
		if _, ok := routes[route]; !ok {
			t.Errorf("%s is in extensionWorkspaceLevelRoutes but not an extension route", route)
		}
	}
}

func TestIsShortID(t *testing.T) {
	for in, want := range map[string]bool{"abcdef": true, "ABCDEF12": true, "abcde": false, "abcdef1234567": false, "abcdeg": false} {
		if got := isShortID(in); got != want {
			t.Errorf("isShortID(%q) = %v, want %v", in, got, want)
		}
	}
}
