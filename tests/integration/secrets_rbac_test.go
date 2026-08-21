//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file closes two proof gaps left open by the cross-tenant secrets
// suite in cross_tenant_params_test.go (task #a23e2cd6, follow-up to the
// second verification round on #37cc114f):
//
//  1. TestCrossTenant_StrangerCannotRotateOrDeleteASecret proves denial to an
//     OUTSIDER — someone with no membership in the victim's workspace at
//     all. It says nothing about a member of the SAME workspace who simply
//     lacks PermManageSecrets (role "member"/"viewer", or an agent key) —
//     the exact class PermManageSecrets exists to gate, per its declaration
//     in internal/middleware/rbac.go.
//  2. TestCrossTenant_ScopedRoutesStillWorkForTheirOwner's `readable` table
//     lists only GET routes, and secrets have no flat GET by design — so the
//     owner's positive path (create → list → rotate → delete) has never run
//     as an integration test, only under mocks in
//     internal/handler/secret_handler_test.go.
//
// Together the two gaps mean the existing suite cannot distinguish "the
// permission works correctly" from "the permission denies everybody,
// including its owner" — a gate that strangles everyone looks identically
// green on cross-tenant tests alone.

// secretsRBACValueClassRE mirrors internal/repository/postgres/secret_repo.go's
// classify() exactly (same four buckets, same '+'-joined order, a-z/A-Z/0-9/sym).
// It is reimplemented here rather than imported so that these tests assert the
// documented CONTRACT of the masked view (what env-inventory.py and the
// production classify() both compute), not merely "whatever the code
// currently returns" — the same reason newVictimFixture reads route
// registrations rather than hand-copying them.
var secretsRBACValueClassRE = struct {
	lower, upper, digit, sym *regexp.Regexp
}{
	lower: regexp.MustCompile(`[a-z]`),
	upper: regexp.MustCompile(`[A-Z]`),
	digit: regexp.MustCompile(`\d`),
	sym:   regexp.MustCompile(`[^A-Za-z0-9]`),
}

func secretsRBACClassify(v string) string {
	var parts []string
	if secretsRBACValueClassRE.lower.MatchString(v) {
		parts = append(parts, "a-z")
	}
	if secretsRBACValueClassRE.upper.MatchString(v) {
		parts = append(parts, "A-Z")
	}
	if secretsRBACValueClassRE.digit.MatchString(v) {
		parts = append(parts, "0-9")
	}
	if secretsRBACValueClassRE.sym.MatchString(v) {
		parts = append(parts, "sym")
	}
	if len(parts) == 0 {
		return "?"
	}
	return strings.Join(parts, "+")
}

func secretsRBACFingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:8]
}

// secretsRBACFixture is a lighter-weight cousin of victimFixture
// (cross_tenant_params_test.go): these tests need only a workspace and one
// real secret, not the full flat-object catalogue that the cross-tenant
// enumeration table exercises.
type secretsRBACFixture struct {
	ownerEnv *TestEnv
	wsID     string
	secretID string
}

// newSecretsRBACFixture registers an owner and creates one real secret in
// their workspace. A real secret is required, not best-effort: a rotate or
// delete aimed at an id that does not exist would 403/404 whether or not the
// permission check ran at all, which is exactly the failure mode
// TestCrossTenant_StrangerCannotRotateOrDeleteASecret's own fixture comment
// warns about (see f.secretID there, citing #a49500c5).
func newSecretsRBACFixture(t *testing.T, prefix string) *secretsRBACFixture {
	t.Helper()
	env := NewTestEnv(t)
	t.Cleanup(func() { env.Cleanup(t) })
	env.Register(t, uniqueEmail(prefix), "TestPass123", "Secrets RBAC Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	resp = env.Post(t, "/api/v1/workspaces/"+wsID+"/secrets", map[string]any{
		"name":  "RBAC_FIXTURE_TOKEN",
		"scope": "workspace",
		"value": "rbac-fixture-secret-value",
	})
	raw := env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "fixture secret create failed: %s", string(raw))
	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created))
	secretID, _ := created["id"].(string)
	require.NotEmpty(t, secretID, "fixture secret create returned no id: %s", string(raw))

	return &secretsRBACFixture{ownerEnv: env, wsID: wsID, secretID: secretID}
}

