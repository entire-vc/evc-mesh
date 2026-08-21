//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSecretsPlaintextNeverCrossesTheBoundary is S6 (task #a2c330a8, epic
// #64e84eb1): a CI-gated negative test that walks EVERY surface a value
// could leak through separately, rather than "wrote it, read it back
// through the same layer" — which the parent task explicitly warns proves
// nothing, since the write path and its own read path share whatever bug
// would leak the value.
//
// Surfaces checked, each independently:
//  1. POST /workspaces/:ws_id/secrets (create) response body
//  2. GET  /workspaces/:ws_id/secrets (list) response body, before AND after
//     a rotation — the rotated-out row must stay masked too
//  3. POST /secrets/:secret_id/rotate response body
//  4. GET  /workspaces/:ws_id/activity (the activity feed named explicitly
//     in the parent task) response body
//  5. The activity_log.changes column, read directly from Postgres — a
//     layer below the API entirely, so a bug that only manifests in raw SQL
//     (e.g. a future column added to the JSON without updating the handler)
//     would still be caught
//  6. The secrets.encrypted_value column itself, read directly from
//     Postgres — confirms the value is genuinely encrypted at rest, not
//     merely masked on the way out
//  7. POST /internal/secrets/materialize with a normal user's Bearer token
//     and no spawn header — the ONE endpoint in this codebase that
//     legitimately returns plaintext (see secret_materialize_handler.go)
//     must refuse an ordinary authenticated caller
//
// get_task / recall / MCP payloads are NOT probed here: secrets carry no
// relationship to tasks or Mesh's memory system, so there is no code path
// that could join a task/recall read against the secrets table today.
// Asserting that against an unrelated endpoint would be exactly the
// "vacuous green probe" class the parent task warns about — a test that
// passes whether or not the property it names is true. That absence is
// instead enforced structurally, at every level, by
// TestSecretsEncryptedValue_OnlyReferencedByAllowlistedFiles
// (internal/repository/postgres/secret_repo_boundary_test.go): it scans the
// whole module for any reference to the encrypted column outside an
// explicit allowlist, so a FUTURE get_task/recall/MCP code path that starts
// reading it fails immediately, wherever it's added — which a probe against
// today's endpoints could never do for a surface that doesn't exist yet.
func TestSecretsPlaintextNeverCrossesTheBoundary(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)
	env.Register(t, uniqueEmail("secrets-boundary"), "TestPass123", "Secrets Boundary Owner")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	plaintext1 := "sk-boundary-" + uuid.New().String()
	plaintext2 := "sk-boundary-rotated-" + uuid.New().String()
	// name must satisfy ^[A-Z][A-Z0-9_]*$ (it becomes an env var name) — a
	// raw UUID has lowercase hex digits, so the suffix is uppercased.
	secretName := "BOUNDARY_TEST_TOKEN_" + strings.ToUpper(strings.ReplaceAll(uuid.New().String()[:8], "-", ""))

	// 1. Create.
	resp = env.Post(t, "/api/v1/workspaces/"+wsID+"/secrets", map[string]any{
		"name":  secretName,
		"scope": "workspace",
		"value": plaintext1,
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	raw := env.ReadBody(t, resp)
	assertNoPlaintext(t, "create response", raw, plaintext1)
	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created))
	secretID, _ := created["id"].(string)
	require.NotEmpty(t, secretID)
	env.OnCleanup(func() {
		_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM secrets WHERE name = $1", secretName)
	})

	// 2. List, before rotation.
	resp = env.Get(t, "/api/v1/workspaces/"+wsID+"/secrets")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw = env.ReadBody(t, resp)
	assertNoPlaintext(t, "list response (pre-rotate)", raw, plaintext1)

	// 3. Rotate.
	resp = env.Post(t, "/api/v1/secrets/"+secretID+"/rotate", map[string]any{"value": plaintext2})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw = env.ReadBody(t, resp)
	assertNoPlaintext(t, "rotate response", raw, plaintext1)
	assertNoPlaintext(t, "rotate response", raw, plaintext2)
	var rotated map[string]any
	require.NoError(t, json.Unmarshal(raw, &rotated))
	rotatedSecretID, _ := rotated["id"].(string)
	require.NotEmpty(t, rotatedSecretID)

	// 2b. List again, after rotation — the superseded row must stay masked too.
	resp = env.Get(t, "/api/v1/workspaces/"+wsID+"/secrets")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw = env.ReadBody(t, resp)
	assertNoPlaintext(t, "list response (post-rotate)", raw, plaintext1)
	assertNoPlaintext(t, "list response (post-rotate)", raw, plaintext2)

	// 4. Activity feed — the surface the parent task names explicitly.
	resp = env.Get(t, "/api/v1/workspaces/"+wsID+"/activity")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw = env.ReadBody(t, resp)
	assertNoPlaintext(t, "activity feed response", raw, plaintext1)
	assertNoPlaintext(t, "activity feed response", raw, plaintext2)

	// 5. Raw Postgres check of activity_log.changes — independent of the API
	// layer: a bug that only shows up in the stored JSON (not in what the
	// handler currently chooses to render) would still be caught here.
	var changesLeakCount int
	err := env.DB.GetContext(context.Background(), &changesLeakCount,
		`SELECT COUNT(*) FROM activity_log
		  WHERE entity_type = 'secret'
		    AND (changes::text LIKE '%' || $1 || '%' OR changes::text LIKE '%' || $2 || '%')`,
		plaintext1, plaintext2)
	require.NoError(t, err)
	assert.Equal(t, 0, changesLeakCount, "activity_log.changes must never contain a secret's plaintext value")

	// 6. Raw Postgres check of the encrypted column itself — confirms the
	// value is genuinely encrypted at rest, not merely masked on the way out
	// by a handler that happens to be careful today.
	var storedPlaintextCount int
	err = env.DB.GetContext(context.Background(), &storedPlaintextCount,
		`SELECT COUNT(*) FROM secrets WHERE name = $1 AND (encrypted_value = $2 OR encrypted_value = $3)`,
		secretName, plaintext1, plaintext2)
	require.NoError(t, err)
	assert.Equal(t, 0, storedPlaintextCount, "secrets.encrypted_value must never equal the plaintext value verbatim")

	// 7. The one legitimate plaintext-returning endpoint must refuse a normal
	// authenticated user. env.Post sends this user's Bearer JWT; SpawnAuth
	// looks only at X-Spawn-Token + MESH_INTEGRATION_ENCRYPTION_KEY-adjacent
	// env config, so a valid JWT must never be sufficient on its own.
	resp = env.Post(t, "/internal/secrets/materialize", map[string]any{"workspace_id": wsID})
	raw = env.ReadBody(t, resp)
	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"a normal authenticated user must never reach the materialize endpoint: got 200, body %s", string(raw))
	assertNoPlaintext(t, "materialize response to a non-spawn caller", raw, plaintext1)
	assertNoPlaintext(t, "materialize response to a non-spawn caller", raw, plaintext2)

	// Delete, and confirm the delete confirmation itself carries nothing either.
	resp = env.Delete(t, "/api/v1/secrets/"+rotatedSecretID)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// assertNoPlaintext fails the test if raw contains needle anywhere at all —
// not just outside a "value" JSON field, since a leak could just as easily
// land in an error message, a log-shaped string, or a field named something
// else entirely. Byte-substring search over the whole response is the
// version of this check that can't be defeated by moving the leak to a
// differently-named field.
func assertNoPlaintext(t *testing.T, surface string, body []byte, plaintext string) {
	t.Helper()
	assert.NotContains(t, string(body), plaintext, "%s leaked the plaintext value", surface)
}
