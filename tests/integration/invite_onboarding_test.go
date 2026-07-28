//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInviteFlow_NewTeammateCanAcceptAndLogIn covers the main onboarding path:
// an admin invites an address that has never been seen before, the invitee
// accepts with a password, and can then log in normally.
//
// This used to fail at the accept step with a 400: AcceptInvite built the new
// user without a username, and users.username is NOT NULL with a slug CHECK
// constraint, so Postgres rejected the INSERT.
func TestInviteFlow_NewTeammateCanAcceptAndLogIn(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// --- Inviter: a normal account with its own workspace ---
	inviterEmail := uniqueEmail("inviter")
	env.Register(t, inviterEmail, "TestPass123", "Invite Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces, "the registering user must own a default workspace")
	wsID := workspaces[0]["id"].(string)

	// --- Invite a brand-new address ---
	inviteeEmail := uniqueEmail("invitee")
	cleanupUserByEmail(t, env, inviteeEmail)

	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/invites", wsID), map[string]string{
		"email": inviteeEmail,
		"role":  "member",
	})
	body := env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create invite failed: %s", body)

	var invite map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &invite))
	token, ok := invite["token"].(string)
	require.True(t, ok && token != "", "invite response must carry a token: %s", body)

	// --- Accept it as the brand-new user (public endpoint, no auth) ---
	acceptClient := &TestEnv{BaseURL: env.BaseURL, HTTPClient: env.HTTPClient, DB: env.DB, t: t}
	resp = acceptClient.Post(t, fmt.Sprintf("/api/v1/invites/%s/accept", token), map[string]string{
		"name":     "Frank Invitee",
		"password": "TestPass123",
	})
	body = env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"accepting an invite as a new user must succeed, got %d: %s", resp.StatusCode, body)

	var accepted map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &accepted))
	assert.NotEmpty(t, accepted["access_token"])
	assert.NotEmpty(t, accepted["refresh_token"])

	// --- The new account is real: it has a valid username and can log in ---
	var username, storedEmail string
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT username, email FROM users WHERE lower(email) = lower($1)", inviteeEmail,
	).Scan(&username, &storedEmail))
	assert.NotEmpty(t, username, "the invited user must have been given a username")
	assert.Regexp(t, `^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`, username,
		"username must satisfy chk_users_username")
	assert.Equal(t, strings.ToLower(inviteeEmail), storedEmail, "email must be stored canonicalized")

	loginEnv := &TestEnv{BaseURL: env.BaseURL, HTTPClient: env.HTTPClient, DB: env.DB, t: t}
	result := loginEnv.Login(t, inviteeEmail, "TestPass123")
	tokens, ok := result["tokens"].(map[string]interface{})
	require.True(t, ok, "login response must carry tokens")
	assert.NotEmpty(t, tokens["access_token"])

	// And the invitee is a member of the inviting workspace. Checked against
	// the database rather than GET /api/v1/workspaces, which lists workspaces
	// the caller *owns* — an invited member owns none.
	var memberRole string
	require.NoError(t, env.DB.QueryRowContext(context.Background(), `
		SELECT wm.role
		  FROM workspace_members wm
		  JOIN users u ON u.id = wm.user_id
		 WHERE wm.workspace_id = $1 AND lower(u.email) = lower($2)`,
		wsID, inviteeEmail).Scan(&memberRole))
	assert.Equal(t, "member", memberRole, "the invitee must be a member of the inviting workspace")
}

// TestInviteFlow_MixedCaseInviteResolvesToOneAccount invites an address in
// mixed case; the account created on acceptance must be reachable by the
// lowercase spelling, not stranded behind the exact casing of the invite.
func TestInviteFlow_MixedCaseInviteResolvesToOneAccount(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	env.Register(t, uniqueEmail("mixedcase-inviter"), "TestPass123", "Mixed Case Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	lower := uniqueEmail("mixedcase-invitee")
	shouty := strings.ToUpper(lower)
	cleanupUserByEmail(t, env, lower)

	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/invites", wsID), map[string]string{
		"email": shouty,
		"role":  "member",
	})
	body := env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create invite failed: %s", body)

	var invite map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &invite))
	assert.Equal(t, lower, invite["email"], "the invite must be stored against the canonical address")
	token := invite["token"].(string)

	acceptEnv := &TestEnv{BaseURL: env.BaseURL, HTTPClient: env.HTTPClient, DB: env.DB, t: t}
	resp = acceptEnv.Post(t, fmt.Sprintf("/api/v1/invites/%s/accept", token), map[string]string{
		"name":     "Shouty Invitee",
		"password": "TestPass123",
	})
	body = env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "accept failed: %s", body)

	// Exactly one account, reachable by the lowercase spelling.
	var count int
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT count(*) FROM users WHERE lower(email) = lower($1)", lower).Scan(&count))
	assert.Equal(t, 1, count, "a mixed-case invite must not create a second shadow account")

	loginEnv := &TestEnv{BaseURL: env.BaseURL, HTTPClient: env.HTTPClient, DB: env.DB, t: t}
	loginEnv.Login(t, lower, "TestPass123")
}

// TestAuthFlow_EmailIsCaseAndWhitespaceInsensitive registers with a mixed-case,
// whitespace-padded address and proves every spelling of it reaches the same
// single account.
func TestAuthFlow_EmailIsCaseAndWhitespaceInsensitive(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	lower := uniqueEmail("case-insensitive")
	padded := "  " + strings.ToUpper(lower) + "  "
	password := "TestPass123"

	result := env.Register(t, padded, password, "Case Test User")
	user, ok := result["user"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, lower, user["email"], "the API must store the canonical address")

	// Every spelling logs in.
	for _, spelling := range []string{lower, strings.ToUpper(lower), padded} {
		loginEnv := &TestEnv{BaseURL: env.BaseURL, HTTPClient: env.HTTPClient, DB: env.DB, t: t}
		res := loginEnv.Login(t, spelling, password)
		tokens, tokensOK := res["tokens"].(map[string]interface{})
		require.True(t, tokensOK, "login with %q must return tokens", spelling)
		assert.NotEmpty(t, tokens["access_token"])
	}

	// And a case variant cannot claim a second account.
	dupEnv := &TestEnv{BaseURL: env.BaseURL, HTTPClient: env.HTTPClient, DB: env.DB, t: t}
	resp := dupEnv.Post(t, "/api/v1/auth/register", map[string]string{
		"email":    strings.ToUpper(lower),
		"password": password,
		"name":     "Impostor",
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode,
		"registering a case variant of an existing address must conflict")
	resp.Body.Close()

	var count int
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT count(*) FROM users WHERE lower(email) = lower($1)", lower).Scan(&count))
	assert.Equal(t, 1, count, "exactly one account must exist for the address")
}

// cleanupUserByEmail removes an account created outside TestEnv.Register (an
// invitee, for instance), which has no cleanup hook of its own.
func cleanupUserByEmail(t *testing.T, env *TestEnv, email string) {
	t.Helper()
	env.OnCleanup(func() {
		ctx := context.Background()
		const sub = "(SELECT id FROM users WHERE lower(email) = lower($1))"
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE user_id = "+sub, email)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE user_id = "+sub, email)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE owner_id = "+sub, email)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM user_invites WHERE lower(email) = lower($1)", email)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM users WHERE lower(email) = lower($1)", email)
	})
}
