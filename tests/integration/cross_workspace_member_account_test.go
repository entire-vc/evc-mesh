//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Admin edits of another member's sign-in identity: PATCH
// /workspaces/:ws_id/members/:user_id with email / password. Named under
// TestCrossWorkspace so the tenancy gate runs them against the live API.

// cleanupUserByID removes an account whose address the test itself changed —
// the fixture's own cleanup keys on the original address and would miss it.
func cleanupUserByID(env *TestEnv, userID string) {
	env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE user_id = $1", userID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE user_id = $1", userID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", userID)
	})
}

func cleanupActivity(env *TestEnv, wsID string) {
	env.OnCleanup(func() {
		_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM activity_log WHERE workspace_id = $1", wsID)
	})
}

// AC: an owner sets a member's password and address; the member signs in with
// the new pair, the old password stops working, and the audit row names the
// reset without the password.
func TestCrossWorkspaceMemberAccount_OwnerSetsPasswordAndEmail(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")
	cleanupUserByID(f.operator, guestID)
	cleanupActivity(f.operator, f.wsA)

	newEmail := uniqueEmail("xwi-renamed")
	const newPW = "NewPass456"

	resp := f.operator.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID), map[string]interface{}{
		"email":    newEmail,
		"password": newPW,
	})
	raw := f.operator.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "owner account edit failed: %s", string(raw))
	assert.NotContains(t, string(raw), newPW, "the password must never come back")
	assert.Contains(t, string(raw), newEmail)

	guest := NewTestEnv(t)
	login := guest.Login(t, newEmail, newPW)
	tokens, ok := login["tokens"].(map[string]interface{})
	require.True(t, ok, "login response carries tokens: %v", login)
	assert.NotEmpty(t, tokens["access_token"])

	old := guest.Post(t, "/api/v1/auth/login", map[string]string{"email": newEmail, "password": f.guestPW})
	_ = old.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, old.StatusCode, "the old password must stop working")

	var changes []string
	require.NoError(t, f.operator.DB.SelectContext(context.Background(), &changes,
		`SELECT changes::text FROM activity_log WHERE workspace_id = $1 AND action = 'member.password_reset'`, f.wsA))
	require.Len(t, changes, 1, "exactly one audit row for the reset")
	assert.Contains(t, changes[0], guestID)
	assert.NotContains(t, changes[0], newPW, "the password must never reach the audit log")
}

// AC: a plain member cannot set another member's password.
func TestCrossWorkspaceMemberAccount_MemberIsForbidden(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	peerEmail := uniqueEmail("xwi-peer")
	resp := f.operator.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", f.wsA), map[string]interface{}{
		"email": peerEmail, "role": "member", "password": "TestPass123",
	})
	raw := f.operator.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "add peer failed: %s", string(raw))
	f.operator.OnCleanup(func() {
		ctx := context.Background()
		_, _ = f.operator.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE user_id IN (SELECT id FROM users WHERE email = $1)", peerEmail)
		_, _ = f.operator.DB.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)", peerEmail)
		_, _ = f.operator.DB.ExecContext(ctx, "DELETE FROM users WHERE email = $1", peerEmail)
	})

	peer := NewTestEnv(t)
	peer.Login(t, peerEmail, "TestPass123")
	resp = peer.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID), map[string]interface{}{
		"password": "NewPass456",
	})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// The refused request changed nothing: the old password still works.
	guest := NewTestEnv(t)
	guest.Login(t, f.guestEmail, f.guestPW)
}

// Anti-takeover: the guest also belongs to a workspace somebody else owns.
// The operator administers workspace A only, so resetting the guest's password
// from A would hand them that other workspace too — refused with 403.
func TestCrossWorkspaceMemberAccount_ForeignWorkspaceBlocksTakeover(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	stranger := NewTestEnv(t)
	t.Cleanup(func() { stranger.Cleanup(t) })
	stranger.Register(t, uniqueEmail("xwi-stranger"), "TestPass123", "Stranger")
	resp := stranger.Post(t, "/api/v1/workspaces", map[string]interface{}{"name": "Elsewhere"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var ws map[string]interface{}
	stranger.DecodeJSON(t, resp, &ws)
	wsC, _ := ws["id"].(string)
	stranger.OnCleanup(func() {
		ctx := context.Background()
		_, _ = stranger.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", wsC)
		_, _ = stranger.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", wsC)
	})
	resp = stranger.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", wsC), map[string]interface{}{
		"email": f.guestEmail, "role": "member",
	})
	raw := stranger.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "add guest to the foreign workspace failed: %s", string(raw))

	resp = f.operator.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID), map[string]interface{}{
		"password": "NewPass456",
	})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	guest := NewTestEnv(t)
	guest.Login(t, f.guestEmail, f.guestPW)
}

// An address already held by another account is a 409, not a silent merge.
func TestCrossWorkspaceMemberAccount_DuplicateEmailConflicts(t *testing.T) {
	f := newIdentityFixture(t)
	guestID := f.addGuestToA(t, "Guest Person")

	other := NewTestEnv(t)
	t.Cleanup(func() { other.Cleanup(t) })
	otherEmail := uniqueEmail("xwi-taken")
	other.Register(t, otherEmail, "TestPass123", "Taken")

	resp := f.operator.Patch(t, fmt.Sprintf("/api/v1/workspaces/%s/members/%s", f.wsA, guestID), map[string]interface{}{
		"email": otherEmail,
	})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}
