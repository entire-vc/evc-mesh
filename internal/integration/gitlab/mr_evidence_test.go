package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// GitLab's merged_at is the only source of the merge time for a
// GitLab MR: it is returned in UTC, and an absent, null or unparseable value
// stays nil rather than becoming a zero or current time.
func TestClient_GetMergeRequestEvidence_MergedAt(t *testing.T) {
	want := time.Date(2026, 11, 20, 15, 3, 4, 512000000, time.UTC)
	cases := []struct {
		name     string
		mergedAt any // omitted from the body when absentMarker
		want     *time.Time
	}{
		{"offset normalised to utc", "2026-11-20T18:03:04.512+03:00", &want},
		{"zulu", "2026-11-20T15:03:04.512Z", &want},
		{"null", nil, nil},
		{"absent", absentMarker{}, nil},
		{"garbage", "yesterday", nil},
		{"zero time", "0001-01-01T00:00:00Z", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"iid": 1753, "state": "merged", "sha": "abc"}
			if _, absent := tc.mergedAt.(absentMarker); !absent {
				body["merged_at"] = tc.mergedAt
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer srv.Close()
			got, err := NewClient(srv.URL, "tok").GetMergeRequestEvidence(context.Background(), "entire-vc/evc-mesh", 1753)
			require.NoError(t, err)
			if tc.want == nil {
				assert.Nil(t, got.MergedAt)
				return
			}
			require.NotNil(t, got.MergedAt)
			assert.True(t, got.MergedAt.Equal(*tc.want), "merged_at %v, want %v", got.MergedAt, tc.want)
			assert.Equal(t, time.UTC, got.MergedAt.Location())
		})
	}
}

type absentMarker struct{}
