//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsRoute is one workspace-scoped endpoint as registered in cmd/api/main.go.
// suffix is everything after "/workspaces/:ws_id", with nested params already
// filled in — the membership guard runs before the handler, so whatever those
// nested ids point at never gets looked up.
type wsRoute struct {
	method string
	suffix string // e.g. "/members", "" for the workspace itself
	body   any    // non-nil for methods that need one
}

// dummyUUID stands in for nested path params (:user_id, :invite_id). It must
// parse as a UUID so the request reaches the guard rather than a 400.
const dummyUUID = "00000000-0000-0000-0000-0000000000ff"

// wsScopedRoutes is the full set of /workspaces/:ws_id routes a non-member must
// not be able to reach. TestCrossTenant_RouteTableIsComplete fails if a route is
// added to cmd/api/main.go and not listed here, so the table cannot silently rot.
var wsScopedRoutes = []wsRoute{
	{http.MethodGet, "", nil},
	{http.MethodPatch, "", map[string]string{"name": "PWNED"}},
	{http.MethodDelete, "", nil},
	{http.MethodGet, "/icon", nil},
	{http.MethodPut, "/icon", map[string]string{}},

	{http.MethodGet, "/members", nil},
	{http.MethodGet, "/members/me", nil},
	{http.MethodPost, "/members", map[string]string{"user_id": dummyUUID, "role": "member"}},
	{http.MethodPatch, "/members/" + dummyUUID, map[string]string{"role": "admin"}},
	{http.MethodDelete, "/members/" + dummyUUID, nil},
	{http.MethodGet, "/users/search?q=a", nil},

	{http.MethodPost, "/invites", map[string]string{"email": "intruder@test.mesh.local"}},
	{http.MethodGet, "/invites", nil},
	{http.MethodPost, "/invites/" + dummyUUID + "/resend", map[string]string{}},
	{http.MethodDelete, "/invites/" + dummyUUID, nil},

	{http.MethodGet, "/projects", nil},
	{http.MethodPost, "/projects", map[string]string{"name": "Intruder Project"}},

	{http.MethodGet, "/tasks", nil},

	{http.MethodGet, "/agents", nil},
	{http.MethodPost, "/agents", map[string]string{"name": "intruder-agent"}},
	{http.MethodGet, "/agents/status", nil},

	{http.MethodPost, "/webhooks", map[string]string{"url": "https://example.invalid/hook"}},
	{http.MethodGet, "/webhooks", nil},

	{http.MethodGet, "/activity", nil},
	{http.MethodGet, "/activity/export", nil},

	{http.MethodGet, "/integrations", nil},
	{http.MethodPost, "/integrations", map[string]string{"provider": "github"}},

	{http.MethodGet, "/analytics", nil},
	{http.MethodGet, "/analytics/export", nil},

	{http.MethodPost, "/initiatives", map[string]string{"name": "Intruder Initiative"}},
	{http.MethodGet, "/initiatives", nil},

	{http.MethodGet, "/triage", nil},
	{http.MethodGet, "/team", nil},

	{http.MethodGet, "/rules/assignment", nil},
	{http.MethodPut, "/rules/assignment", map[string]any{"rules": []any{}}},
	{http.MethodGet, "/violations", nil},

	{http.MethodPost, "/config/import", map[string]string{}},
	{http.MethodGet, "/config/export", nil},
	{http.MethodPost, "/team/import", map[string]string{}},

	{http.MethodGet, "/rules/workflow-templates", nil},
	{http.MethodPut, "/rules/workflow-templates", map[string]any{"templates": []any{}}},

	{http.MethodPost, "/rules", map[string]string{"name": "intruder-rule"}},
	{http.MethodGet, "/rules", nil},
	{http.MethodGet, "/rules/effective", nil},

	{http.MethodGet, "/mentionables", nil},
	{http.MethodGet, "/comments/recent", nil},
}

