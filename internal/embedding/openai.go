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

// openAIEmbedder calls the OpenAI (or compatible) embeddings API.
// API reference: POST /v1/embeddings  {"model": "...", "input": "..."}
type openAIEmbedder struct {
	endpoint   string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

// NewOpenAIEmbedder returns an Embedder backed by the OpenAI embeddings API.
// endpoint defaults to "https://api.openai.com" when empty.
// model defaults to "text-embedding-3-small" when empty.
func NewOpenAIEmbedder(endpoint, apiKey, model string, dimensions int) Embedder {
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &openAIEmbedder{
		endpoint:   strings.TrimRight(endpoint, "/"),
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *openAIEmbedder) Model() string   { return o.model }
func (o *openAIEmbedder) Dimensions() int { return o.dimensions }

type openAIEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type openAIEmbedData struct {
	Embedding []float32 `json:"embedding"`
}

type openAIEmbedResponse struct {
	Data []openAIEmbedData `json:"data"`
}

func (o *openAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Model: o.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: http: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai embed: unexpected status %d", resp.StatusCode)
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("openai embed: decode response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("openai embed: empty data in response")
	}
	return result.Data[0].Embedding, nil
}

func (o *openAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := o.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("openai embed batch[%d]: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}
