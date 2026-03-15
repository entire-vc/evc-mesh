package embedding

import "context"

// noopEmbedder is returned when no embedding provider is configured.
// All operations return nil/empty and no error, so callers always degrade
// gracefully to keyword-only search.
type noopEmbedder struct{}

// NewNoopEmbedder returns an Embedder that never produces vectors.
// Use IsNoop to detect whether vector search is available.
func NewNoopEmbedder() Embedder {
	return &noopEmbedder{}
}

// IsNoop reports whether e is a no-op embedder (i.e. vector search is disabled).
func IsNoop(e Embedder) bool {
	_, ok := e.(*noopEmbedder)
	return ok
}

func (n *noopEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func (n *noopEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return make([][]float32, len(texts)), nil
}

func (n *noopEmbedder) Model() string   { return "" }
func (n *noopEmbedder) Dimensions() int { return 0 }
