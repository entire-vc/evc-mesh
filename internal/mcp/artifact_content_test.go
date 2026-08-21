package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// onePixelPNG is a real, complete 1x1 PNG. Using genuine bytes rather than a
// hand-written header matters: the point of these tests is that what lands in
// storage is a file that actually opens.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0A, 'I', 'D', 'A', 'T',
	0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00,
	0x00, 0x00, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
}

// newArtifactTestTask spins up a server and returns it with a task to upload to.
func newArtifactTestTask(t *testing.T) (*Server, *mockAPIState, context.Context, string) {
	t.Helper()
	srv, state := newTestServer()
	ctx := context.Background()

	createResult, _ := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": state.fx.projectID.String(),
		"title":      "Artifact content task",
	}))
	var task map[string]any
	require.NoError(t, json.Unmarshal([]byte(mcpsdk.GetTextFromContent(createResult.Content[0])), &task))
	taskID, _ := task["id"].(string)
	require.NotEmpty(t, taskID)

	return srv, state, ctx, taskID
}

// TestUploadArtifact_Base64_StoresDecodedBytes is AC1: a base64 PNG must land as
// a valid PNG.
//
// The assertion is deliberately on the bytes the server received, not on
// result.IsError. Before the fix this call also returned success — the response
// was indistinguishable from a correct upload, which is the entire reason the
// defect survived in production.
func TestUploadArtifact_Base64_StoresDecodedBytes(t *testing.T) {
	srv, state, ctx, taskID := newArtifactTestTask(t)

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "screenshot.png",
		"content":       base64.StdEncoding.EncodeToString(onePixelPNG),
		"encoding":      "base64",
		"artifact_type": "image",
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, "upload must succeed: %s", mcpsdk.GetTextFromContent(result.Content[0]))

	require.Len(t, state.uploads, 1)
	got := state.uploads[0]

	assert.Equal(t, onePixelPNG, got.fileBytes,
		"stored bytes must be the DECODED png, not the base64 string")
	assert.True(t, len(got.fileBytes) > 0 && got.fileBytes[0] == 0x89,
		"stored file must start with the PNG signature")

	// The regression in one line: the old code stored the base64 text itself.
	assert.NotEqual(t, base64.StdEncoding.EncodeToString(onePixelPNG), string(got.fileBytes),
		"stored bytes must not be the literal base64 string")

	// mime_type used to be accepted and dropped, leaving the server to guess
	// from the filename.
	assert.Equal(t, "image/png", got.contentType,
		"declared mime type must reach the server on the file part")
}

// TestUploadArtifact_TruncatedBase64_Rejected is AC2, the load-bearing negative
// control: the same call with a truncated payload must be REFUSED and must not
// create an artifact. Before the fix it was accepted and answered success.
func TestUploadArtifact_TruncatedBase64_Rejected(t *testing.T) {
	full := base64.StdEncoding.EncodeToString(onePixelPNG)

	// Chop a character off the end — the canonical shape of a payload cut short
	// in transit.
	truncated := full[:len(full)-1]

	srv, state, ctx, taskID := newArtifactTestTask(t)

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "screenshot.png",
		"content":       truncated,
		"encoding":      "base64",
		"artifact_type": "image",
	}))
	require.NoError(t, err)

	assert.True(t, result.IsError, "truncated base64 must be refused, not stored")
	assert.Contains(t, mcpsdk.GetTextFromContent(result.Content[0]), "base64")
	assert.Empty(t, state.uploads, "no artifact may be created when the payload is refused")
}

// TestUploadArtifact_TruncatedButAlignedBase64_CaughtByChecksum covers the gap
// the length check alone leaves. A payload cut at a 4-character boundary decodes
// cleanly and still starts with a valid PNG header, so neither the base64 check
// nor the magic-byte check can see it. Only the checksum can.
func TestUploadArtifact_TruncatedButAlignedBase64_CaughtByChecksum(t *testing.T) {
	full := base64.StdEncoding.EncodeToString(onePixelPNG)
	aligned := full[:(len(full)/4)*4-4] // still a multiple of 4, but short

	sum := sha256.Sum256(onePixelPNG)

	srv, state, ctx, taskID := newArtifactTestTask(t)

	// Positive control: this payload IS accepted without a checksum — proving
	// the checksum is what rejects it below, not some other guard.
	okResult, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "aligned.png",
		"content":       aligned,
		"encoding":      "base64",
		"artifact_type": "image",
	}))
	require.NoError(t, err)
	require.False(t, okResult.IsError,
		"premise: an aligned truncation slips past the base64 and magic checks")

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "aligned.png",
		"content":       aligned,
		"encoding":      "base64",
		"artifact_type": "image",
		"sha256":        hex.EncodeToString(sum[:]),
	}))
	require.NoError(t, err)

	assert.True(t, result.IsError, "sha256 mismatch must fail the upload")
	assert.Contains(t, mcpsdk.GetTextFromContent(result.Content[0]), "sha256 mismatch")
	assert.Len(t, state.uploads, 1, "only the control upload may have been stored")
}

// TestUploadArtifact_MimeTypeContradictsContent_Rejected is AC3.
func TestUploadArtifact_MimeTypeContradictsContent_Rejected(t *testing.T) {
	srv, state, ctx, taskID := newArtifactTestTask(t)

	// Exactly the production failure: a base64 string sent as text, named .png.
	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "board.png",
		"content":       base64.StdEncoding.EncodeToString(onePixelPNG),
		"artifact_type": "image",
		// no encoding → treated as text, which is what agents were doing
	}))
	require.NoError(t, err)

	assert.True(t, result.IsError, "declared image/png with non-PNG bytes must be refused")
	msg := mcpsdk.GetTextFromContent(result.Content[0])
	assert.Contains(t, msg, "does not match declared mime_type")
	assert.Contains(t, msg, "base64", "the error must tell the agent how to fix it")
	assert.Empty(t, state.uploads)
}

