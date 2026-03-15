// Package embedding provides pluggable text embedding implementations for semantic memory search.
// All implementations satisfy the Embedder interface. When no embedding provider is configured,
// NewNoopEmbedder() is returned and all vector operations are skipped — keyword search continues
// to work normally (graceful degradation).
package embedding

import "context"

// Embedder generates dense vector representations of text for semantic similarity search.
// Implementations must be safe for concurrent use.
type Embedder interface {
	// Embed returns a vector representation of the given text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch returns vector representations for a slice of texts.
	// Implementations may call the underlying API in parallel or in a single batched request.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Model returns the model identifier string (e.g. "nomic-embed-text", "text-embedding-3-small").
	Model() string

	// Dimensions returns the expected embedding vector length (e.g. 768, 1536).
	// Returns 0 for NoopEmbedder.
	Dimensions() int
}
