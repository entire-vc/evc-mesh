//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeBytes(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

func urlQuery(s string) string { return url.QueryEscape(s) }

// identityFixture is the shape of the situation these tests exist for: one
// person (the operator) owning two workspaces, and a second person who exists
// on the instance already and needs to be in both.
type identityFixture struct {
	operator            *TestEnv
	wsA, wsB            string
	guestEmail, guestPW string
	guestID             string
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()

	operator := NewTestEnv(t)
	t.Cleanup(func() { operator.Cleanup(t) })
	operator.Register(t, uniqueEmail("xwi-operator"), "TestPass123", "Operator")

	mkWorkspace := func(name string) string {
		resp := operator.Post(t, "/api/v1/workspaces", map[string]interface{}{"name": name})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var created map[string]interface{}
		operator.DecodeJSON(t, resp, &created)
		id, ok := created["id"].(string)
		require.True(t, ok)
		operator.OnCleanup(func() {
			ctx := context.Background()
			_, _ = operator.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", id)
			_, _ = operator.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", id)
		})
		return id
	}

	f := &identityFixture{
		operator:   operator,
		wsA:        mkWorkspace("Maombi"),
		wsB:        mkWorkspace("Prototypes"),
		guestEmail: uniqueEmail("xwi-guest"),
		guestPW:    "TestPass123",
	}

	operator.OnCleanup(func() {
		ctx := context.Background()
		_, _ = operator.DB.ExecContext(ctx,
			"DELETE FROM workspace_members WHERE user_id IN (SELECT id FROM users WHERE email = $1)", f.guestEmail)
		_, _ = operator.DB.ExecContext(ctx,
			"DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)", f.guestEmail)
		_, _ = operator.DB.ExecContext(ctx, "DELETE FROM users WHERE email = $1", f.guestEmail)
	})

	return f
}

// addGuestToA provisions the guest account inside the first workspace and
// returns their user id.
func (f *identityFixture) addGuestToA(t *testing.T, name string) string {
	t.Helper()
	body := map[string]interface{}{
		"email":    f.guestEmail,
		"role":     "member",
		"password": f.guestPW,
	}
	if name != "" {
		body["name"] = name
	}
	resp := f.operator.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", f.wsA), body)
	raw := f.operator.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "add member failed: %s", string(raw))

	var member map[string]interface{}
	require.NoError(t, decodeBytes(raw, &member))
	user, ok := member["user"].(map[string]interface{})
	require.True(t, ok, "add-member response must embed the user: %s", string(raw))
	id, ok := user["id"].(string)
	require.True(t, ok)
	f.guestID = id
	return id
}

