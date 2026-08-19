//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachmentBytes is the marker content the negative control looks for in the
// intruder's response. If it ever appears there — or if the intruder gets a
// presigned URL that serves it — the read crossed a tenant boundary, whatever the
// status code said.
const attachmentBytes = "SECRET-ATTACHMENT-a7f3e91c-the-quarterly-numbers"

// createDocumentAttachment uploads a file into this tenant's document and returns
// the attachment's id.
//
// Like createDocument it skips — and only skips — when object storage is absent:
// that is the one environmental reason this fixture can legitimately fail. Any
// other failure is a defect and must be loud.
func (tn *tenant) createDocumentAttachment(t *testing.T, docID, filename string) string {
	t.Helper()

	resp := uploadDocumentAttachmentRaw(t, tn.env, docID, filename, []byte(attachmentBytes))
	raw := tn.env.ReadBody(t, resp)

	if resp.StatusCode == http.StatusServiceUnavailable ||
		(resp.StatusCode == http.StatusInternalServerError && strings.Contains(string(raw), "storage")) {
		t.Skipf("object storage not available in this environment, skipping: %s", string(raw))
	}
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: POST /documents/%s/attachments returned %d: %s",
		docID, resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode the created attachment: %s", string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "the created attachment has no id: %s", string(raw))
	return id
}

// uploadDocumentAttachmentRaw performs the multipart upload and returns the raw
// response — TestEnv.Post only speaks JSON.
func uploadDocumentAttachmentRaw(t *testing.T, env *TestEnv, docID, filename string, content []byte) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req, err := http.NewRequest(http.MethodPost,
		env.BaseURL+"/api/v1/documents/"+docID+"/attachments", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if env.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+env.AuthToken)
	}

	resp, err := env.HTTPClient.Do(req)
	require.NoError(t, err)
	return resp
}

