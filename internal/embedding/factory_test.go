package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

func TestNewEmbedder_Providers(t *testing.T) {
	cases := []struct {
		provider string
		wantNoop bool
	}{
		{"ollama", false},
		{"openai", false},
		{"none", true},
		{"", true},
		{"unknown-provider", true},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			e := NewEmbedder(config.EmbeddingConfig{Provider: tc.provider, Dimensions: 3})
			if e == nil {
				t.Fatal("NewEmbedder returned nil")
			}
			if got := IsNoop(e); got != tc.wantNoop {
				t.Errorf("provider %q: IsNoop() = %v, want %v", tc.provider, got, tc.wantNoop)
			}
		})
	}
}

// TestNewEmbedder_OpenAI_WiresTruncationReporting proves NewEmbedder's "openai"
// branch actually wires WithMaxInputTokens(cfg.MaxInputTokens, reportTruncation) —
// not just that it returns *an* embedder, but that a truncated response reaches
// the real production reporter (the Prometheus counter this feature exists to
// populate), not a test double.
func TestNewEmbedder_OpenAI_WiresTruncationReporting(t *testing.T) {
	const window = 512
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
			"usage": map[string]any{"prompt_tokens": window, "total_tokens": window},
		})
	}))
	defer srv.Close()

	before := promtestutil.ToFloat64(metrics.MemoryEmbedTruncatedTotal.WithLabelValues("factory-test-model"))

	e := NewEmbedder(config.EmbeddingConfig{
		Provider:       "openai",
		Endpoint:       srv.URL,
		Model:          "factory-test-model",
		Dimensions:     3,
		MaxInputTokens: window,
	})

	if _, err := e.Embed(context.Background(), strings.Repeat("x ", 5000)); err != nil {
		t.Fatalf("Embed returned an error: %v", err)
	}

	after := promtestutil.ToFloat64(metrics.MemoryEmbedTruncatedTotal.WithLabelValues("factory-test-model"))
	if after != before+1 {
		t.Errorf("mesh_memory_embed_truncated_total did not increment via NewEmbedder's wiring: before=%v after=%v", before, after)
	}
}

// TestNewEmbedder_OpenAI_NoTruncationWhenWindowUnset: MaxInputTokens=0 (the
// config default) must disable the check entirely, not report every document.
func TestNewEmbedder_OpenAI_NoTruncationWhenWindowUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"embedding": []float32{0.1}}},
			"usage": map[string]any{"prompt_tokens": 99999, "total_tokens": 99999},
		})
	}))
	defer srv.Close()

	before := promtestutil.ToFloat64(metrics.MemoryEmbedTruncatedTotal.WithLabelValues("factory-test-model-unset"))

	e := NewEmbedder(config.EmbeddingConfig{
		Provider:   "openai",
		Endpoint:   srv.URL,
		Model:      "factory-test-model-unset",
		Dimensions: 1,
	})
	if _, err := e.Embed(context.Background(), "text"); err != nil {
		t.Fatalf("Embed returned an error: %v", err)
	}

	after := promtestutil.ToFloat64(metrics.MemoryEmbedTruncatedTotal.WithLabelValues("factory-test-model-unset"))
	if after != before {
		t.Errorf("reported truncation with MaxInputTokens=0 (disabled) — before=%v after=%v", before, after)
	}
}
