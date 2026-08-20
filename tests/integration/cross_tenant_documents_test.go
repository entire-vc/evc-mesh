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

// documentBody is the marker content the negative control looks for in the
// intruder's response: if it ever appears there, the read crossed a tenant
// boundary, whatever the status code said.
const documentBody = "# victim runbook\nthe production database password is in 1Password\n"

// createDocument creates a document in this tenant's project and returns its id.
//
// A document body lives in object storage, so this is the one fixture in the
// cross-tenant suite that can fail for an environmental reason. It skips — and
// only — when storage is absent, the same rule uploadIconForRead follows: any
// other failure is a defect and must be loud.
func (tn *tenant) createDocument(t *testing.T, title string) string {
	t.Helper()

	resp := tn.env.Post(t, "/api/v1/projects/"+tn.projectID+"/documents",
		map[string]any{"title": title, "body": documentBody})
	raw := tn.env.ReadBody(t, resp)

	if resp.StatusCode == http.StatusServiceUnavailable ||
		(resp.StatusCode == http.StatusInternalServerError && strings.Contains(string(raw), "storage")) {
		t.Skipf("object storage not available in this environment, skipping: %s", string(raw))
	}
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: POST /projects/%s/documents returned %d: %s",
		tn.projectID, resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode the created document: %s", string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "the created document has no id: %s", string(raw))
	return id
}

// TestCrossTenant_DocumentIsNotReadableFromAnotherWorkspace is the negative
// control for the document routes.
//
// The intruder is not a stranger: they are an ordinary member of their OWN
// workspace, holding a valid session, asking for a document by an id they were
// given. That is the shape every cross-tenant hole in this repo has had — the
// caller's own credentials are genuine and it is the object they name that is
// somebody else's.
//
// GET /documents/:doc_id carries no project in its path, so nothing about the
// route says which tenant it addresses. What answers that is the :doc_id
// resolver in workspaceParamResolvers; without it RequireWorkspaceMemberScoped
// would see a route with nothing to check and wave it through, exactly as it did
// for /events/:event_id.
//
// The body assertion is the one that matters. A status code alone would not
// notice a handler that answered 200 with the document attached to an error
// envelope, and the whole point of the route is the markdown it returns.
func TestCrossTenant_DocumentIsNotReadableFromAnotherWorkspace(t *testing.T) {
	victim := newTenant(t, "xtd-victim")
	intruder := newTenant(t, "xtd-intruder")

	docID := victim.createDocument(t, "xtd-victim runbook")
	ctx := context.Background()

	t.Run("GET /documents/:doc_id", func(t *testing.T) {
		resp := intruder.env.Get(t, "/api/v1/documents/"+docID)
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace read this tenant's document (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotContains(t, body, "production database password",
			"the document body reached another tenant")
	})

	t.Run("PATCH /documents/:doc_id", func(t *testing.T) {
		resp := intruder.env.Patch(t, "/api/v1/documents/"+docID,
			map[string]any{"title": "hijacked", "body": "overwritten"})
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace edited this tenant's document (status %d, body %s)",
			resp.StatusCode, body)

		var title string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT title FROM documents WHERE id = $1", docID).Scan(&title))
		assert.Equal(t, "xtd-victim runbook", title, "the cross-tenant rename reached the row")
	})

	// The refusal alone would be compatible with a delete that happened anyway —
	// the handler addresses the row by id — so the row is what gets asserted.
	t.Run("DELETE /documents/:doc_id", func(t *testing.T) {
		resp := intruder.env.Delete(t, "/api/v1/documents/"+docID)
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace deleted this tenant's document (status %d, body %s)",
			resp.StatusCode, body)

		var deletedAt *string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT deleted_at::text FROM documents WHERE id = $1", docID).Scan(&deletedAt))
		assert.Nil(t, deletedAt, "the cross-tenant delete reached the row")
	})

	// The list route names the project, which is where the composite holes lived:
	// the intruder's own membership is real, the project id is not theirs.
	t.Run("GET /projects/:proj_id/documents", func(t *testing.T) {
		resp := intruder.env.Get(t, "/api/v1/projects/"+victim.projectID+"/documents")
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace listed this tenant's documents (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotContains(t, body, "xtd-victim runbook", "the document titles reached another tenant")
	})

	// The victim can still read their own document — a guard that refuses
	// everybody would pass every assertion above and be useless.
	t.Run("the owner still reads it", func(t *testing.T) {
		resp := victim.env.Get(t, "/api/v1/documents/"+docID)
		raw := victim.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "the owner was refused their own document: %s", string(raw))

		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc))
		assert.Equal(t, documentBody, doc["body"], "the markdown body did not come back with the metadata")
	})
}

// TestCrossTenant_DocumentIsNotReadableFromAnotherWorkspaceForAgentKeys runs the
// same read against an agent key rather than a user JWT.
//
// It is not redundant: rbac() short-circuits to a static capability map for agent
// keys and never looks at the target object's workspace, so an agent key is the
// weaker of the two paths and the one worth stating separately.
func TestCrossTenant_DocumentIsNotReadableFromAnotherWorkspaceForAgentKeys(t *testing.T) {
	victim := newTenant(t, "xtda-victim")
	intruder := newTenant(t, "xtda-intruder")

	docID := victim.createDocument(t, "xtda-victim runbook")

	resp := intruder.env.GetWithAgentKey(t, "/api/v1/documents/"+docID, intruder.agentKey)
	body := string(intruder.env.ReadBody(t, resp))

	assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
		"an agent key from another workspace read this tenant's document (status %d, body %s)",
		resp.StatusCode, body)
	assert.NotContains(t, body, "production database password",
		fmt.Sprintf("the document body reached another tenant's agent (status %d)", resp.StatusCode))
}
