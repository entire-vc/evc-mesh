//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two-step uploads (docs/mcp-reference.md "Uploading files"): declare, PUT the
// bytes to the presigned URL with no credentials, confirm. Each test drives the
// running API and real object storage; nothing in between is stubbed.

// uploadFixture is one tenant with a project member agent, a document and a
// task, which is what an agent on a remote MCP transport has.
type uploadFixture struct {
	tn       *tenant
	agentID  string
	agentKey string
	docID    string
}

func newUploadFixture(t *testing.T, prefix string) *uploadFixture {
	t.Helper()
	tn := newTenant(t, prefix)
	agentID, agentKey := tn.env.CreateAgent(t, tn.wsID, prefix+" uploader")
	enrol := tn.env.Post(t, fmt.Sprintf("/api/v1/projects/%s/members/agents", tn.projectID),
		map[string]any{"agent_id": agentID, "role": "member"})
	require.Equal(t, http.StatusCreated, enrol.StatusCode, string(tn.env.ReadBody(t, enrol)))
	f := &uploadFixture{tn: tn, agentID: agentID, agentKey: agentKey, docID: tn.createDocument(t, prefix+" doc")}
	tn.env.OnCleanup(func() {
		_, _ = tn.env.DB.ExecContext(context.Background(), "DELETE FROM upload_sessions WHERE workspace_id = $1", tn.wsID)
	})
	return f
}

// pngBytes is a real PNG header followed by size-8 random bytes: the type
// sniffer reads the signature, the rest makes every file unique.
func pngBytes(t *testing.T, size int) []byte {
	t.Helper()
	b := make([]byte, size)
	_, err := rand.Read(b)
	require.NoError(t, err)
	copy(b, []byte("\x89PNG\r\n\x1a\n"))
	return b
}

func sha256Of(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// agentJSON sends a JSON request with only the agent key, plus extra headers.
func (f *uploadFixture) agentJSON(t *testing.T, method, path, key string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.tn.env.BaseURL+path, r)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Agent-Key", key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.tn.env.HTTPClient.Do(req)
	require.NoError(t, err)
	raw := f.tn.env.ReadBody(t, resp)
	var out map[string]any
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out), "%s %s answered non-JSON %d: %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

// declare creates an upload and returns its id and URL; a storage-less
// environment skips, as every storage fixture here does.
func (f *uploadFixture) declare(t *testing.T, path string, body map[string]any) (string, string) {
	t.Helper()
	code, out := f.agentJSON(t, http.MethodPost, path, f.agentKey, body, nil)
	if code == http.StatusServiceUnavailable {
		t.Skipf("object storage not available in this environment: %v", out)
	}
	require.Equal(t, http.StatusCreated, code, "declare %s: %v", path, out)
	assert.Equal(t, "PUT", out["upload_method"])
	assert.Equal(t, map[string]any{}, out["upload_headers"], "the contract promises no headers to set by hand")
	id, _ := out["upload_id"].(string)
	url, _ := out["upload_url"].(string)
	require.NotEmpty(t, id)
	require.NotEmpty(t, url)
	return id, url
}

// putBare PUTs the bytes with no Authorization, no agent key and no
// Content-Type: what an agent's plain HTTP client sends.
func putBare(t *testing.T, url string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "bare PUT to upload_url: %s", raw)
	return resp.StatusCode
}

func (f *uploadFixture) confirm(t *testing.T, id string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	return f.agentJSON(t, http.MethodPost, "/api/v1/uploads/"+id+"/confirm", f.agentKey, nil, headers)
}

