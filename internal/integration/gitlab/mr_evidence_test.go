package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_GetMergeRequestEvidence(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    map[string]any
		want    MergeRequestEvidence
		wantErr bool
	}{
		{name: "merged with head pipeline", status: 200, body: map[string]any{
			"iid": 42, "state": "merged", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"web_url":       "https://git.example.org/example/product/-/merge_requests/42",
			"head_pipeline": map[string]any{"id": 123, "status": "success", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		}, want: MergeRequestEvidence{State: "merged", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			WebURL:       "https://git.example.org/example/product/-/merge_requests/42",
			HeadPipeline: &HeadPipeline{ID: 123, Status: "success", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{name: "no pipeline", status: 200, body: map[string]any{"iid": 42, "state": "merged", "sha": "abc", "head_pipeline": nil},
			want: MergeRequestEvidence{State: "merged", SHA: "abc"}},
		{name: "iid mismatch", status: 200, body: map[string]any{"iid": 1, "state": "merged", "sha": "abc"}, wantErr: true},
		{name: "not found", status: 404, body: map[string]any{"message": "404 Not found"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v4/projects/example%2Fproduct/merge_requests/42", r.URL.EscapedPath())
				assert.Equal(t, "tok", r.Header.Get("PRIVATE-TOKEN"))
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			defer srv.Close()
			got, err := NewClient(srv.URL, "tok").GetMergeRequestEvidence(context.Background(), "example/product", 42)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
