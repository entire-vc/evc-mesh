package middleware

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is about the half of the tenancy invariant that detection alone does
// not cover: the verdict.
//
// TestBodyTenantFieldsAreDeclared finds every tenant-shaped id that arrives in a
// request body and requires each one to be written down. It found
// updatePreferencesRequest.workspace_id — PUT /notifications/preferences — on the
// first pass, and the entry that was written down for it read:
//
//	"benign: writes a preference row keyed on (workspace_id, the caller's own user_id)"
//
// which is a true sentence and the wrong conclusion. The request returns nothing
// belonging to the other tenant, so read across the response it does look
// harmless. What it does instead is write a subscription, and the notification
// dispatcher later reads that subscription and sends the workspace's comment
// bodies to whoever the row names. The leak is asynchronous — the exploit is the
// write, and the payload arrives later through a different code path — so
// "returns nothing" was never the question to ask.
//
// So the test caught the field and the reviewer let it through. A declaration
// mechanism whose only requirement is that somebody typed something cannot tell a
// reviewed field from a rationalized one, and the two tests below are what make
// the verdict itself checkable rather than prose:
//
//   - a workspace_id in a request body has no benign case. It IS the tenant, not
//     a reference to something inside one, and every route that takes one must
//     enforce membership.
//   - a verdict that claims a route is guarded has to be a route that carries the
//     guard, read back out of the router.

// requestBodyTenantField is the json field name that names the tenant itself. A
// project_id or task_id can genuinely be an inert reference on a row already
// pinned to a checked tenant; a workspace_id cannot — it is the pin.
const requestBodyTenantField = "workspace_id"

// enforcementVerdictPrefixes are the verdicts available for a workspace_id in a
// request body.
//
// "guarded:" — middleware refuses a non-member before the handler runs.
// "checked:" — the handler, or the service behind it, checks the caller itself.
// "response:" — the struct is never bound from a request at all, so no caller
// supplies this field. Only for structs that are demonstrably response-only.
//
// "benign:" is deliberately not among them.
var enforcementVerdictPrefixes = []string{"guarded:", "checked:", "response:"}

// bodyWSRouteVerdict matches the strict form a guarded verdict has to take, so
// that the claim names a route this test can go and verify.
var bodyWSRouteVerdict = regexp.MustCompile(`^guarded: bodyWS on (GET|POST|PUT|PATCH|DELETE) (/\S+)$`)

// TestAWorkspaceIDInARequestBodyIsNeverBenign is the invariant that would have
// stopped PUT /notifications/preferences from shipping.
//
// The route bound a workspace_id out of the body, carried no membership check of
// any kind, and was declared benign on the grounds that it only wrote a row. Any
// authenticated user could therefore subscribe themselves to any workspace, and
// the dispatcher delivered that workspace's comment bodies to them from then on —
// with TestBodyTenantFieldsAreDeclared green, because the field was declared.
//
// Whether a request reads or writes is not what decides this. A workspace_id from
// a caller is a claim about which tenant they are acting in, and the only two
// answers are that something enforced it or something checked it.
func TestAWorkspaceIDInARequestBodyIsNeverBenign(t *testing.T) {
	found := bodyTenantFields(t)
	require.NotEmpty(t, found, "no request-body tenant fields discovered — has the struct style changed?")

	var unenforced []string
	for _, f := range found {
		if !strings.HasSuffix(f, "."+requestBodyTenantField) {
			continue
		}
		verdict, declared := declaredBodyTenantFields[f]
		if !declared {
			// TestBodyTenantFieldsAreDeclared reports this one; not our failure.
			continue
		}
		if !verdictEnforcesMembership(verdict) {
			unenforced = append(unenforced, fmt.Sprintf("%s — %q", f, verdict))
		}
	}
	sort.Strings(unenforced)

	assert.Empty(t, unenforced,
		"a workspace_id in a request body is the caller naming a tenant, and there is no benign "+
			"way to take one: even a request that returns nothing can write a row another subsystem "+
			"later reads and acts on, which is exactly how PUT /notifications/preferences leaked "+
			"comment bodies. Guard the route with middleware.RequireBodyWorkspace or check the "+
			"caller in the handler, and declare it as %v:\n  %s",
		enforcementVerdictPrefixes, strings.Join(unenforced, "\n  "))
}

// TestBodyWorkspaceIDGuardsAreWiredToTheirRoutes stops a verdict from being the
// only thing that is true.
//
// "guarded: bodyWS on PUT /notifications/preferences" is a claim about the router,
// and a claim about the router is worth what reading the router says it is worth.
// Deleting the middleware argument from a route registration is a one-token edit
// that leaves every declaration in this package still saying the route is guarded.
func TestBodyWorkspaceIDGuardsAreWiredToTheirRoutes(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err, "cannot read route registrations")
	lines := strings.Split(string(src), "\n")

	checked := 0
	for field, verdict := range declaredBodyTenantFields {
		if !strings.HasPrefix(verdict, "guarded: bodyWS on ") {
			continue
		}
		m := bodyWSRouteVerdict.FindStringSubmatch(verdict)
		require.NotNil(t, m,
			"%s claims a bodyWS guard but does not name the route in the form this test can "+
				"verify. Write it as \"guarded: bodyWS on <METHOD> </path>\": %q", field, verdict)

		method, path := m[1], m[2]
		registration := findRouteRegistration(lines, method, path)
		require.NotEmpty(t, registration,
			"%s claims bodyWS guards %s %s, but no such route is registered in cmd/api/main.go",
			field, method, path)
		assert.Contains(t, registration, "bodyWS",
			"%s is declared guarded by bodyWS, but %s %s does not carry the guard:\n  %s",
			field, method, path, strings.TrimSpace(registration))
		checked++
	}

	require.Positive(t, checked,
		"no bodyWS verdicts were verified — the declaration format has drifted and this test is "+
			"no longer reading anything")
}

// TestAWorkspaceIDInARequestBodyIsNeverBenign_CatchesTheDeclarationThatShipped is
// the test for the test.
//
// The rule above is one string comparison, and a rule that cheap is also cheap to
// weaken back into "somebody wrote something" — add "benign:" to the accepted
// prefixes and every declaration passes again. This pins the exact verdict that
// was live in production and requires the rule to reject it, so the strengthening
// cannot be undone without failing here.
func TestAWorkspaceIDInARequestBodyIsNeverBenign_CatchesTheDeclarationThatShipped(t *testing.T) {
	const shipped = "benign: writes a preference row keyed on (workspace_id, the caller's own user_id)"

	assert.False(t, verdictEnforcesMembership(shipped),
		"the verdict that shipped with the notification-preferences hole is accepted again — "+
			"a workspace_id in a request body must be guarded or checked, never excused")

	assert.True(t, verdictEnforcesMembership("guarded: bodyWS on PUT /notifications/preferences"),
		"the verdict the fix records is not accepted, so the rule refuses the state it demands")
}

// verdictEnforcesMembership reports whether a declared verdict claims something
// actually stops a non-member, as opposed to explaining why nobody looked.
func verdictEnforcesMembership(verdict string) bool {
	for _, prefix := range enforcementVerdictPrefixes {
		if strings.HasPrefix(verdict, prefix) {
			return true
		}
	}
	return false
}

// findRouteRegistration returns the line in cmd/api/main.go registering the given
// method and path on the authenticated group, or "" if there is none.
func findRouteRegistration(lines []string, method, path string) string {
	needle := fmt.Sprintf("api.%s(%q", method, path)
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