// fetchPresignedURL follows a presigned URL with NO credentials of ours — that is
// the point of a presigned URL, and it is how a test can tell whether one that
// leaked would actually serve the bytes.
func fetchPresignedURL(t *testing.T, env *TestEnv, url string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	resp, err := env.HTTPClient.Do(req)
	if err != nil {
		// The presign points at whatever S3_PUBLIC_URL says, which may not be
		// reachable from the test process. That is not a verdict about the guard.
		t.Skipf("presigned URL is not reachable from this environment: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// TestCrossTenant_DocumentAttachmentIsNotReachableFromAnotherWorkspace is the
// negative control for the document-attachment routes.
//
// The intruder is not a stranger: they are an ordinary member of their OWN
// workspace, holding a valid session, naming an attachment by an id they were
// given. That is the shape every cross-tenant hole in this repo has had.
//
// /document-attachments/:att_id carries neither a project nor a document in its
// path, so nothing about the route says which tenant it addresses. Two
// independent layers answer that question: the :att_id resolver in
// workspaceParamResolvers, which is what lets RequireWorkspaceMemberScoped fire
// at all, and the handler's own workspace-scoped lookup behind it.
//
// What this test does and does not prove, stated plainly because the distinction
// is easy to get wrong: it proves the BEHAVIOUR — an ordinary member of another
// workspace reaches none of these four routes. It is NOT a discriminating control
// for the resolver. Measured on 2026-08-19 by deleting the :att_id entry,
// rebuilding and re-running this test against real Postgres and MinIO: every
// assertion here still passed, and the intruder still got 403, because with no
// resolver the workspace falls back to the caller's own and the handler's
// GetByIDInWorkspace refuses on that instead. Defence in depth working as
// designed — but it means a green run here says nothing about whether the
// resolver exists.
//
// The control that IS discriminating for the resolver is
// TestEveryIdentifiedRouteIsWorkspaceScoped in internal/middleware, which was
// verified to fail with "/document-attachments/:att_id (unchecked: :att_id)" when
// the entry is removed. Both tests are needed; neither substitutes for the other.
//
// The download route is the one that matters most here, and it is a worse leak
// than a normal read: it does not return the bytes, it returns a presigned URL
// that anyone can then fetch WITHOUT any credential at all. A refusal that leaked
// the URL in the body would look like a refusal and be a full disclosure.
func TestCrossTenant_DocumentAttachmentIsNotReachableFromAnotherWorkspace(t *testing.T) {
	victim := newTenant(t, "xta-victim")
	intruder := newTenant(t, "xta-intruder")

	docID := victim.createDocument(t, "xta-victim runbook")
	attID := victim.createDocumentAttachment(t, docID, "quarterly-numbers.png")
	ctx := context.Background()

	t.Run("GET /document-attachments/:att_id/download", func(t *testing.T) {
		resp := intruder.env.Get(t, "/api/v1/document-attachments/"+attID+"/download?disposition=inline")
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace got a download URL for this tenant's attachment (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotContains(t, body, attachmentBytes,
			"the attachment content reached another tenant")
		// A presigned URL in the body is the disclosure, whatever the status code
		// on the response that carried it.
		assert.NotContains(t, body, "X-Amz-Signature",
			"a presigned URL reached another tenant — it needs no credential to follow")
	})

	t.Run("GET /documents/:doc_id/attachments", func(t *testing.T) {
		resp := intruder.env.Get(t, "/api/v1/documents/"+docID+"/attachments")
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace listed this tenant's attachments (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotContains(t, body, "quarterly-numbers.png",
			"the attachment names reached another tenant")
	})

	t.Run("POST /documents/:doc_id/attachments", func(t *testing.T) {
		resp := uploadDocumentAttachmentRaw(t, intruder.env, docID, "planted.png", []byte("planted by the intruder"))
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace attached a file to this tenant's document (status %d, body %s)",
			resp.StatusCode, body)

		// The refusal alone would be compatible with a row written anyway, so the
		// row is what gets asserted.
		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM document_attachments WHERE document_id = $1", docID).Scan(&n))
		assert.Equal(t, 1, n, "the cross-tenant upload reached the table")
	})

	// The refusal alone would be compatible with a delete that happened anyway —
	// the handler addresses the row by id — so the row is what gets asserted.
	t.Run("DELETE /document-attachments/:att_id", func(t *testing.T) {
		resp := intruder.env.Delete(t, "/api/v1/document-attachments/"+attID)
		body := string(intruder.env.ReadBody(t, resp))

		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace deleted this tenant's attachment (status %d, body %s)",
			resp.StatusCode, body)

		var deletedAt *string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT deleted_at::text FROM document_attachments WHERE id = $1", attID).Scan(&deletedAt))
		assert.Nil(t, deletedAt, "the cross-tenant delete reached the row")
	})

	// The owner can still reach it — four refusals prove nothing on their own,
	// because a guard that refused everybody would satisfy every one of them.
	t.Run("the owner still downloads it", func(t *testing.T) {
		resp := victim.env.Get(t, "/api/v1/document-attachments/"+attID+"/download?disposition=inline")
		raw := victim.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"the owner was refused their own attachment: %s", string(raw))

		var got map[string]string
		require.NoError(t, json.Unmarshal(raw, &got))
		require.NotEmpty(t, got["url"], "no presigned URL came back: %s", string(raw))

		status, body := fetchPresignedURL(t, victim.env, got["url"])
		assert.Equal(t, http.StatusOK, status, "the owner's presigned URL did not serve the object: %s", body)
		assert.Contains(t, body, attachmentBytes, "the presigned URL served the wrong object")
	})

	t.Run("the owner still lists it", func(t *testing.T) {
		resp := victim.env.Get(t, "/api/v1/documents/"+docID+"/attachments")
		raw := victim.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "the owner was refused their own listing: %s", string(raw))
		assert.Contains(t, string(raw), "quarterly-numbers.png")
	})
}

// TestCrossTenant_DocumentAttachmentIsNotReachableForAgentKeys runs the same read
// against an agent key rather than a user JWT.
//
// It is not redundant: rbac() short-circuits to a static capability map for agent
// keys and never looks at the target object's workspace, so an agent key is the
// weaker of the two paths and the one worth stating separately.
func TestCrossTenant_DocumentAttachmentIsNotReachableForAgentKeys(t *testing.T) {
	victim := newTenant(t, "xtaa-victim")
	intruder := newTenant(t, "xtaa-intruder")

	docID := victim.createDocument(t, "xtaa-victim runbook")
	attID := victim.createDocumentAttachment(t, docID, "xtaa-numbers.png")

	resp := intruder.env.GetWithAgentKey(t,
		"/api/v1/document-attachments/"+attID+"/download?disposition=inline", intruder.agentKey)
	body := string(intruder.env.ReadBody(t, resp))

	assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
		"an agent key from another workspace got a download URL for this tenant's attachment (status %d, body %s)",
		resp.StatusCode, body)
	assert.NotContains(t, body, attachmentBytes,
		fmt.Sprintf("the attachment content reached another tenant's agent (status %d)", resp.StatusCode))
	assert.NotContains(t, body, "X-Amz-Signature",
		"a presigned URL reached another tenant's agent")
}