func (f *identityFixture) members(t *testing.T, wsID string) []map[string]interface{} {
	t.Helper()
	resp := f.operator.Get(t, fmt.Sprintf("/api/v1/workspaces/%s/members", wsID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload struct {
		Members []map[string]interface{} `json:"members"`
	}
	f.operator.DecodeJSON(t, resp, &payload)
	return payload.Members
}

func (f *identityFixture) search(t *testing.T, env *TestEnv, wsID, query string) []map[string]interface{} {
	t.Helper()
	resp := env.Get(t, fmt.Sprintf("/api/v1/workspaces/%s/users/search?q=%s", wsID, urlQuery(query)))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload struct {
		Users []map[string]interface{} `json:"users"`
	}
	env.DecodeJSON(t, resp, &payload)
	return payload.Users
}

// ---------------------------------------------------------------------------

// The headline behaviour: the same account belongs to two workspaces, and the
// instance still holds exactly one user row for that address.
func TestCrossWorkspaceIdentity_OneAccountTwoWorkspaces(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	// Added to the second workspace by address, no password — the existing
	// account is what joins.
	resp := f.operator.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", f.wsB), map[string]interface{}{
		"email": strings.ToUpper(f.guestEmail), // spelling must not fork the identity
		"role":  "admin",
	})
	raw := f.operator.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "add to second workspace failed: %s", string(raw))

	var second map[string]interface{}
	require.NoError(t, decodeBytes(raw, &second))
	secondUser := second["user"].(map[string]interface{})
	assert.Equal(t, guestID, secondUser["id"],
		"the second workspace must attach the same account, not a new one")

	// One row in users, two in workspace_members.
	var accounts int
	require.NoError(t, f.operator.DB.GetContext(context.Background(), &accounts,
		"SELECT count(*) FROM users WHERE lower(email) = lower($1)", f.guestEmail))
	assert.Equal(t, 1, accounts, "exactly one account may exist for this address")

	var memberships int
	require.NoError(t, f.operator.DB.GetContext(context.Background(), &memberships,
		"SELECT count(*) FROM workspace_members WHERE user_id = $1", guestID))
	assert.Equal(t, 2, memberships)

	// And the guest can log in once and see both.
	guestEnv := NewTestEnv(t)
	defer guestEnv.Cleanup(t)
	guestEnv.Login(t, f.guestEmail, f.guestPW)
	require.NotEmpty(t, guestEnv.AuthToken, "one login serves both workspaces")

	resp = guestEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	guestEnv.DecodeJSON(t, resp, &workspaces)

	seen := map[string]bool{}
	for _, ws := range workspaces {
		if id, isStr := ws["id"].(string); isStr {
			seen[id] = true
		}
	}
	assert.True(t, seen[f.wsA], "workspace A must be listed for the single account")
	assert.True(t, seen[f.wsB], "workspace B must be listed for the same account")
}

// The search endpoint is what makes the above reachable from the UI: type the
// address of somebody who already exists and they come back as an addable
// result flagged as not-yet-a-member.
func TestCrossWorkspaceIdentity_SearchFindsAnExistingAccountByAddress(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	results := f.search(t, f.operator, f.wsB, f.guestEmail)
	require.NotEmpty(t, results, "an exact address must resolve so the person can be added")

	var found map[string]interface{}
	for _, r := range results {
		if r["id"] == guestID {
			found = r
		}
	}
	require.NotNil(t, found, "the guest must be in the results")
	assert.Equal(t, false, found["is_member"], "they are not in this workspace yet — that is the point")
	assert.Equal(t, "Guest Person", found["name"], "results carry the name, not just the address")
	assert.NotEmpty(t, found["username"], "and a username, so two people with one name can be told apart")
}

// The cross-tenant invariant. A loose query must not turn the endpoint into an
// instance-wide user directory — which is what it was, gated only by a
// permission any user can grant themselves by creating their own workspace.
func TestCrossWorkspaceIdentity_SearchCannotEnumerateStrangers(t *testing.T) {
	f := newIdentityFixture(t)
	f.addGuestToA(t, "Zzunique Person")

	// A completely unrelated user with their own workspace.
	outsider := NewTestEnv(t)
	defer outsider.Cleanup(t)
	outsider.Register(t, uniqueEmail("xwi-outsider"), "TestPass123", "Outsider")

	resp := outsider.Post(t, "/api/v1/workspaces", map[string]interface{}{"name": "Outsider WS"})
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"creating a workspace is open to any user — which is why owning one cannot be the tenant boundary")
	var ws map[string]interface{}
	outsider.DecodeJSON(t, resp, &ws)
	outsiderWS := ws["id"].(string)
	outsider.OnCleanup(func() {
		ctx := context.Background()
		_, _ = outsider.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", outsiderWS)
		_, _ = outsider.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", outsiderWS)
	})

	// The outsider is an owner, so they hold manage-members. They still must not
	// be able to page other tenants' users.
	for _, probe := range []string{"zzunique", "xwi-", "a", "%", "_", "@"} {
		results := f.search(t, outsider, outsiderWS, probe)
		for _, r := range results {
			assert.NotEqual(t, f.guestEmail, r["email"],
				"query %q leaked a user from a workspace the caller has nothing to do with", probe)
		}
	}

	// Knowing the exact address is still allowed — that is the deliberate
	// exception that makes inviting somebody possible at all.
	exact := f.search(t, outsider, outsiderWS, f.guestEmail)
	assert.NotEmpty(t, exact, "an exact address must remain resolvable")
}

