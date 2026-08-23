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

func TestParseMergeRequestURL(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		wantProject string
		wantIID     int
		wantOK      bool
	}{
		{"plain", "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/123", "entire-vc/evc-mesh", 123, true},
		{"subgroup", "https://git.entire.host/entire-vc/team/sub/-/merge_requests/7", "entire-vc/team/sub", 7, true},
		{"trailing slash", "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/123/", "entire-vc/evc-mesh", 123, true},
		{"diffs suffix", "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/123/diffs", "entire-vc/evc-mesh", 123, true},
		{"different self-hosted host", "https://gitlab.example.org/o/r/-/merge_requests/1", "o/r", 1, true},
		{"http scheme", "http://git.entire.host/o/r/-/merge_requests/1", "o/r", 1, true},
		{"not an MR url", "https://git.entire.host/entire-vc/evc-mesh/-/commit/abc123", "", 0, false},
		{"github pull url", "https://github.com/entire-vc/evc-mesh/pull/123", "", 0, false},
		{"garbage", "not a url at all", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, iid, ok := ParseMergeRequestURL(tc.url)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantProject, project)
				assert.Equal(t, tc.wantIID, iid)
			}
		})
	}
}

func TestClient_GetMergeRequestState_Merged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is the DECODED path — net/http unescapes %2F back to
		// '/' there. EscapedPath() is what actually went over the wire, and
		// that's what proves the project path was sent as one URL-encoded
		// segment rather than (wrongly) as two path segments.
		assert.Equal(t, "/api/v4/projects/entire-vc%2Fevc-mesh/merge_requests/123", r.URL.EscapedPath())
		assert.Equal(t, "test-token", r.Header.Get("PRIVATE-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "merged"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")

	state, err := c.GetMergeRequestState(context.Background(), "entire-vc/evc-mesh", 123)
	require.NoError(t, err)
	assert.True(t, state.Merged)
	assert.Equal(t, "merged", state.State)
}

func TestClient_GetMergeRequestState_StillOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "opened"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")

	state, err := c.GetMergeRequestState(context.Background(), "entire-vc/evc-mesh", 99)
	require.NoError(t, err)
	assert.False(t, state.Merged)
	assert.Equal(t, "opened", state.State)
}

func TestClient_GetMergeRequestState_NoAuthHeaderWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "opened"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.GetMergeRequestState(context.Background(), "o/r", 1)
	require.NoError(t, err)
}

func TestClient_GetMergeRequestState_PrivateProjectWithoutTokenIs404(t *testing.T) {
	// GitLab deliberately doesn't distinguish "doesn't exist" from "not for
	// you" on a private project — an unauthenticated (or wrongly-scoped)
	// request gets a plain 404, same as a genuinely missing project/MR.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.GetMergeRequestState(context.Background(), "o/r", 1)
	require.Error(t, err)
}

func TestClient_GetMergeRequestState_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.GetMergeRequestState(context.Background(), "o/r", 1)
	require.Error(t, err)
}

func TestNewClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "opened"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/", "")
	_, err := c.GetMergeRequestState(context.Background(), "o/r", 1)
	require.NoError(t, err)
	assert.Equal(t, "/api/v4/projects/o%2Fr/merge_requests/1", gotPath)
}
