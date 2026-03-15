package embedding

import "github.com/entire-vc/evc-mesh/internal/config"

// NewEmbedder constructs the appropriate Embedder based on cfg.Provider.
// Supported providers:
//   - "ollama"  — local Ollama server (nomic-embed-text by default)
//   - "openai"  — OpenAI embeddings API (text-embedding-3-small by default)
//   - "none" or anything else — NoopEmbedder (keyword-only recall, no HTTP calls)
func NewEmbedder(cfg config.EmbeddingConfig) Embedder {
	switch cfg.Provider {
	case "ollama":
		return NewOllamaEmbedder(cfg.Endpoint, cfg.Model, cfg.Dimensions)
	case "openai":
		return NewOpenAIEmbedder(cfg.Endpoint, cfg.APIKey, cfg.Model, cfg.Dimensions)
	default:
		return NewNoopEmbedder()
	}
}
