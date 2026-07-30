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

// TestWorkspaceListing_MemberSeesWorkspace verifies that GET /api/v1/workspaces
// lists workspaces by membership, not just by ownership.
//
// Regression: the endpoint used to run
//
//	SELECT * FROM workspaces WHERE owner_id = $1
//
// so a user added to a workspace as a plain member got an empty list and could
// only reach the workspace through a direct link.
//
// Scenario:
//  1. Register user A (becomes the owner of a default workspace).
//  2. A creates an additional workspace.
//  3. A adds user B to that workspace as a member (the endpoint creates B's
//     account when a password is supplied).
//  4. Log in as B and GET /api/v1/workspaces — the workspace must be listed.
func TestWorkspaceListing_MemberSeesWorkspace(t *testing.T) {
	ownerEnv := NewTestEnv(t)
	defer ownerEnv.Cleanup(t)

	ownerEmail := uniqueEmail("ws-list-owner")
	ownerEnv.Register(t, ownerEmail, "TestPass123", "Owner User")

	// --- Step 1: owner creates a workspace ---
	resp := ownerEnv.Post(t, "/api/v1/workspaces", map[string]interface{}{
		"name": "Membership Listing WS",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "owner must be able to create a workspace")
	var created map[string]interface{}
	ownerEnv.DecodeJSON(t, resp, &created)
	wsID, ok := created["id"].(string)
	require.True(t, ok, "create workspace response must contain an id")
	require.NotEmpty(t, wsID)

	ownerEnv.OnCleanup(func() {
		ctx := context.Background()
		_, _ = ownerEnv.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", wsID)
		_, _ = ownerEnv.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", wsID)
	})

	// --- Step 2: owner adds a member ---
	memberEmail := uniqueEmail("ws-list-member")
	const memberPassword = "TestPass123"

	resp = ownerEnv.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/members", wsID), map[string]interface{}{
		"email":    memberEmail,
		"role":     "member",
		"password": memberPassword,
	})
	body := ownerEnv.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "add member failed: %s", string(body))

	ownerEnv.OnCleanup(func() {
		ctx := context.Background()
		_, _ = ownerEnv.DB.ExecContext(ctx,
			"DELETE FROM workspace_members WHERE user_id IN (SELECT id FROM users WHERE email = $1)", memberEmail)
		_, _ = ownerEnv.DB.ExecContext(ctx,
			"DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)", memberEmail)
		_, _ = ownerEnv.DB.ExecContext(ctx, "DELETE FROM users WHERE email = $1", memberEmail)
	})

	// --- Step 3: the member logs in and lists their workspaces ---
	memberEnv := NewTestEnv(t)
	defer memberEnv.Cleanup(t)
	memberEnv.Login(t, memberEmail, memberPassword)
	require.NotEmpty(t, memberEnv.AuthToken, "member must be able to log in")

	resp = memberEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	memberEnv.DecodeJSON(t, resp, &workspaces)

	ids := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		if id, isString := ws["id"].(string); isString {
			ids = append(ids, id)
		}
	}
	assert.Contains(t, ids, wsID, "a workspace member must see the workspace in GET /api/v1/workspaces")

	// The member owns nothing, so the workspace must appear exactly once.
	count := 0
	for _, id := range ids {
		if id == wsID {
			count++
		}
	}
	assert.Equal(t, 1, count, "the workspace must not be listed twice")

	// --- Step 4: the owner still sees it too ---
	resp = ownerEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var ownerWorkspaces []map[string]interface{}
	ownerEnv.DecodeJSON(t, resp, &ownerWorkspaces)

	ownerIDs := make([]string, 0, len(ownerWorkspaces))
	for _, ws := range ownerWorkspaces {
		if id, isString := ws["id"].(string); isString {
			ownerIDs = append(ownerIDs, id)
		}
	}
	assert.Contains(t, ownerIDs, wsID, "the owner must still see their own workspace")
}

// TestWorkspaceListing_NonMemberDoesNotSeeWorkspace verifies that listing by
// membership does not leak workspaces to unrelated users.
func TestWorkspaceListing_NonMemberDoesNotSeeWorkspace(t *testing.T) {
	ownerEnv := NewTestEnv(t)
	defer ownerEnv.Cleanup(t)

	ownerEnv.Register(t, uniqueEmail("ws-list-private-owner"), "TestPass123", "Private Owner")

	resp := ownerEnv.Post(t, "/api/v1/workspaces", map[string]interface{}{
		"name": "Private Listing WS",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]interface{}
	ownerEnv.DecodeJSON(t, resp, &created)
	wsID, ok := created["id"].(string)
	require.True(t, ok)

	ownerEnv.OnCleanup(func() {
		ctx := context.Background()
		_, _ = ownerEnv.DB.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", wsID)
		_, _ = ownerEnv.DB.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", wsID)
	})

	// An unrelated user must not see it.
	outsiderEnv := NewTestEnv(t)
	defer outsiderEnv.Cleanup(t)
	outsiderEnv.Register(t, uniqueEmail("ws-list-outsider"), "TestPass123", "Outsider User")

	resp = outsiderEnv.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	outsiderEnv.DecodeJSON(t, resp, &workspaces)

	for _, ws := range workspaces {
		assert.NotEqual(t, wsID, ws["id"], "an unrelated user must not see another user's workspace")
	}
}
