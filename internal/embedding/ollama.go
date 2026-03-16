package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ollamaEmbedder calls the Ollama local embedding API.
// API reference: POST /api/embeddings  {"model": "...", "prompt": "..."}
type ollamaEmbedder struct {
	endpoint   string
	model      string
	dimensions int
	client     *http.Client
}

// NewOllamaEmbedder returns an Embedder backed by a local Ollama instance.
// endpoint defaults to "http://localhost:11434" when empty.
// model defaults to "nomic-embed-text" when empty.
func NewOllamaEmbedder(endpoint, model string, dimensions int) Embedder {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	return &ollamaEmbedder{
		endpoint:   strings.TrimRight(endpoint, "/"),
		model:      model,
		dimensions: dimensions,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *ollamaEmbedder) Model() string   { return o.model }
func (o *ollamaEmbedder) Dimensions() int { return o.dimensions }

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (o *ollamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaEmbedRequest{Model: o.model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("ollama embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: http: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed: unexpected status %d", resp.StatusCode)
	}

	var result ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama embed: decode response: %w", err)
	}
	return result.Embedding, nil
}

func (o *ollamaEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := o.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("ollama embed batch[%d]: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}