// Provisioning without a name is what filled an instance with addresses where
// names belong. Supplying one has to actually stick, all the way to the member
// list the operator reads.
func TestCrossWorkspaceIdentity_MemberListShowsTheName(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	var stored string
	require.NoError(t, f.operator.DB.GetContext(context.Background(), &stored,
		"SELECT display_name FROM users WHERE id = $1", guestID))
	assert.Equal(t, "Guest Person", stored, "the name must reach the database, not the address")

	for _, m := range f.members(t, f.wsA) {
		user := m["user"].(map[string]interface{})
		if user["id"] != guestID {
			continue
		}
		assert.Equal(t, "Guest Person", user["name"])
		assert.NotEqual(t, user["name"], user["email"], "name and address must be different things")
		assert.NotEmpty(t, user["username"], "the member payload carries a username to disambiguate by")
		return
	}
	t.Fatal("guest not found in the member list")
}

// Editing: an operator can fill in a name nobody chose, and the moment its
// owner chooses one, the operator can no longer overwrite it. That boundary is
// what keeps a workspace-level edit from silently changing how the person
// appears in every other workspace they are in.
func TestCrossWorkspaceIdentity_NameEditingRespectsOwnership(t *testing.T) {
	f := newIdentityFixture(t)
	// Provisioned with no name: display_name is the address.
	guestID := f.addGuestToA(t, "")

	var stored string
	require.NoError(t, f.operator.DB.GetContext(context.Background(), &stored,
		"SELECT display_name FROM users WHERE id = $1", guestID))
	assert.Equal(t, strings.ToLower(f.guestEmail), strings.ToLower(stored),
		"this is the state the whole complaint is about")

	// The operator fills it in.
	resp := f.operator.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID),
		map[string]interface{}{"name": "Konstantin M."})
	raw := f.operator.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "filling in an unset name must be allowed: %s", string(raw))

	var updated map[string]interface{}
	require.NoError(t, decodeBytes(raw, &updated))
	updatedUser, ok := updated["user"].(map[string]interface{})
	require.True(t, ok, "PATCH must answer with the updated member, not a status envelope: %s", string(raw))
	assert.Equal(t, "Konstantin M.", updatedUser["name"])

	// The guest now sets their own name.
	guestEnv := NewTestEnv(t)
	defer guestEnv.Cleanup(t)
	guestEnv.Login(t, f.guestEmail, f.guestPW)
	resp = guestEnv.Patch(t, "/api/v1/auth/me", map[string]interface{}{"name": "Konstantin Malevich"})
	raw = guestEnv.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"a user must be able to edit their own display name: %s", string(raw))

	// And the operator can no longer overwrite it.
	resp = f.operator.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID),
		map[string]interface{}{"name": "Something Else"})
	_ = f.operator.ReadBody(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"once its owner has chosen a name, no workspace admin may rewrite it")

	require.NoError(t, f.operator.DB.GetContext(context.Background(), &stored,
		"SELECT display_name FROM users WHERE id = $1", guestID))
	assert.Equal(t, "Konstantin Malevich", stored, "the refusal must leave the chosen name intact")
}

// A role change must answer with the member it changed. The web client splices
// the response into its list, so a status envelope blanked the row.
func TestCrossWorkspaceIdentity_RoleChangeReturnsTheMember(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	resp := f.operator.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID),
		map[string]interface{}{"role": "admin"})
	raw := f.operator.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", string(raw))

	var updated map[string]interface{}
	require.NoError(t, decodeBytes(raw, &updated))
	assert.Equal(t, "admin", updated["role"])
	user, ok := updated["user"].(map[string]interface{})
	require.True(t, ok, "response must embed the user: %s", string(raw))
	assert.Equal(t, "Guest Person", user["name"])
}
