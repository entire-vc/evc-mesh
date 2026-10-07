package main

import "testing"

// These privileged writes must keep the same tenancy and update permission
// gates as task moves. Merely implementing a handler does not wire the API.
func TestParkedWaitRoutesRequireTaskWriteGates(t *testing.T) {
	routes := parseRouteGates(t)
	for _, path := range []string{"/tasks/:task_id/parked-waits", "/tasks/:task_id/parked-waits/release"} {
		r := findRoute(t, routes, "POST", path)
		if !hasGuard(r, "wsAccess") || !hasGuard(r, "rbac") {
			t.Fatalf("%s missing workspace/update guards: %v", path, r.guards)
		}
	}
	findRoute(t, routes, "PATCH", "/tasks/:task_id") // legacy positive control
	findRoute(t, routes, "POST", "/tasks/:task_id/move")
}