// assertInlineDownload follows download_path as the agent and fetches the
// presigned URL it answers with: 200, the declared type, no attachment
// disposition, and the exact bytes.
func (f *uploadFixture) assertInlineDownload(t *testing.T, downloadPath string, want []byte) {
	t.Helper()
	code, out := f.agentJSON(t, http.MethodGet, downloadPath, f.agentKey, nil, nil)
	require.Equal(t, http.StatusOK, code, "GET %s: %v", downloadPath, out)
	url, _ := out["url"].(string)
	require.NotEmpty(t, url, "no presigned url in %v", out)
	resp, err := http.Get(url) //nolint:gosec,noctx // the URL is the API's own presigned download
	require.NoError(t, err)
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "presigned GET: %s", got)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	assert.NotContains(t, strings.ToLower(resp.Header.Get("Content-Disposition")), "attachment",
		"an inline image must not be forced to download")
	assert.True(t, bytes.Equal(want, got), "downloaded bytes differ from the uploaded ones")
}

// TestTwoStepUpload_DocumentAttachment is AC1: a 500 KB PNG goes into a
// document through a bare PUT, the confirm answers 201 with markdown that
// carries the stable download_path, the row is in the database, and the
// download serves image/png inline.
func TestTwoStepUpload_DocumentAttachment(t *testing.T) {
	f := newUploadFixture(t, "upl-doc")
	png := pngBytes(t, 500*1000)
	id, url := f.declare(t, "/api/v1/documents/"+f.docID+"/attachments/uploads", map[string]any{
		"name": "diagram.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png),
	})
	assert.Contains(t, url, "/uploads/"+f.tn.wsID+"/"+id+"?", "the PUT goes to the staging key")
	putBare(t, url, png)

	code, out := f.confirm(t, id, nil)
	require.Equal(t, http.StatusCreated, code, "confirm: %v", out)
	attID, _ := out["attachment_id"].(string)
	require.NotEmpty(t, attID)
	path := "/api/v1/document-attachments/" + attID + "/download?disposition=inline"
	assert.Equal(t, "document_attachment", out["kind"])
	assert.Equal(t, f.docID, out["document_id"])
	assert.Equal(t, path, out["download_path"])
	assert.Equal(t, "![diagram.png]("+path+")", out["markdown"])
	assert.Equal(t, true, out["renders_inline"])
	assert.Equal(t, sha256Of(png), out["sha256"])
	assert.EqualValues(t, len(png), out["size_bytes"])

	// Re-read after the save: the row exists, under the document, with the
	// counted size and the declared type.
	var row struct {
		DocumentID string `db:"document_id"`
		Size       int64  `db:"size_bytes"`
		Mime       string `db:"mime_type"`
		By         string `db:"uploaded_by"`
	}
	require.NoError(t, f.tn.env.DB.GetContext(context.Background(), &row,
		"SELECT document_id, size_bytes, mime_type, uploaded_by FROM document_attachments WHERE id = $1", attID))
	assert.Equal(t, f.docID, row.DocumentID)
	assert.EqualValues(t, len(png), row.Size)
	assert.Equal(t, "image/png", row.Mime)
	assert.Equal(t, f.agentID, row.By)

	f.assertInlineDownload(t, path, png)

	// Repeated confirm: 200, same attachment, still exactly one row.
	code, again := f.confirm(t, id, nil)
	require.Equal(t, http.StatusOK, code, "repeat confirm: %v", again)
	assert.Equal(t, attID, again["attachment_id"])
	var n int
	require.NoError(t, f.tn.env.DB.GetContext(context.Background(), &n,
		"SELECT count(*) FROM document_attachments WHERE document_id = $1 AND name = 'diagram.png'", f.docID))
	assert.Equal(t, 1, n, "a repeated confirm wrote a second row")
}

// checkout takes the task's lock as the fixture agent and returns its token.
func (f *uploadFixture) checkout(t *testing.T) string {
	t.Helper()
	code, out := f.agentJSON(t, http.MethodPost, "/api/v1/tasks/"+f.tn.taskID+"/checkout", f.agentKey,
		map[string]any{}, nil)
	require.Equal(t, http.StatusOK, code, "checkout: %v", out)
	tok, _ := out["checkout_token"].(string)
	require.NotEmpty(t, tok)
	return tok
}

