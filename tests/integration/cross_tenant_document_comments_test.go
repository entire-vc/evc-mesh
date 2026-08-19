//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// documentCommentBody is the marker text the negative control looks for in the
// intruder's response. If it ever appears there, the read crossed a tenant
// boundary, whatever the status code said.
const documentCommentBody = "SECRET-COMMENT-4f1b2e07 the failover runbook is wrong, we lost the London region"

// documentCommentQuote is the anchored text. It is asserted on separately from
// the body: an anchor carries a verbatim slice of the document, so a leak of the
// anchor alone still leaks the document.
const documentCommentQuote = "the production database password"

// createDocumentComment leaves an anchored comment on this tenant's document and
// returns its id.
func (tn *tenant) createDocumentComment(t *testing.T, docID string) string {
	t.Helper()

	resp := tn.env.Post(t, "/api/v1/documents/"+docID+"/comments", map[string]any{
		"body": documentCommentBody,
		"anchor": map[string]any{
			"exact":  documentCommentQuote,
			"prefix": "the ",
			"suffix": " is in 1Password",
			"start":  20,
			"end":    52,
		},
	})
	raw := tn.env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: POST /documents/%s/comments returned %d: %s",
		docID, resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode the created comment: %s", string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "the created comment has no id: %s", string(raw))
	return id
}

// TestCrossTenant_DocumentCommentsAreNotReachableFromAnotherWorkspace is the
// negative control for the document comment routes.
//
// The intruder is not a stranger: they are an ordinary member of their OWN
// workspace, holding a valid session, naming an object by an id they were given.
// That is the shape every cross-tenant hole in this repo has had — the caller's
// credentials are genuine and it is the object they name that is somebody else's.
//
// /document-comments/:dcom_id carries no project and no document in its path, so
// nothing about the route says which tenant it addresses. What answers that is
// the :dcom_id resolver in workspaceParamResolvers; without it
// RequireWorkspaceMemberScoped would see a route with nothing to check and wave
// it through, exactly as it did for /events/:event_id.
//
// The body assertions are the ones that matter. A status code alone would not
// notice a handler that answered 200 with the comment attached to an error
// envelope, and the comment text — and the document text quoted in its anchor —
// is the thing the route exists to return.
func TestCrossTenant_DocumentCommentsAreNotReachableFromAnotherWorkspace(t *testing.T) {
	victim := newTenant(t, "xtdc-victim")
	intruder := newTenant(t, "xtdc-intruder")

	docID := victim.createDocument(t, "xtdc-victim runbook")
	commentID := victim.createDocumentComment(t, docID)
	ctx := context.Background()

	// refuses asserts the standard shape: an error status, and none of the
	// victim's text anywhere in the response.
	refuses := func(t *testing.T, what string, resp *http.Response) string {
		t.Helper()
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace %s (status %d, body %s)", what, resp.StatusCode, body)
		assert.NotContains(t, body, documentCommentBody,
			"the comment text reached another tenant via %s", what)
		assert.NotContains(t, body, documentCommentQuote,
			"the anchored document text reached another tenant via %s", what)
		return body
	}

	t.Run("GET /documents/:doc_id/comments", func(t *testing.T) {
		refuses(t, "listed this tenant's document comments",
			intruder.env.Get(t, "/api/v1/documents/"+docID+"/comments"))
	})

	// include_resolved is a filter, not a second door: it must not become a way
	// to reach rows the unfiltered listing refused.
	t.Run("GET /documents/:doc_id/comments?include_resolved=true", func(t *testing.T) {
		refuses(t, "listed this tenant's resolved document comments",
			intruder.env.Get(t, "/api/v1/documents/"+docID+"/comments?include_resolved=true"))
	})

	t.Run("POST /documents/:doc_id/comments", func(t *testing.T) {
		refuses(t, "commented on this tenant's document",
			intruder.env.Post(t, "/api/v1/documents/"+docID+"/comments",
				map[string]any{"body": "injected by a stranger"}))

		var count int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM document_comments WHERE document_id = $1", docID).Scan(&count))
		assert.Equal(t, 1, count, "the cross-tenant comment reached the table")
	})

	// The refusal alone would be compatible with an edit that happened anyway —
	// the handler addresses the row by id — so the row is what gets asserted.
	t.Run("PATCH /document-comments/:dcom_id", func(t *testing.T) {
		refuses(t, "edited this tenant's document comment",
			intruder.env.Patch(t, "/api/v1/document-comments/"+commentID,
				map[string]any{"body": "hijacked"}))

		var body string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT body FROM document_comments WHERE id = $1", commentID).Scan(&body))
		assert.Equal(t, documentCommentBody, body, "the cross-tenant edit reached the row")
	})

	t.Run("POST /document-comments/:dcom_id/resolve", func(t *testing.T) {
		refuses(t, "resolved this tenant's document comment",
			intruder.env.Post(t, "/api/v1/document-comments/"+commentID+"/resolve", map[string]any{}))

		var resolvedAt *string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT resolved_at::text FROM document_comments WHERE id = $1", commentID).Scan(&resolvedAt))
		assert.Nil(t, resolvedAt, "the cross-tenant resolve reached the row")
	})

	t.Run("POST /document-comments/:dcom_id/unresolve", func(t *testing.T) {
		refuses(t, "unresolved this tenant's document comment",
			intruder.env.Post(t, "/api/v1/document-comments/"+commentID+"/unresolve", map[string]any{}))
	})

	t.Run("DELETE /document-comments/:dcom_id", func(t *testing.T) {
		refuses(t, "deleted this tenant's document comment",
			intruder.env.Delete(t, "/api/v1/document-comments/"+commentID))

		var deletedAt *string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT deleted_at::text FROM document_comments WHERE id = $1", commentID).Scan(&deletedAt))
		assert.Nil(t, deletedAt, "the cross-tenant delete reached the row")
	})

	// An agent key is the weaker of the two auth paths and worth stating
	// separately: rbac() short-circuits to a static capability map for agent keys
	// and never looks at the target object's workspace.
	t.Run("agent key from another workspace", func(t *testing.T) {
		refuses(t, "read this tenant's document comments with an agent key",
			intruder.env.GetWithAgentKey(t, "/api/v1/documents/"+docID+"/comments", intruder.agentKey))
	})

	// The positive half. A guard that refused everybody would pass every
	// assertion above and be useless.
	t.Run("the owner still uses their own comments", func(t *testing.T) {
		resp := victim.env.Get(t, "/api/v1/documents/"+docID+"/comments")
		raw := victim.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"the owner was refused their own document's comments: %s", string(raw))

		var page struct {
			Items []struct {
				ID         string `json:"id"`
				Body       string `json:"body"`
				AuthorName string `json:"author_name"`
				Anchor     struct {
					Exact    string `json:"exact"`
					Prefix   string `json:"prefix"`
					Start    *int   `json:"start"`
					End      *int   `json:"end"`
					Orphaned bool   `json:"orphaned"`
				} `json:"anchor"`
			} `json:"items"`
		}
		require.NoError(t, json.Unmarshal(raw, &page), "cannot decode the listing: %s", string(raw))
		require.Len(t, page.Items, 1)

		got := page.Items[0]
		assert.Equal(t, commentID, got.ID)
		assert.Equal(t, documentCommentBody, got.Body)
		assert.NotEmpty(t, got.AuthorName, "the author is resolved to a display name, not left as a bare id")

		// The anchor round-trips whole. Offsets without the quote could not be
		// re-found after an edit; the quote without offsets is an orphan.
		assert.Equal(t, documentCommentQuote, got.Anchor.Exact)
		assert.Equal(t, "the ", got.Anchor.Prefix)
		require.NotNil(t, got.Anchor.Start)
		assert.Equal(t, 20, *got.Anchor.Start)
		require.NotNil(t, got.Anchor.End)
		assert.Equal(t, 52, *got.Anchor.End)
		assert.False(t, got.Anchor.Orphaned)

		// And the owner can resolve their own thread, which is what the intruder
		// was refused two subtests up.
		resolveResp := victim.env.Post(t, "/api/v1/document-comments/"+commentID+"/resolve", map[string]any{})
		resolveRaw := victim.env.ReadBody(t, resolveResp)
		require.Equal(t, http.StatusOK, resolveResp.StatusCode,
			"the owner was refused resolving their own thread: %s", string(resolveRaw))

		var resolved map[string]any
		require.NoError(t, json.Unmarshal(resolveRaw, &resolved))
		assert.NotNil(t, resolved["resolved_at"])
		assert.NotEmpty(t, resolved["resolved_by_name"],
			fmt.Sprintf("who resolved it must come back as a name: %s", string(resolveRaw)))
	})
}

