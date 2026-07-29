//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
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
	// GET/HEAD /icon are registered exceptions — see wsPublicRoutes below and
	// TestCrossTenant_PublicRoutesBehaveExactlyAsRegistered, which pins what
	// they are allowed to do. Uploading an icon is NOT an exception: PUT stays
	// members-only and admin-gated, and is asserted here like everything else.
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

// wsPublicRoute is a workspace-scoped route that is deliberately readable
// without workspace membership.
//
// This list is the ONLY way out of the "every /workspaces/:ws_id route refuses a
// non-member" invariant, and it exists so that stepping out is a visible,
// argued decision rather than a quietly deleted assertion. Deleting a route
// from wsScopedRoutes without registering it here fails
// TestCrossTenant_RouteTableIsComplete; registering it here without a `why`
// fails TestCrossTenant_PublicRoutesAreJustified. The invariant keeps applying
// unchanged to every other route.
//
// Bar for adding an entry: the route must be impossible to authenticate rather
// than merely inconvenient, and what it exposes must be worth exposing to
// anyone holding the workspace id.
type wsPublicRoute struct {
	method string
	suffix string
	why    string // mandatory: why membership cannot be required here
}

var wsPublicRoutes = []wsPublicRoute{
	{
		http.MethodGet, "/icon",
		"The browser loads the workspace icon as <img src> and <link rel=icon>. " +
			"Neither tag can carry an Authorization header, and Mesh has no cookie " +
			"auth (the server never issues one — the token lives only in a header), " +
			"so membership cannot be checked on this request at all: behind the " +
			"authenticated group it answered every browser 401 and the icon could " +
			"never render. A signed token in the URL was rejected as the alternative " +
			"— it leaks via logs and Referer, for a logo. What is exposed is one PNG " +
			"to whoever already knows the workspace UUID, which is only ever handed " +
			"out in authenticated responses. Uploading it (PUT) is NOT public.",
	},
	{
		http.MethodHead, "/icon",
		"Same route and same handler as GET /icon: HEAD is what curl -I and cache " +
			"validators send, and Echo does not derive it from the GET registration. " +
			"It exposes strictly less than the GET it mirrors.",
	},
}