// TestTwoStepUpload_ArtifactUnderCheckout is AC2: the same flow for a task
// artifact, fenced by the agent's checkout. A stale fence is refused and
// writes nothing; the right fence writes one artifact with its checksum.
func TestTwoStepUpload_ArtifactUnderCheckout(t *testing.T) {
	f := newUploadFixture(t, "upl-art")
	token := f.checkout(t)
	png := pngBytes(t, 500*1000)
	id, url := f.declare(t, "/api/v1/tasks/"+f.tn.taskID+"/artifacts/uploads", map[string]any{
		"name": "screenshot.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png),
		"artifact_type": "image",
	})
	putBare(t, url, png)

	countArtifacts := func() int {
		var n int
		require.NoError(t, f.tn.env.DB.GetContext(context.Background(), &n,
			"SELECT count(*) FROM artifacts WHERE task_id = $1", f.tn.taskID))
		return n
	}

	// Negative: a fence naming a lock the agent does not hold.
	code, out := f.confirm(t, id, map[string]string{"X-Mesh-Checkout-Token": "00000000-0000-4000-8000-000000000001"})
	require.Equal(t, http.StatusConflict, code, "stale fence: %v", out)
	assert.Equal(t, "checkout_precondition_failed", out["error_code"])
	assert.Equal(t, 0, countArtifacts(), "a refused fenced confirm wrote an artifact")

	// The staged bytes and the session survive the refusal: confirm again
	// with the real fence.
	code, out = f.confirm(t, id, map[string]string{"X-Mesh-Checkout-Token": token})
	require.Equal(t, http.StatusCreated, code, "fenced confirm: %v", out)
	artID, _ := out["artifact_id"].(string)
	require.NotEmpty(t, artID)
	path := "/api/v1/artifacts/" + artID + "/download?disposition=inline"
	assert.Equal(t, "artifact", out["kind"])
	assert.Equal(t, f.tn.taskID, out["task_id"])
	assert.Equal(t, path, out["download_path"])
	assert.Equal(t, "![screenshot.png]("+path+")", out["markdown"])
	assert.Equal(t, true, out["renders_inline"])

	var row struct {
		TaskID   string `db:"task_id"`
		Size     int64  `db:"size_bytes"`
		Checksum string `db:"checksum_sha256"`
		Type     string `db:"artifact_type"`
	}
	require.NoError(t, f.tn.env.DB.GetContext(context.Background(), &row,
		"SELECT task_id, size_bytes, checksum_sha256, artifact_type FROM artifacts WHERE id = $1", artID))
	assert.Equal(t, f.tn.taskID, row.TaskID)
	assert.EqualValues(t, len(png), row.Size)
	assert.Equal(t, sha256Of(png), row.Checksum, "checksum_sha256 is stored from the verified upload")
	assert.Equal(t, "image", row.Type)
	assert.Equal(t, 1, countArtifacts())

	f.assertInlineDownload(t, path, png)

	code, again := f.confirm(t, id, map[string]string{"X-Mesh-Checkout-Token": token})
	require.Equal(t, http.StatusOK, code, "repeat confirm: %v", again)
	assert.Equal(t, artID, again["artifact_id"])
	assert.Equal(t, 1, countArtifacts(), "a repeated confirm wrote a second artifact")
}

