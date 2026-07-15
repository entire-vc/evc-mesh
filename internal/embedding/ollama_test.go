package embedding

import (
	"testing"
	"time"
)

// TestOllamaEmbedHTTPTimeoutConfigurable verifies that NewOllamaEmbedder threads a custom
// httpTimeoutSecs through to the embedder's http.Client, mirroring the OpenAI embedder's
// configurable-timeout fix.
func TestOllamaEmbedHTTPTimeoutConfigurable(t *testing.T) {
	e := NewOllamaEmbedder("", "", 0, 90)
	oe, ok := e.(*ollamaEmbedder)
	if !ok {
		t.Fatalf("expected *ollamaEmbedder, got %T", e)
	}
	if oe.client.Timeout != 90*time.Second {
		t.Fatalf("expected http.Client timeout 90s, got %s", oe.client.Timeout)
	}
}

// TestOllamaEmbedHTTPTimeoutDefaultsTo30s verifies that a zero or negative httpTimeoutSecs
// preserves today's hardcoded 30s behavior exactly.
func TestOllamaEmbedHTTPTimeoutDefaultsTo30s(t *testing.T) {
	for _, secs := range []int{0, -1} {
		e := NewOllamaEmbedder("", "", 0, secs)
		oe, ok := e.(*ollamaEmbedder)
		if !ok {
			t.Fatalf("expected *ollamaEmbedder, got %T", e)
		}
		if oe.client.Timeout != 30*time.Second {
			t.Fatalf("httpTimeoutSecs=%d: expected default http.Client timeout 30s, got %s", secs, oe.client.Timeout)
		}
	}
}
