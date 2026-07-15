package embedding

import (
	"testing"
	"time"
)

// TestEmbedHTTPTimeoutConfigurable verifies that NewOpenAIEmbedder threads a custom
// httpTimeoutSecs through to the embedder's http.Client, so EMBEDDING_HTTP_TIMEOUT_SECS
// can raise the timeout for slow, CPU-bound embedding backends (e.g. a self-hosted TEI
// server) instead of tripping the previously-hardcoded 30s deadline.
func TestEmbedHTTPTimeoutConfigurable(t *testing.T) {
	e := NewOpenAIEmbedder("", "", "", 0, 90)
	oe, ok := e.(*openAIEmbedder)
	if !ok {
		t.Fatalf("expected *openAIEmbedder, got %T", e)
	}
	if oe.client.Timeout != 90*time.Second {
		t.Fatalf("expected http.Client timeout 90s, got %s", oe.client.Timeout)
	}
}

// TestEmbedHTTPTimeoutDefaultsTo30s verifies that a zero or negative httpTimeoutSecs
// preserves today's hardcoded 30s behavior exactly.
func TestEmbedHTTPTimeoutDefaultsTo30s(t *testing.T) {
	for _, secs := range []int{0, -1} {
		e := NewOpenAIEmbedder("", "", "", 0, secs)
		oe, ok := e.(*openAIEmbedder)
		if !ok {
			t.Fatalf("expected *openAIEmbedder, got %T", e)
		}
		if oe.client.Timeout != 30*time.Second {
			t.Fatalf("httpTimeoutSecs=%d: expected default http.Client timeout 30s, got %s", secs, oe.client.Timeout)
		}
	}
}