// TestTwoStepUpload_Refusals is AC3: every refusal of the contract, each
// against a real upload.
func TestTwoStepUpload_Refusals(t *testing.T) {
	f := newUploadFixture(t, "upl-neg")
	docUploads := "/api/v1/documents/" + f.docID + "/attachments/uploads"

	t.Run("OversizeDeclarationIs422FileTooLarge", func(t *testing.T) {
		code, out := f.agentJSON(t, http.MethodPost, docUploads, f.agentKey, map[string]any{
			"name": "big.bin", "mime_type": "application/octet-stream",
			"size_bytes": 25<<20 + 1, "sha256": strings.Repeat("a", 64),
		}, nil)
		require.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "file_too_large", out["error_code"])
		ctx, _ := out["context"].(map[string]any)
		assert.EqualValues(t, 25<<20, ctx["limit_bytes"])
		// Positive control: exactly the limit is accepted.
		code, out = f.agentJSON(t, http.MethodPost, docUploads, f.agentKey, map[string]any{
			"name": "max.bin", "mime_type": "application/octet-stream",
			"size_bytes": 25 << 20, "sha256": strings.Repeat("a", 64),
		}, nil)
		require.Equal(t, http.StatusCreated, code, "%v", out)
	})

	t.Run("CorruptedByteIs422ChecksumMismatchAndStagedRemoved", func(t *testing.T) {
		png := pngBytes(t, 4096)
		declared := sha256Of(png)
		id, url := f.declare(t, docUploads, map[string]any{
			"name": "c.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": declared,
		})
		bad := append([]byte(nil), png...)
		bad[2000] ^= 0xff
		putBare(t, url, bad)
		code, out := f.confirm(t, id, nil)
		require.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "checksum_mismatch", out["error_code"])
		ctx, _ := out["context"].(map[string]any)
		assert.Equal(t, declared, ctx["declared"])
		assert.Equal(t, sha256Of(bad), ctx["received"])
		// The staged object is gone: confirming again finds nothing uploaded.
		code, out = f.confirm(t, id, nil)
		require.Equal(t, http.StatusConflict, code, "%v", out)
		assert.Equal(t, "upload_not_received", out["error_code"])
		// The session is still open: PUT the right bytes and confirm.
		putBare(t, url, png)
		code, out = f.confirm(t, id, nil)
		require.Equal(t, http.StatusCreated, code, "%v", out)
	})

	t.Run("ConfirmWithoutPutIs409", func(t *testing.T) {
		id, _ := f.declare(t, docUploads, map[string]any{
			"name": "n.png", "mime_type": "image/png", "size_bytes": 10, "sha256": strings.Repeat("b", 64),
		})
		code, out := f.confirm(t, id, nil)
		require.Equal(t, http.StatusConflict, code, "%v", out)
		assert.Equal(t, "upload_not_received", out["error_code"])
	})

	t.Run("ForeignUploadIdIs404", func(t *testing.T) {
		id, _ := f.declare(t, docUploads, map[string]any{
			"name": "f.png", "mime_type": "image/png", "size_bytes": 10, "sha256": strings.Repeat("c", 64),
		})
		// Another agent of the same workspace.
		_, sameWsKey := f.tn.env.CreateAgent(t, f.tn.wsID, "upl-neg other agent")
		code, out := f.agentJSON(t, http.MethodPost, "/api/v1/uploads/"+id+"/confirm", sameWsKey, nil, nil)
		assert.Equal(t, http.StatusNotFound, code, "same workspace, other agent: %v", out)
		// An agent of another workspace.
		stranger := newTenant(t, "upl-neg-stranger")
		code, out = f.agentJSON(t, http.MethodPost, "/api/v1/uploads/"+id+"/confirm", stranger.agentKey, nil, nil)
		assert.Equal(t, http.StatusNotFound, code, "other workspace: %v", out)
		// The workspace owner, a user, is not the declarer either.
		resp := f.tn.env.Post(t, "/api/v1/uploads/"+id+"/confirm", nil)
		raw := f.tn.env.ReadBody(t, resp)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "owner user: %s", raw)
		// An unknown id answers the same.
		code, _ = f.agentJSON(t, http.MethodPost, "/api/v1/uploads/00000000-0000-4000-8000-000000000002/confirm", f.agentKey, nil, nil)
		assert.Equal(t, http.StatusNotFound, code)
		// Positive control: the declarer reaches it (409, nothing PUT yet).
		code, out = f.confirm(t, id, nil)
		assert.Equal(t, http.StatusConflict, code, "declarer: %v", out)
	})

	t.Run("WrongTypeIs422MimeMismatch", func(t *testing.T) {
		notPNG := []byte("<html><script>alert(1)</script></html>")
		id, url := f.declare(t, docUploads, map[string]any{
			"name": "x.png", "mime_type": "image/png", "size_bytes": len(notPNG), "sha256": sha256Of(notPNG),
		})
		putBare(t, url, notPNG)
		code, out := f.confirm(t, id, nil)
		require.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "mime_mismatch", out["error_code"])
	})

	t.Run("ExpiredIs410", func(t *testing.T) {
		png := pngBytes(t, 64)
		id, url := f.declare(t, docUploads, map[string]any{
			"name": "e.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png),
		})
		putBare(t, url, png)
		_, err := f.tn.env.DB.ExecContext(context.Background(),
			"UPDATE upload_sessions SET expires_at = now() - interval '1 second' WHERE id = $1", id)
		require.NoError(t, err)
		code, out := f.confirm(t, id, nil)
		require.Equal(t, http.StatusGone, code, "%v", out)
		assert.Equal(t, "upload_expired", out["error_code"])
	})
}