// TestCrossTenant_NonMemberIsRefusedOnEveryWorkspaceRoute is the regression test for the
// cross-tenant hole found by the self-host end-to-end run: only 2 of the 46
// /workspaces/:ws_id routes actually enforced membership. A stranger could read another
// tenant's member emails, team directory, analytics and full YAML config export, and
// PATCH /workspaces/:ws_id — guarded by neither membership nor RBAC — let them rename
// that workspace or change its slug, which breaks every /w/<slug>/... link the team has.
//
// Victim and intruder are two ordinary registered users, each with their own workspace.
// Nothing about the intruder is special: this is what any signed-up stranger could do.
func TestCrossTenant_NonMemberIsRefusedOnEveryWorkspaceRoute(t *testing.T) {
	victim := NewTestEnv(t)
	defer victim.Cleanup(t)
	victim.Register(t, uniqueEmail("xt-victim"), "TestPass123", "Victim Owner")
	victimWS := firstWorkspaceID(t, victim)

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xt-intruder"), "TestPass123", "Intruder")

	for _, r := range wsScopedRoutes {
		name := r.method + " /workspaces/:ws_id" + r.suffix
		t.Run(name, func(t *testing.T) {
			path := fmt.Sprintf("/api/v1/workspaces/%s%s", victimWS, r.suffix)
			resp := intruder.doRequest(t, r.method, path, r.body)
			body := string(intruder.ReadBody(t, resp))

			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"a non-member reached %s (status %d, body %s)", name, resp.StatusCode, body)
			// A leak that answers 200 with an empty list is still a leak of existence,
			// but a 404/500 here would mean the request got past the guard and into the
			// handler — so anything other than 403 is a failure, and we say which.
			assert.NotContains(t, strings.ToLower(body), "@test.mesh.local",
				"%s leaked an email address to a non-member", name)
		})
	}
}

// TestCrossTenant_WorkspaceStillWorksForItsOwner is the other half of the guard: the
// fix must not lock legitimate users out. Every route the intruder was refused, the
// owner must still reach — otherwise the security fix reads as an outage.
func TestCrossTenant_WorkspaceStillWorksForItsOwner(t *testing.T) {
	owner := NewTestEnv(t)
	defer owner.Cleanup(t)
	owner.Register(t, uniqueEmail("xt-owner"), "TestPass123", "Legit Owner")
	wsID := firstWorkspaceID(t, owner)

	// Read-only routes only: this asserts access, not side effects.
	readable := []string{"", "/members", "/members/me", "/projects", "/agents", "/team", "/analytics", "/config/export", "/mentionables"}
	for _, suffix := range readable {
		t.Run("GET /workspaces/:ws_id"+suffix, func(t *testing.T) {
			resp := owner.Get(t, fmt.Sprintf("/api/v1/workspaces/%s%s", wsID, suffix))
			body := string(owner.ReadBody(t, resp))
			assert.Less(t, resp.StatusCode, http.StatusBadRequest,
				"the workspace owner was refused %s (status %d, body %s)", suffix, resp.StatusCode, body)
		})
	}
}

// TestCrossTenant_RouteTableIsComplete keeps the table above honest. It reads the real
// route registrations out of cmd/api/main.go and fails if any /workspaces/:ws_id route
// is missing from wsScopedRoutes.
//
// The guard itself is registered group-wide, so a newly added route is protected whether
// or not anyone updates this file — this test exists so that protection is also proven
// for each new route rather than assumed.
func TestCrossTenant_RouteTableIsComplete(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err, "cannot read route registrations")

	re := regexp.MustCompile(`api\.(GET|POST|PUT|PATCH|DELETE)\("(/workspaces/:ws_id[^"]*)"`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, matches, "found no :ws_id routes — has the registration style changed?")

	covered := make(map[string]bool, len(wsScopedRoutes))
	for _, r := range wsScopedRoutes {
		// Drop the query string; nested ids are normalised back to their param names.
		suffix := r.suffix
		if i := strings.IndexByte(suffix, '?'); i >= 0 {
			suffix = suffix[:i]
		}
		suffix = strings.ReplaceAll(suffix, "/members/"+dummyUUID, "/members/:user_id")
		suffix = strings.ReplaceAll(suffix, "/invites/"+dummyUUID, "/invites/:invite_id")
		covered[r.method+" /workspaces/:ws_id"+suffix] = true
	}

	var missing []string
	for _, m := range matches {
		key := m[1] + " " + m[2]
		if !covered[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	assert.Empty(t, missing,
		"these workspace-scoped routes are not covered by the cross-tenant regression table — "+
			"add them to wsScopedRoutes so a non-member is proven to get 403:\n  %s",
		strings.Join(missing, "\n  "))
}

// firstWorkspaceID returns the id of the workspace created for a freshly registered user.
func firstWorkspaceID(t *testing.T, env *TestEnv) string {
	t.Helper()
	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces, "a newly registered user must own a default workspace")
	id, ok := workspaces[0]["id"].(string)
	require.True(t, ok, "workspace id must be a string")
	return id
}
