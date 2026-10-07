package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPipelineStatusChecksResponseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		good       bool
	}{
		{"terminal", `{"id":93,"status":"success"}`, 200, true},
		{"running", `{"id":93,"status":"running"}`, 200, true},
		{"wrong-pipeline", `{"id":934,"status":"success"}`, 200, false},
		{"missing-status", `{"id":93}`, 200, false},
		{"provider-error", `{"id":93,"status":"success"}`, 500, false},
		{"invalid-json", `oops`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/v4/projects/entire-vc%2Fevc-mesh/pipelines/93", r.URL.EscapedPath())
				require.Equal(t, "fixture-token", r.Header.Get("PRIVATE-TOKEN"))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			status, err := NewClient(srv.URL, "fixture-token").GetPipelineStatus(context.Background(), "entire-vc/evc-mesh", 93)
			if tc.good {
				require.NoError(t, err)
				require.NotEmpty(t, status)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestPipelineStatusReturnsRequestAndTransportErrors(t *testing.T) {
	t.Run("rejects malformed configured URL", func(t *testing.T) {
		_, err := NewClient("\n", "").GetPipelineStatus(context.Background(), "group/project", 93)
		require.Error(t, err)
	})
	t.Run("returns transport failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()
		_, err := NewClient(url, "fixture-token").GetPipelineStatus(context.Background(), "group/project", 93)
		require.Error(t, err)
	})
}