// TestTwoStepUpload_SizeLimitIsTheSignedLength: the presigned PUT carries the
// declared length in its signature, so a body of another length is refused
// by the object store itself and never staged.
func TestTwoStepUpload_SizeLimitIsTheSignedLength(t *testing.T) {
	f := newUploadFixture(t, "upl-len")
	png := pngBytes(t, 1000)
	id, url := f.declare(t, "/api/v1/documents/"+f.docID+"/attachments/uploads", map[string]any{
		"name": "l.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png),
	})
	longer := append(append([]byte(nil), png...), 'x')
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(longer))
	require.NoError(t, err)
	req.ContentLength = int64(len(longer))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a PUT longer than declared must not be accepted")
	code, out := f.confirm(t, id, nil)
	assert.Equal(t, http.StatusConflict, code, "%v", out)
}

// dbTrigger installs a BEFORE trigger for the test and drops it on cleanup.
// body is plpgsql run per row; it must scope itself to the test's own file
// name, since the table is shared.
func (f *uploadFixture) dbTrigger(t *testing.T, name, table, event, body string) {
	t.Helper()
	db := f.tn.env.DB
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only DDL cannot bind identifiers; name, table, event and body are constants of the calling test, no external input
	drop := fmt.Sprintf(`DROP TRIGGER IF EXISTS %[1]s ON %[2]s; DROP FUNCTION IF EXISTS %[1]s()`, name, table)
	_, _ = db.Exec(drop)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only DDL cannot bind identifiers; name, table, event and body are constants of the calling test, no external input
	_, err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN %[2]s RETURN NEW; END $$;
		CREATE TRIGGER %[1]s BEFORE %[3]s ON %[4]s FOR EACH ROW EXECUTE FUNCTION %[1]s()`, name, body, event, table))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(drop) })
}

// TestTwoStepUpload_ConfirmChecksProjectAccessNow: removing the agent from
// the task's project refuses the confirm of an upload it declared while it
// was a member, and writes nothing; a confirmed upload's replay is refused
// the same way.
func TestTwoStepUpload_ConfirmChecksProjectAccessNow(t *testing.T) {
	f := newUploadFixture(t, "upl-revoke")
	png := pngBytes(t, 2000)
	path := "/api/v1/tasks/" + f.tn.taskID + "/artifacts/uploads"
	decl := func(name string) map[string]any {
		return map[string]any{"name": name, "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png)}
	}
	done, doneURL := f.declare(t, path, decl("before.png"))
	putBare(t, doneURL, png)
	code, out := f.confirm(t, done, nil)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	pending, pendingURL := f.declare(t, path, decl("revoked.png"))
	putBare(t, pendingURL, png)

	_, err := f.tn.env.DB.Exec(`DELETE FROM project_members WHERE project_id = $1 AND agent_id = $2`, f.tn.projectID, f.agentID)
	require.NoError(t, err)
	code, out = f.agentJSON(t, http.MethodPost, path, f.agentKey, decl("after.png"), nil)
	require.Equal(t, http.StatusForbidden, code, "positive control: declaring is refused after removal: %v", out)

	code, out = f.confirm(t, pending, nil)
	assert.Equal(t, http.StatusForbidden, code, "confirm after removal: %v", out)
	var n int
	require.NoError(t, f.tn.env.DB.Get(&n, `SELECT count(*) FROM artifacts WHERE task_id = $1 AND name = 'revoked.png'`, f.tn.taskID))
	assert.Equal(t, 0, n, "no artifact written after removal")
	code, out = f.confirm(t, done, nil)
	assert.Equal(t, http.StatusForbidden, code, "replay after removal: %v", out)
}

// TestTwoStepUpload_SlowWriteStillRecorded: a result write slower than the
// bookkeeping timeout is still recorded, so the replay answers with it.
func TestTwoStepUpload_SlowWriteStillRecorded(t *testing.T) {
	f := newUploadFixture(t, "upl-slow")
	png := pngBytes(t, 2000)
	id, url := f.declare(t, "/api/v1/documents/"+f.docID+"/attachments/uploads", map[string]any{
		"name": "upl-slow-write.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png),
	})
	putBare(t, url, png)
	f.dbTrigger(t, "upl_test_slow_attachment", "document_attachments", "INSERT",
		`IF NEW.name = 'upl-slow-write.png' THEN PERFORM pg_sleep(11); END IF;`)
	code, out := f.confirm(t, id, nil)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	first := out["attachment_id"]
	code, out = f.confirm(t, id, nil)
	require.Equal(t, http.StatusOK, code, "replay after a slow write: %v", out)
	assert.Equal(t, first, out["attachment_id"])
}

// TestTwoStepUpload_UnrecordedResultIsNotWrittenTwice: when recording the
// confirmed result fails after the row is written, the confirm still answers
// with the row, a replay answers with the same row, and a second PUT and
// confirm of the same upload does not write another.
func TestTwoStepUpload_UnrecordedResultIsNotWrittenTwice(t *testing.T) {
	f := newUploadFixture(t, "upl-receipt")
	png := pngBytes(t, 2000)
	id, url := f.declare(t, "/api/v1/documents/"+f.docID+"/attachments/uploads", map[string]any{
		"name": "upl-receipt.png", "mime_type": "image/png", "size_bytes": len(png), "sha256": sha256Of(png),
	})
	putBare(t, url, png)
	f.dbTrigger(t, "upl_test_receipt_failure", "upload_sessions", "UPDATE",
		`IF NEW.name = 'upl-receipt.png' AND NEW.confirmed_at IS NOT NULL THEN RAISE EXCEPTION 'injected receipt write failure'; END IF;`)
	code, out := f.confirm(t, id, nil)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	first := out["attachment_id"]
	code, out = f.confirm(t, id, nil)
	require.Equal(t, http.StatusOK, code, "replay while the record still fails: %v", out)
	assert.Equal(t, first, out["attachment_id"])

	_, err := f.tn.env.DB.Exec(`DROP TRIGGER upl_test_receipt_failure ON upload_sessions`)
	require.NoError(t, err)
	putBare(t, url, png)
	code, out = f.confirm(t, id, nil)
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, first, out["attachment_id"])
	var n int
	require.NoError(t, f.tn.env.DB.Get(&n, `SELECT count(*) FROM document_attachments WHERE document_id = $1`, f.docID))
	assert.Equal(t, 1, n, "one attachment for one upload")
	var confirmed bool
	require.NoError(t, f.tn.env.DB.Get(&confirmed, `SELECT confirmed_at IS NOT NULL FROM upload_sessions WHERE id = $1`, id))
	assert.True(t, confirmed, "the record lands once the database takes it")
}