// TestDocumentAttachment_ReferenceOutlivesItsPresignedURL is the honest end of the
// "the image still opens an hour later" criterion.
//
// The claim decomposes into two halves, and only both together mean anything:
//
//  1. What gets STORED in a markdown body carries no signature material, so it has
//     no expiry of its own and cannot go stale however long the page sits there.
//  2. What gets SERVED is minted at the moment of viewing — signed with the
//     current time, valid for an hour from then.
//
// Given both, the age of the stored text is irrelevant. Given only the first, the
// image is permanently broken; given only the second, the reference expires with
// the URL and (2) never gets a chance to run.
//
// Note on what is NOT asserted: two resolves in the same second return the
// IDENTICAL URL, and that is correct, not a bug. An AWS SigV4 presign is a pure
// function of (key, expiry, credentials, X-Amz-Date), and X-Amz-Date has
// one-second granularity — so a same-second re-sign is bit-identical by
// construction, and both copies are equally valid for the next hour. The property
// that actually distinguishes a live signer from a cached URL is that the
// signature tracks the CLOCK, which is what the sleep below makes observable.
func TestDocumentAttachment_ReferenceOutlivesItsPresignedURL(t *testing.T) {
	victim := newTenant(t, "xtae-owner")

	docID := victim.createDocument(t, "xtae runbook")
	attID := victim.createDocumentAttachment(t, docID, "xtae-diagram.png")

	// (1) The reference a markdown body would store. It is built from the id
	// alone — this is the thing that must not expire.
	storedRef := "/api/v1/document-attachments/" + attID + "/download?disposition=inline"
	assert.NotContains(t, storedRef, "X-Amz-",
		"the stored reference carries signature material, so it has an expiry of its own")
	assert.NotContains(t, storedRef, "Signature")

	resolve := func() string {
		resp := victim.env.Get(t, storedRef)
		raw := victim.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "resolving the stored reference failed: %s", string(raw))
		var got map[string]string
		require.NoError(t, json.Unmarshal(raw, &got))
		require.NotEmpty(t, got["url"])
		return got["url"]
	}

	first := resolve()

	// (2a) What is served IS time-limited — otherwise "it expires" is not a
	// property of this system at all and the rest of the test is about nothing.
	assert.Contains(t, first, "X-Amz-Signature=", "the served URL is not presigned")
	assert.Contains(t, first, "X-Amz-Expires=", "the served URL has no expiry window")

	status, body := fetchPresignedURL(t, victim.env, first)
	require.Equal(t, http.StatusOK, status, "the freshly signed URL did not serve the object: %s", body)
	assert.Contains(t, body, attachmentBytes)

	// (2b) And the signature tracks the clock rather than being minted once and
	// kept. Two seconds is enough to cross a X-Amz-Date boundary; a service that
	// cached the presign on the row would return the byte-identical URL here and
	// would start serving 403s one hour after that single mint.
	time.Sleep(2 * time.Second)
	later := resolve()
	assert.NotEqual(t, first, later,
		"the same stored reference resolved two seconds apart returned the identical URL — "+
			"the presign is being cached rather than minted per view, so it expires once and "+
			"the image breaks with it")
	assert.NotEqual(t, amzDateOf(t, first), amzDateOf(t, later),
		"the signature did not advance with the clock")

	// And the aged reference still serves the object, which is the user-visible
	// claim. Two seconds is not an hour, but once the stored text carries no
	// credential the property under test is "resolution happens per view", and
	// that is not time-dependent.
	status, body = fetchPresignedURL(t, victim.env, later)
	assert.Equal(t, http.StatusOK, status,
		"the stored reference stopped serving the object after it aged: %s", body)
	assert.Contains(t, body, attachmentBytes)
}

// amzDateOf extracts X-Amz-Date from a presigned URL. It is the field that makes
// a re-sign observable: everything else in the signature's input is constant for
// a given attachment.
func amzDateOf(t *testing.T, presigned string) string {
	t.Helper()
	u, err := neturl.Parse(presigned)
	require.NoError(t, err)
	d := u.Query().Get("X-Amz-Date")
	require.NotEmpty(t, d, "no X-Amz-Date in %s", presigned)
	return d
}
