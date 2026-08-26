//go:build live

package teamrelay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// rawSyncWrite sends a sync-write with an arbitrary (or absent) If-Match,
// bypassing SyncWrite's own guard. Only the live test uses it, and only to
// establish that the SERVER refuses unconditional writes — a property we must
// not infer from our client refusing to send them.
func rawSyncWrite(t *testing.T, relayURL, shareID, path, agentKey, ifMatch string, body []byte) (int, string) {
	t.Helper()
	endpoint := fmt.Sprintf("%s/v1/shares/%s/sync-write?path=%s",
		strings.TrimRight(relayURL, "/"), url.PathEscape(shareID), url.QueryEscape(path))
	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)
	req.Header.Set("Content-Type", "text/markdown")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("raw sync-write: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(b)
}
