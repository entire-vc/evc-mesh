package teamrelay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransport_SyncUploadEndpointAndResponse(t *testing.T) {
	var capturedPath string
	var capturedMethod string
	var capturedKey string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path + "?" + r.URL.RawQuery
		capturedKey = r.Header.Get("X-Agent-Key")
		capturedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(syncUploadResponse{
			SyncURL: "wss://relay.example.com/share-id",
			WebURL:  "https://web.example.com/share/path/artifact.md",
			Path:    "path/artifact.md",
		})
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	content := []byte("# hello world")
	publicURL, err := transport(context.Background(), "share-id", "path/artifact.md", content, "text/markdown", "tr_agent_test")

	require.NoError(t, err)
	assert.Equal(t, "https://web.example.com/share/path/artifact.md", publicURL)
	assert.Equal(t, http.MethodPost, capturedMethod)
	assert.True(t, strings.HasPrefix(capturedPath, "/v1/web/shares/share-id/sync-upload?"), "endpoint must be /sync-upload, got %s", capturedPath)
	assert.Contains(t, capturedPath, "path=path%2Fartifact.md")
	assert.NotContains(t, capturedPath, "source=mesh-artifact", "old source param must not be present")
	assert.Equal(t, "tr_agent_test", capturedKey)
	assert.Equal(t, content, capturedBody)
}

func TestTransport_MissingRelayURL(t *testing.T) {
	t.Setenv("MESH_TEAMRELAY_RELAY_URL", "")
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	url, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/markdown", "key")
	assert.NoError(t, err)
	assert.Empty(t, url)
}

func TestTransport_EmptyPathResponse_ReturnsError(t *testing.T) {
	// Simulates TR returning {ok: false} or any response that doesn't populate the
	// syncUploadResponse fields — path will be "" which signals upload failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": false, "error": "share not found"}`))
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	publicURL, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/plain", "tr_agent_test")
	assert.Error(t, err, "expected error when relay returns ok=false body at HTTP 200")
	assert.Empty(t, publicURL)
	assert.Contains(t, err.Error(), "empty path")
}

func TestTransport_UnparsableResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json {{{`))
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	publicURL, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/plain", "tr_agent_test")
	assert.Error(t, err)
	assert.Empty(t, publicURL)
}

func TestTransport_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	publicURL, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/plain", "bad-key")
	assert.NoError(t, err)
	assert.Empty(t, publicURL)
}
