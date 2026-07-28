package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestOpenAIEmbedder_EmbedBatch_SendsOneRequestPerBatch pins the fix for #67f4e0d9:
// EmbedBatch must issue ONE HTTP request per maxClientBatchSize texts, not one per text.
// The pre-fix implementation looped over Embed, which is what multiplied load on the
// shared TEI instance by the chunk count and made long documents fail under concurrency.
func TestOpenAIEmbedder_EmbedBatch_SendsOneRequestPerBatch(t *testing.T) {
	var requests int
	var lastInputs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		lastInputs = req.Input

		// Respond out of order on purpose: the API does not promise request order, and
		// placing vectors by position rather than by index would silently mis-assign
		// every chunk's vector to the wrong chunk.
		data := make([]map[string]any, 0, len(req.Input))
		for i := len(req.Input) - 1; i >= 0; i-- {
			data = append(data, map[string]any{
				"index":     i,
				"embedding": []float32{float32(i), 0, 0, 0},
			})
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder(srv.URL, "", "test-model", 4, 5)

	texts := make([]string, 5)
	for i := range texts {
		texts[i] = fmt.Sprintf("chunk %d", i)
	}
	vecs, err := emb.EmbedBatch(context.Background(), texts)
	require.NoError(t, err)

	assert.Equal(t, 1, requests, "5 texts must cost ONE request, not one per text")
	assert.Len(t, lastInputs, 5, "all texts must travel in a single input array")
	require.Len(t, vecs, 5)
	for i := range vecs {
		require.Len(t, vecs[i], 4)
		assert.Equal(t, float32(i), vecs[i][0], "vectors must be ordered by the response index field, not by arrival order")
	}
}

// TestOpenAIEmbedder_EmbedBatch_SplitsOversizedBatches ensures a chunk set larger than the
// server's max_client_batch_size is split rather than rejected wholesale.
func TestOpenAIEmbedder_EmbedBatch_SplitsOversizedBatches(t *testing.T) {
	var requests int
	var sizes []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		sizes = append(sizes, len(req.Input))
		data := make([]map[string]any, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0, 0, 0}})
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder(srv.URL, "", "test-model", 4, 5)

	texts := make([]string, maxClientBatchSize+3)
	for i := range texts {
		texts[i] = "t"
	}
	vecs, err := emb.EmbedBatch(context.Background(), texts)
	require.NoError(t, err)

	assert.Equal(t, 2, requests)
	assert.Equal(t, []int{maxClientBatchSize, 3}, sizes, "no request may exceed the server's client batch limit")
	assert.Len(t, vecs, maxClientBatchSize+3)
}

// newJSONServer returns a test server replying with the given status and raw body.
func newJSONServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestOpenAIEmbedder_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("non-200 status", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusTooManyRequests, `{}`)
		defer srv.Close()
		_, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 5).Embed(ctx, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 429")
	})

	t.Run("undecodable body", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusOK, `not json`)
		defer srv.Close()
		_, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 5).Embed(ctx, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode response")
	})

	t.Run("empty data", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusOK, `{"data":[]}`)
		defer srv.Close()
		_, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 5).Embed(ctx, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty data")
	})

	// An out-of-range index must be an error, never a silent drop: writing it would
	// panic, and ignoring it would leave a nil vector for a chunk that reported success.
	t.Run("index out of range", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusOK, `{"data":[{"index":7,"embedding":[1,0,0,0]}]}`)
		defer srv.Close()
		_, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 5).Embed(ctx, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	// A response that omits an input's vector must fail loudly. Before this check it
	// left a nil entry, which embedChunked treats as "empty vector, skip this chunk" —
	// storing the memory with a silently incomplete chunk set while reporting success.
	t.Run("missing vector for an input", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusOK, `{"data":[{"index":0,"embedding":[1,0,0,0]}]}`)
		defer srv.Close()
		_, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 5).EmbedBatch(ctx, []string{"a", "b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no vector returned for input 1")
	})

	t.Run("transport error", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusOK, `{}`)
		srv.Close() // closed on purpose — connection refused
		_, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 1).Embed(ctx, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http")
	})

	t.Run("empty input is a no-op", func(t *testing.T) {
		srv := newJSONServer(t, http.StatusOK, `{"data":[]}`)
		defer srv.Close()
		vecs, err := NewOpenAIEmbedder(srv.URL, "", "m", 4, 5).EmbedBatch(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, vecs)
	})

	t.Run("api key is sent when configured", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0,0,0]}]}`))
		}))
		defer srv.Close()
		_, err := NewOpenAIEmbedder(srv.URL, "secret", "m", 4, 5).Embed(ctx, "x")
		require.NoError(t, err)
		assert.Equal(t, "Bearer secret", gotAuth)
	})
}