// TestCrossTenant_DocumentMetadataDoesNotLeakEditorNames covers the other half of
// this change: documents now carry "created by X, last updated by Y", and those
// are people's names.
//
// A byline is a display name and an id, so a route that leaked a document to
// another tenant would now leak who works there as well. The positive half is
// what proves the guard is not simply refusing everyone.
func TestCrossTenant_DocumentMetadataDoesNotLeakEditorNames(t *testing.T) {
	victim := newTenant(t, "xtdm-victim")
	intruder := newTenant(t, "xtdm-intruder")

	docID := victim.createDocument(t, "xtdm-victim runbook")

	// An edit, so updated_by is somebody rather than nothing. base_version is 1:
	// the document was created a line ago and nothing has written to it since,
	// and a PATCH without one is refused before it ever reaches the tenant check.
	resp := victim.env.Patch(t, "/api/v1/documents/"+docID,
		map[string]any{"title": "xtdm-victim runbook v2", "base_version": 1})
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", string(victim.env.ReadBody(t, resp)))

	t.Run("the intruder sees neither the document nor its byline", func(t *testing.T) {
		resp := intruder.env.Get(t, "/api/v1/documents/"+docID)
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace read this tenant's document (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotContains(t, body, "updated_by_name",
			"the byline reached another tenant")
	})

	t.Run("the owner gets a complete byline", func(t *testing.T) {
		resp := victim.env.Get(t, "/api/v1/documents/"+docID)
		raw := victim.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", string(raw))

		var doc struct {
			CreatedBy     string  `json:"created_by"`
			CreatedByType string  `json:"created_by_type"`
			CreatedByName *string `json:"created_by_name"`
			UpdatedBy     *string `json:"updated_by"`
			UpdatedByType *string `json:"updated_by_type"`
			UpdatedByName *string `json:"updated_by_name"`
		}
		require.NoError(t, json.Unmarshal(raw, &doc), "cannot decode the document: %s", string(raw))

		assert.NotEmpty(t, doc.CreatedBy)
		assert.Equal(t, "user", doc.CreatedByType)
		require.NotNil(t, doc.CreatedByName, "the creator has no resolved name: %s", string(raw))
		assert.NotEmpty(t, *doc.CreatedByName)

		require.NotNil(t, doc.UpdatedBy, "the edit did not record a last editor: %s", string(raw))
		require.NotNil(t, doc.UpdatedByType)
		assert.Equal(t, "user", *doc.UpdatedByType)
		require.NotNil(t, doc.UpdatedByName, "the last editor has no resolved name: %s", string(raw))
		assert.NotEmpty(t, *doc.UpdatedByName)
	})
}