// TestUploadArtifact_TextArtifact_Unchanged is AC4, the regression guard: the
// path everyone already uses must behave exactly as before.
func TestUploadArtifact_TextArtifact_Unchanged(t *testing.T) {
	srv, state, ctx, taskID := newArtifactTestTask(t)

	const body = "# Report\n\nAll good.\n"

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "report.md",
		"content":       body,
		"artifact_type": "report",
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, mcpsdk.GetTextFromContent(result.Content[0]))

	require.Len(t, state.uploads, 1)
	assert.Equal(t, body, string(state.uploads[0].fileBytes), "text content must be stored verbatim")
	assert.Equal(t, "text/markdown", state.uploads[0].contentType)
}

// TestUploadArtifact_TextThatLooksLikeBase64_StoredAsText is why encoding is an
// explicit parameter rather than something guessed. "deadbeef" is valid base64;
// a heuristic would silently mangle a legitimate text file.
func TestUploadArtifact_TextThatLooksLikeBase64_StoredAsText(t *testing.T) {
	srv, state, ctx, taskID := newArtifactTestTask(t)

	const body = "deadbeef"

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "notes.txt",
		"content":       body,
		"artifact_type": "file",
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)

	require.Len(t, state.uploads, 1)
	assert.Equal(t, body, string(state.uploads[0].fileBytes))
}

// TestUploadArtifact_MetadataForwarded closes the third silent drop in this
// handler: metadata was a declared parameter that was never read.
func TestUploadArtifact_MetadataForwarded(t *testing.T) {
	srv, state, ctx, taskID := newArtifactTestTask(t)

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID,
		"name":          "out.json",
		"content":       `{"ok":true}`,
		"artifact_type": "data",
		"metadata":      map[string]any{"source": "unit-test"},
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)

	require.Len(t, state.uploads, 1)
	assert.JSONEq(t, `{"source":"unit-test"}`, state.uploads[0].metadata)
}

func TestUploadArtifact_InvalidEncoding_Rejected(t *testing.T) {
	srv, state, ctx, taskID := newArtifactTestTask(t)

	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":  taskID,
		"name":     "x.txt",
		"content":  "hello",
		"encoding": "hex",
	}))
	require.NoError(t, err)

	assert.True(t, result.IsError)
	assert.Contains(t, mcpsdk.GetTextFromContent(result.Content[0]), "invalid encoding")
	assert.Empty(t, state.uploads)
}

// --- unit-level tests for the helpers ---

func TestDecodeArtifactContent(t *testing.T) {
	png64 := base64.StdEncoding.EncodeToString(onePixelPNG)

	tests := []struct {
		name     string
		content  string
		encoding string
		want     []byte
		wantErr  string
	}{
		{name: "default is text", content: "hello", encoding: "", want: []byte("hello")},
		{name: "explicit text", content: "hello", encoding: "text", want: []byte("hello")},
		{name: "base64 decodes", content: png64, encoding: "base64", want: onePixelPNG},
		{name: "base64 case-insensitive", content: png64, encoding: "BASE64", want: onePixelPNG},
		{name: "whitespace tolerated", content: png64[:8] + "\n" + png64[8:], encoding: "base64", want: onePixelPNG},
		{name: "truncated rejected", content: png64[:len(png64)-1], encoding: "base64", wantErr: "not a multiple of 4"},
		{name: "garbage rejected", content: "not!valid!base64", encoding: "base64", wantErr: "not valid base64"},
		{name: "unknown encoding", content: "x", encoding: "hex", wantErr: "invalid encoding"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeArtifactContent(tt.content, tt.encoding)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateArtifactMagic(t *testing.T) {
	tests := []struct {
		name    string
		mime    string
		data    []byte
		wantErr bool
	}{
		{name: "png ok", mime: "image/png", data: onePixelPNG},
		{name: "png with params ok", mime: "image/png; charset=binary", data: onePixelPNG},
		{name: "png against text fails", mime: "image/png", data: []byte("iVBORw0KGgoAAAA"), wantErr: true},
		{name: "jpeg ok", mime: "image/jpeg", data: []byte{0xFF, 0xD8, 0xFF, 0xE0}},
		{name: "jpeg against png fails", mime: "image/jpeg", data: onePixelPNG, wantErr: true},
		{name: "pdf ok", mime: "application/pdf", data: []byte("%PDF-1.7\n")},
		{name: "zip ok", mime: "application/zip", data: []byte{'P', 'K', 0x03, 0x04}},
		{name: "gif ok", mime: "image/gif", data: []byte("GIF89a...")},
		// Unknown types are not policed — the check only refuses what it can prove wrong.
		{name: "unknown mime passes", mime: "text/markdown", data: []byte("# hi")},
		{name: "empty content passes", mime: "image/png", data: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArtifactMagic(tt.mime, tt.data)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "does not match declared mime_type")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestVerifyArtifactChecksum(t *testing.T) {
	data := []byte("payload")
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	assert.NoError(t, verifyArtifactChecksum("", data), "empty checksum is a no-op")
	assert.NoError(t, verifyArtifactChecksum(hexSum, data))
	assert.NoError(t, verifyArtifactChecksum("  "+hexSum+"  ", data), "whitespace tolerated")

	err := verifyArtifactChecksum(hexSum, []byte("payloa"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256 mismatch")
}
