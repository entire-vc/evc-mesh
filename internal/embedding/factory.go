package embedding

import (
	"log"
	"sync"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

var truncationLogOnce sync.Once

// reportTruncation records a truncated embedding on the counter, and says it out
// loud once per process. The counter is the alertable signal; the single log line
// is there because the first person to hit this will be reading logs, not Grafana,
// and "why is semantic recall missing this document" is otherwise unanswerable.
func reportTruncation(model string, promptTokens, maxTokens int) {
	metrics.MemoryEmbedTruncatedTotal.WithLabelValues(model).Inc()
	truncationLogOnce.Do(func() {
		log.Printf("[embed] TRUNCATED: %s embedded only the first %d tokens (server window %d). "+
			"Anything past the cut is invisible to semantic recall — the vector is real but partial. "+
			"Track mesh_memory_embed_truncated_total; this line is logged once per process.",
			model, promptTokens, maxTokens)
	})
}

// NewEmbedder constructs the appropriate Embedder based on cfg.Provider.
// Supported providers:
//   - "ollama"  — local Ollama server (nomic-embed-text by default)
//   - "openai"  — OpenAI embeddings API (text-embedding-3-small by default)
//   - "none" or anything else — NoopEmbedder (keyword-only recall, no HTTP calls)
func NewEmbedder(cfg config.EmbeddingConfig) Embedder {
	switch cfg.Provider {
	case "ollama":
		return NewOllamaEmbedder(cfg.Endpoint, cfg.Model, cfg.Dimensions, cfg.HTTPTimeoutSecs)
	case "openai":
		return NewOpenAIEmbedder(cfg.Endpoint, cfg.APIKey, cfg.Model, cfg.Dimensions, cfg.HTTPTimeoutSecs,
			WithMaxInputTokens(cfg.MaxInputTokens, reportTruncation))
	default:
		return NewNoopEmbedder()
	}
}