// isCurrent reports whether the fixture secret is still the current row
// (rotated_at IS NULL) — the same survival check
// TestCrossTenant_StrangerCannotRotateOrDeleteASecret uses, and for the same
// reason: rotate and delete both work by stamping rotated_at on the CURRENT
// row, so a refusal that leaked through would not look like corruption on
// the next read, it would look like the secret quietly ceasing to exist.
func (f *secretsRBACFixture) isCurrent(t *testing.T) bool {
	t.Helper()
	var rotated *time.Time
	require.NoError(t, f.ownerEnv.DB.QueryRowContext(context.Background(),
		"SELECT rotated_at FROM secrets WHERE id = $1", f.secretID).Scan(&rotated))
	return rotated == nil
}

// TestSecretsRBAC_SameWorkspaceMemberWithoutManagePermIsForbidden is Gap 1,
// user half. A member of the SAME workspace (role "member", holding none of
// the excluded permissions per permissionMatrix in rbac.go) must get 403 on
// all four secret routes, and a refused mutation must not have touched the
// row.
func TestSecretsRBAC_SameWorkspaceMemberWithoutManagePermIsForbidden(t *testing.T) {
	f := newSecretsRBACFixture(t, "sec-member-owner")
	require.True(t, f.isCurrent(t), "fixture secret is not current before the test even starts")

	memberEmail := uniqueEmail("sec-member-user")
	memberEnv := NewTestEnv(t)
	defer memberEnv.Cleanup(t)
	memberResult := memberEnv.Register(t, memberEmail, "TestPass123", "Secrets Member")
	memberUser, _ := memberResult["user"].(map[string]any)
	memberUserID, _ := memberUser["id"].(string)
	require.NotEmpty(t, memberUserID, "member registration returned no user id: %v", memberResult)

	// Add takes an email (addMemberRequest, workspace_member_handler.go), not a
	// user_id — it resolves an existing account by address or creates one if a
	// password is also given. The member already registered above, so no
	// password is needed here.
	resp := f.ownerEnv.Post(t, "/api/v1/workspaces/"+f.wsID+"/members", map[string]any{
		"email": memberEmail,
		"role":  "member",
	})
	inviteBody := string(f.ownerEnv.ReadBody(t, resp))
	require.Equal(t, http.StatusCreated, resp.StatusCode, "fixture invite failed: %s", inviteBody)

	t.Run("create", func(t *testing.T) {
		resp := memberEnv.Post(t, "/api/v1/workspaces/"+f.wsID+"/secrets", map[string]any{
			"name": "MEMBER_ATTEMPT_TOKEN", "scope": "workspace", "value": "member-attempt-value",
		})
		body := string(memberEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a same-workspace member without manage_secrets created a secret (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("list", func(t *testing.T) {
		resp := memberEnv.Get(t, "/api/v1/workspaces/"+f.wsID+"/secrets")
		body := string(memberEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a same-workspace member without manage_secrets listed secrets (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("rotate", func(t *testing.T) {
		resp := memberEnv.Post(t, "/api/v1/secrets/"+f.secretID+"/rotate", map[string]any{
			"value": "planted-by-a-plain-member",
		})
		body := string(memberEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a same-workspace member without manage_secrets rotated a secret (status %d, body %s)", resp.StatusCode, body)
		assert.True(t, f.isCurrent(t), "a plain member's rotate superseded the secret")
	})

	t.Run("delete", func(t *testing.T) {
		resp := memberEnv.Delete(t, "/api/v1/secrets/"+f.secretID)
		body := string(memberEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a same-workspace member without manage_secrets deleted a secret (status %d, body %s)", resp.StatusCode, body)
		assert.True(t, f.isCurrent(t), "a plain member's delete ended the secret")
	})
}

// TestSecretsRBAC_AgentKeyIsForbiddenOnAllFourRoutes is Gap 1, agent half.
// PermManageSecrets is deliberately absent from agentPerms (rbac.go) — no
// agent identity may read, rotate, or delete a secret, because an agent able
// to rotate one could plant a value of its own choosing and then read it
// back through the materializer. This proves the fast-path 403 by running
// it, not by reading agentPerms.
func TestSecretsRBAC_AgentKeyIsForbiddenOnAllFourRoutes(t *testing.T) {
	f := newSecretsRBACFixture(t, "sec-agent-owner")
	require.True(t, f.isCurrent(t), "fixture secret is not current before the test even starts")

	_, agentKey := f.ownerEnv.CreateAgent(t, f.wsID, "sec-rbac-agent")

	t.Run("create", func(t *testing.T) {
		resp := f.ownerEnv.PostWithAgentKey(t, "/api/v1/workspaces/"+f.wsID+"/secrets", agentKey, map[string]any{
			"name": "AGENT_ATTEMPT_TOKEN", "scope": "workspace", "value": "agent-attempt-value",
		})
		body := string(f.ownerEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an agent key created a secret (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("list", func(t *testing.T) {
		resp := f.ownerEnv.GetWithAgentKey(t, "/api/v1/workspaces/"+f.wsID+"/secrets", agentKey)
		body := string(f.ownerEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an agent key listed secrets (status %d, body %s)", resp.StatusCode, body)
	})

	t.Run("rotate", func(t *testing.T) {
		resp := f.ownerEnv.PostWithAgentKey(t, "/api/v1/secrets/"+f.secretID+"/rotate", agentKey, map[string]any{
			"value": "planted-by-an-agent",
		})
		body := string(f.ownerEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an agent key rotated a secret (status %d, body %s)", resp.StatusCode, body)
		assert.True(t, f.isCurrent(t), "an agent key's rotate superseded the secret")
	})

	t.Run("delete", func(t *testing.T) {
		resp := f.ownerEnv.doRequestWithAgentKey(t, http.MethodDelete, "/api/v1/secrets/"+f.secretID, agentKey, nil)
		body := string(f.ownerEnv.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an agent key deleted a secret (status %d, body %s)", resp.StatusCode, body)
		assert.True(t, f.isCurrent(t), "an agent key's delete ended the secret")
	})
}

// TestSecretsRBAC_OwnerCanCreateRotateAndDeleteTheirOwnSecret is Gap 2: the
// legitimate owner's positive path, end to end, against the live handler —
// not the mocked unit coverage in internal/handler/secret_handler_test.go.
// It walks create -> masked list -> rotate -> masked list -> delete ->
// masked list, asserting the exact sha256[:8]/length/char-class fields the
// masked view promises at each step, and that rotate inserts a NEW row
// (never edits in place) while stamping the old one's rotated_at.
func TestSecretsRBAC_OwnerCanCreateRotateAndDeleteTheirOwnSecret(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)
	env.Register(t, uniqueEmail("sec-owner-positive"), "TestPass123", "Secrets Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	const value = "OwnerPathValue123!"
	expectedPrefix := secretsRBACFingerprint(value)
	expectedLength := float64(len(value))
	expectedClass := secretsRBACClassify(value)
	require.Equal(t, "a-z+A-Z+0-9+sym", expectedClass,
		"fixture value no longer exercises all four character classes — the point of this test")

	// --- Create ---
	resp = env.Post(t, "/api/v1/workspaces/"+wsID+"/secrets", map[string]any{
		"name": "OWNER_POSITIVE_TOKEN", "scope": "workspace", "value": value,
	})
	raw := env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "owner create failed: %s", string(raw))
	assert.NotContains(t, string(raw), value, "create response echoed the plaintext value")

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created))
	secretID, _ := created["id"].(string)
	require.NotEmpty(t, secretID)
	assert.Equal(t, expectedPrefix, created["value_sha256_prefix"], "create response fingerprint mismatch")
	assert.Equal(t, expectedLength, created["value_length"], "create response length mismatch")
	assert.Equal(t, expectedClass, created["value_char_class"], "create response char class mismatch")

	env.OnCleanup(func() {
		_, _ = env.DB.ExecContext(context.Background(),
			"DELETE FROM secrets WHERE workspace_id = $1 AND name = 'OWNER_POSITIVE_TOKEN'", wsID)
	})

	// --- Appears in the masked list ---
	findByID := func(t *testing.T, id string) map[string]any {
		t.Helper()
		listResp := env.Get(t, "/api/v1/workspaces/"+wsID+"/secrets")
		body := string(env.ReadBody(t, listResp))
		require.Equal(t, http.StatusOK, listResp.StatusCode, "owner list failed: %s", body)
		var listed []map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &listed))
		for _, s := range listed {
			if sid, _ := s["id"].(string); sid == id {
				return s
			}
		}
		return nil
	}

	found := findByID(t, secretID)
	require.NotNil(t, found, "created secret did not appear in the owner's masked list")
	assert.Equal(t, expectedPrefix, found["value_sha256_prefix"], "list fingerprint mismatch")
	assert.Equal(t, expectedLength, found["value_length"], "list length mismatch")
	assert.Equal(t, expectedClass, found["value_char_class"], "list char class mismatch")

	// --- Rotate ---
	const rotatedValue = "Rotated#Value456"
	rotatedPrefix := secretsRBACFingerprint(rotatedValue)

	resp = env.Post(t, "/api/v1/secrets/"+secretID+"/rotate", map[string]any{"value": rotatedValue})
	raw = env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "owner rotate failed: %s", string(raw))
	assert.NotContains(t, string(raw), rotatedValue, "rotate response echoed the plaintext value")

	var rotated map[string]any
	require.NoError(t, json.Unmarshal(raw, &rotated))
	newSecretID, _ := rotated["id"].(string)
	require.NotEmpty(t, newSecretID)
	assert.NotEqual(t, secretID, newSecretID, "rotate must insert a new row, not edit the old one in place")
	assert.Equal(t, rotatedPrefix, rotated["value_sha256_prefix"], "rotate response fingerprint mismatch")

	var oldRotatedAt *time.Time
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT rotated_at FROM secrets WHERE id = $1", secretID).Scan(&oldRotatedAt))
	assert.NotNil(t, oldRotatedAt, "the superseded row's rotated_at was not stamped")

	var newRotatedAt *time.Time
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT rotated_at FROM secrets WHERE id = $1", newSecretID).Scan(&newRotatedAt))
	assert.Nil(t, newRotatedAt, "the rotated row is not current")

	// --- List reflects only the current row under this name ---
	assert.Nil(t, findByID(t, secretID), "the superseded row is still in the masked list")
	newFound := findByID(t, newSecretID)
	require.NotNil(t, newFound, "the rotated secret is not in the owner's masked list")
	assert.Equal(t, rotatedPrefix, newFound["value_sha256_prefix"])

	// --- Delete ---
	resp = env.Delete(t, "/api/v1/secrets/"+newSecretID)
	body := string(env.ReadBody(t, resp))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "owner delete failed: %s", body)

	assert.Nil(t, findByID(t, newSecretID), "deleted secret still appears in the owner's masked list")

	var deletedRotatedAt *time.Time
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		"SELECT rotated_at FROM secrets WHERE id = $1", newSecretID).Scan(&deletedRotatedAt))
	assert.NotNil(t, deletedRotatedAt, "the deleted row's rotated_at was not stamped")
}
