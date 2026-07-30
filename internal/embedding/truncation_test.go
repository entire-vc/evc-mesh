package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An embedding server that truncates is the normal case, not an edge case: TEI
// ships auto_truncate=true. It answers 200 and returns a well-formed vector for
// the first N tokens of a document of any size, and says so nowhere a caller can
// see — the only trace is prompt_tokens landing exactly on the window.
//
// That silence is why a memory can be missing from semantic recall for weeks
// while the row looks perfectly embedded: measured on the bench corpus
// (#e8063a65), 96% of documents exceeded a 512-token window, and one gold
// session carried its answer at 75% of the document, outside the window, giving
// it dense rank 35/45 instead of 1/45.
//
// mock: external boundary — the embedding server is a true external dependency,
// and the behaviour under test is how we read ITS response.
func embedServer(t *testing.T, promptTokens int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
			"usage": map[string]any{"prompt_tokens": promptTokens, "total_tokens": promptTokens},
		})
	}))
}

type report struct {
	model                   string
	promptTokens, maxTokens int
	calls                   int
}

func TestOpenAIEmbedder_ReportsTruncation(t *testing.T) {
	const window = 512

	cases := []struct {
		name         string
		promptTokens int
		wantReport   bool
	}{
		{"exactly at the window — the server stopped reading", window, true},
		{"over the window", window + 1, true},
		{"one token short — a genuinely complete document", window - 1, false},
		{"short document", 12, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := embedServer(t, tc.promptTokens)
			defer srv.Close()

			var got report
			e := NewOpenAIEmbedder(srv.URL, "", "multilingual-e5-small", 384, 5,
				WithMaxInputTokens(window, func(model string, pt, mt int) {
					got = report{model: model, promptTokens: pt, maxTokens: mt, calls: got.calls + 1}
				}))

			vec, err := e.Embed(context.Background(), strings.Repeat("x ", 5000))
			if err != nil {
				t.Fatalf("Embed returned an error: %v", err)
			}
			if len(vec) == 0 {
				t.Fatal("Embed returned no vector — a truncated embedding is still usable and must be returned")
			}

			if tc.wantReport && got.calls != 1 {
				t.Errorf("truncation went unreported: prompt_tokens=%d, window=%d", tc.promptTokens, window)
			}
			if !tc.wantReport && got.calls != 0 {
				t.Errorf("reported truncation for a complete document (prompt_tokens=%d, window=%d)", tc.promptTokens, window)
			}
			if tc.wantReport && got.calls == 1 {
				if got.model != "multilingual-e5-small" || got.maxTokens != window || got.promptTokens != tc.promptTokens {
					t.Errorf("report carried %+v, want model/window/tokens to identify the truncation", got)
				}
			}
		})
	}
}

// TestOpenAIEmbedder_TruncationCheckDisabled: an unknown window cannot be
// compared against. Guessing one would report every short document as truncated.
func TestOpenAIEmbedder_TruncationCheckDisabled(t *testing.T) {
	for _, window := range []int{0, -1} {
		t.Run(fmt.Sprintf("window=%d", window), func(t *testing.T) {
			srv := embedServer(t, 4096)
			defer srv.Close()

			calls := 0
			e := NewOpenAIEmbedder(srv.URL, "", "m", 3, 5,
				WithMaxInputTokens(window, func(string, int, int) { calls++ }))

			if _, err := e.Embed(context.Background(), "anything"); err != nil {
				t.Fatalf("Embed returned an error: %v", err)
			}
			if calls != 0 {
				t.Errorf("reported truncation with no configured window — it cannot be known")
			}
		})
	}
}

// TestOpenAIEmbedder_NoReporterIsSafe: the check must never panic when wired
// without a reporter, since that is the default construction.
func TestOpenAIEmbedder_NoReporterIsSafe(t *testing.T) {
	srv := embedServer(t, 512)
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "m", 3, 5, WithMaxInputTokens(512, nil))
	if _, err := e.Embed(context.Background(), "text"); err != nil {
		t.Fatalf("Embed returned an error: %v", err)
	}
}

// TestOpenAIEmbedder_DefaultConstructionUnchanged: the option is additive — an
// embedder built the old way behaves exactly as before.
func TestOpenAIEmbedder_DefaultConstructionUnchanged(t *testing.T) {
	srv := embedServer(t, 99999)
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "m", 3, 5)
	vec, err := e.Embed(context.Background(), "text")
	if err != nil {
		t.Fatalf("Embed returned an error: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected the vector through unchanged, got %d dims", len(vec))
	}
}