// TestCrossTenant_PublicRoutesAreJustified keeps the exception list from becoming a
// dumping ground. An entry with no argument behind it is exactly the failure mode this
// list is meant to prevent, so an empty or throwaway `why` is a test failure.
func TestCrossTenant_PublicRoutesAreJustified(t *testing.T) {
	for _, r := range wsPublicRoutes {
		t.Run(r.method+" /workspaces/:ws_id"+r.suffix, func(t *testing.T) {
			assert.NotEmpty(t, strings.TrimSpace(r.why),
				"every public workspace route must record why membership cannot be required")
			assert.Greater(t, len(r.why), 80,
				"the justification must be an actual argument, not a label: %q", r.why)
		})
	}
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

// TestCrossTenant_RouteTableIsComplete keeps the tables above honest. It reads the real
// route registrations out of cmd/api/main.go and fails if any /workspaces/:ws_id route
// appears in neither wsScopedRoutes (proven to refuse a non-member) nor wsPublicRoutes
// (a registered, argued exception).
//
// The guard itself is registered group-wide, so a newly added route on the authenticated
// group is protected whether or not anyone updates this file — this test exists so that
// protection is also proven for each new route rather than assumed.
//
// It matches registrations on BOTH the authenticated group (`api.`) and the version
// group (`v1.`) on purpose. Moving a route from `api.` to `v1.` is precisely how a
// workspace route would slip out of the membership guard unnoticed, so such a move has
// to land it in the exception list or fail here.
func TestCrossTenant_RouteTableIsComplete(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err, "cannot read route registrations")

	re := regexp.MustCompile(`\b(?:api|v1)\.(GET|HEAD|POST|PUT|PATCH|DELETE)\("(/workspaces/:ws_id[^"]*)"`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, matches, "found no :ws_id routes — has the registration style changed?")

	normalise := func(suffix string) string {
		// Drop the query string; nested ids are normalised back to their param names.
		if i := strings.IndexByte(suffix, '?'); i >= 0 {
			suffix = suffix[:i]
		}
		suffix = strings.ReplaceAll(suffix, "/members/"+dummyUUID, "/members/:user_id")
		suffix = strings.ReplaceAll(suffix, "/invites/"+dummyUUID, "/invites/:invite_id")
		return suffix
	}

	covered := make(map[string]bool, len(wsScopedRoutes)+len(wsPublicRoutes))
	for _, r := range wsScopedRoutes {
		covered[r.method+" /workspaces/:ws_id"+normalise(r.suffix)] = true
	}
	registeredPublic := make(map[string]bool, len(wsPublicRoutes))
	for _, r := range wsPublicRoutes {
		key := r.method + " /workspaces/:ws_id" + normalise(r.suffix)
		covered[key] = true
		registeredPublic[key] = true
	}

	var missing []string
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		key := m[1] + " " + m[2]
		seen[key] = true
		if !covered[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	assert.Empty(t, missing,
		"these workspace-scoped routes are covered by neither table — add them to "+
			"wsScopedRoutes so a non-member is proven to get 403, or, if membership "+
			"genuinely cannot be enforced, register them in wsPublicRoutes with a "+
			"justification:\n  %s",
		strings.Join(missing, "\n  "))

	// The exception list must not outlive the routes it excuses: a stale entry
	// would silently pre-approve a future route with the same name.
	for key := range registeredPublic {
		assert.True(t, seen[key],
			"%s is registered in wsPublicRoutes but no longer exists in cmd/api/main.go — "+
				"remove the exception", key)
	}
}

// TestCrossTenant_PublicRoutesBehaveExactlyAsRegistered pins the icon exception to
// exactly what wsPublicRoutes claims for it. Being on the exception list buys the
// route one thing — anonymous reads — and nothing else:
//
//	(a) an existing icon is served to a caller with no credentials at all;
//	(b) an unknown workspace and a real workspace with no icon answer identically,
//	    so the route cannot be used to enumerate which workspace ids exist;
//	(c) uploading is still refused to a non-member.
//
// (c) already passes today; it is asserted here so that it cannot quietly stop
// passing while attention is on the read path.
func TestCrossTenant_PublicRoutesBehaveExactlyAsRegistered(t *testing.T) {
	owner := NewTestEnv(t)
	defer owner.Cleanup(t)
	owner.Register(t, uniqueEmail("icon-owner"), "TestPass123", "Icon Owner")
	ownerWS := firstWorkspaceID(t, owner)

	// A second tenant: their workspace exists but has no icon.
	other := NewTestEnv(t)
	defer other.Cleanup(t)
	other.Register(t, uniqueEmail("icon-other"), "TestPass123", "Other Owner")
	otherWS := firstWorkspaceID(t, other)

	// anon carries no Authorization header — exactly what an <img> tag sends.
	anon := NewTestEnv(t)
	defer anon.Cleanup(t)
	require.Empty(t, anon.AuthToken, "the anonymous client must not be authenticated")

	png := onePixelPNG(t)
	storageAvailable, uploadDetail := uploadIconForRead(t, owner, ownerWS, png)

	t.Run("(a) anonymous GET serves the icon", func(t *testing.T) {
		if !storageAvailable {
			// Explicit and loud, never a silent green. (b) and (c) below do not
			// touch storage and still run.
			t.Skipf("no object storage in this environment, so there is no icon to read back: %s", uploadDetail)
		}

		resp := anon.Get(t, "/api/v1/workspaces/"+ownerWS+"/icon")
		body := anon.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"an <img> tag cannot authenticate; this route must answer it (body %s)", string(body))
		assert.Contains(t, resp.Header.Get("Content-Type"), "image/png")
		assert.Equal(t, png, body, "the bytes served must be the bytes uploaded")
	})

	t.Run("(b) unknown workspace and iconless workspace are indistinguishable", func(t *testing.T) {
		// A random id that belongs to no workspace at all.
		absent := anon.Get(t, "/api/v1/workspaces/"+uuid.NewString()+"/icon")
		absentBody := anon.ReadBody(t, absent)

		// A workspace that really exists, owned by someone else, with no icon.
		iconless := anon.Get(t, "/api/v1/workspaces/"+otherWS+"/icon")
		iconlessBody := anon.ReadBody(t, iconless)

		assert.Equal(t, http.StatusNotFound, absent.StatusCode)
		assert.Equal(t, http.StatusNotFound, iconless.StatusCode)
		// Byte-identical, not merely both-404: a different message would let
		// anyone probe ids and learn which workspaces are real.
		assert.Equal(t, string(absentBody), string(iconlessBody),
			"a missing workspace and an iconless workspace must answer identically, "+
				"otherwise this public route is an existence oracle")
	})

	t.Run("(c) uploading an icon is still refused to a non-member", func(t *testing.T) {
		resp := uploadWorkspaceIconRaw(t, other, ownerWS, png)
		body := string(other.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a non-member uploaded an icon into another tenant's workspace (body %s)", body)

		// And anonymously, which is what the read path now allows.
		anonResp := uploadWorkspaceIconRaw(t, anon, ownerWS, png)
		anonBody := string(anon.ReadBody(t, anonResp))
		assert.Equal(t, http.StatusUnauthorized, anonResp.StatusCode,
			"an unauthenticated caller uploaded an icon (body %s)", anonBody)
	})
}

// onePixelPNG builds the smallest valid PNG, so the test exercises the real
// magic-byte check rather than a hand-waved blob.
func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 30, G: 120, B: 200, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// storageUnavailableMarker is the API's own wording for "the object storage
// endpoint never answered". It is the ONLY upload failure that may downgrade
// subtest (a) to a skip: bad credentials, a bucket that cannot be created, or
// any other error is a real defect and must fail loudly.
const storageUnavailableMarker = "Object storage is unreachable"

// uploadIconForRead uploads the icon that subtest (a) reads back, and reports
// whether object storage exists in this environment at all.
//
// It exists because the CI job that runs these tests had no object storage:
// the upload answered 500 and (a) failed for an environmental reason rather
// than a defect. MinIO is now started in that job, so the normal path is a
// successful upload — but a developer running the suite against a bare API
// still gets an honest result instead of a red herring, and subtests (b) and
// (c), which never touch storage, keep running either way.
func uploadIconForRead(t *testing.T, env *TestEnv, wsID string, content []byte) (ok bool, detail string) {
	t.Helper()

	resp := uploadWorkspaceIconRaw(t, env, wsID, content)
	body := string(env.ReadBody(t, resp))
	if resp.StatusCode == http.StatusOK {
		return true, body
	}

	// Anything other than "storage is not there" is a genuine failure — do not
	// let it hide behind a skip.
	require.True(t,
		resp.StatusCode == http.StatusInternalServerError && strings.Contains(body, storageUnavailableMarker),
		"icon upload failed for a reason other than absent object storage (status %d): %s",
		resp.StatusCode, body)

	return false, body
}

// uploadWorkspaceIconRaw performs the multipart PUT and returns the raw response.
// The shared helpers only speak JSON, and the auth header is attached only when the
// env actually holds a token — so passing an unregistered env produces a genuinely
// anonymous request.
func uploadWorkspaceIconRaw(t *testing.T, env *TestEnv, wsID string, content []byte) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", "icon.png")
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req, err := http.NewRequest(http.MethodPut,
		env.BaseURL+"/api/v1/workspaces/"+wsID+"/icon", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if env.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+env.AuthToken)
	}

	resp, err := env.HTTPClient.Do(req)
	require.NoError(t, err)
	return resp
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
