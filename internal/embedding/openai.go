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
// httpTimeoutSecs defaults to 30 when zero or negative (today's hardcoded behavior).
func NewOpenAIEmbedder(endpoint, apiKey, model string, dimensions, httpTimeoutSecs int) Embedder {
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if httpTimeoutSecs <= 0 {
		httpTimeoutSecs = 30
	}
	return &openAIEmbedder{
		endpoint:   strings.TrimRight(endpoint, "/"),
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		client:     &http.Client{Timeout: time.Duration(httpTimeoutSecs) * time.Second},
	}
}

func (o *openAIEmbedder) Model() string   { return o.model }
func (o *openAIEmbedder) Dimensions() int { return o.dimensions }

// maxClientBatchSize bounds how many texts go into one /v1/embeddings request.
// Matches the prod TEI server's max_client_batch_size (32); a larger array is
// rejected outright, so batches are split rather than sent whole.
const maxClientBatchSize = 32

// Input is []string, not string: the API accepts both, and the array form is what
// makes EmbedBatch a single round trip. Chunked memories embed 2-24 pieces each, and
// sending those one at a time is what exhausted the request budget mid-document under
// concurrent load (#67f4e0d9).
type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// Index is required, not decorative: the API does not guarantee data[] arrives in
// request order, so vectors are placed by index rather than by position.
type openAIEmbedData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openAIEmbedResponse struct {
	Data []openAIEmbedData `json:"data"`
}

func (o *openAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.embedOnce(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("openai embed: empty data in response")
	}
	return vecs[0], nil
}

// EmbedBatch embeds every text, using ONE HTTP request per maxClientBatchSize texts
// rather than one per text. For a chunked memory this collapses up to 24 sequential
// round trips into a single call — which is both the latency fix and a ~20x reduction
// in load on the shared embedder instance (#67f4e0d9).
func (o *openAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxClientBatchSize {
		end := start + maxClientBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := o.embedOnce(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("openai embed batch[%d:%d]: %w", start, end, err)
		}
		if len(vecs) != end-start {
			return nil, fmt.Errorf("openai embed batch[%d:%d]: got %d vectors for %d inputs", start, end, len(vecs), end-start)
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// embedOnce performs a single /v1/embeddings call for the given inputs and returns the
// vectors in INPUT order (reordering by the response's index field).
func (o *openAIEmbedder) embedOnce(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Model: o.model, Input: inputs})
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
	defer func() { _ = resp.Body.Close() }()

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

	out := make([][]float32, len(inputs))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(inputs) {
			return nil, fmt.Errorf("openai embed: response index %d out of range for %d inputs", d.Index, len(inputs))
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
