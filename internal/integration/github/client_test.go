package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePullRequestURL(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantOK    bool
	}{
		{"plain", "https://github.com/entire-vc/evc-mesh/pull/123", "entire-vc", "evc-mesh", 123, true},
		{"trailing slash", "https://github.com/entire-vc/evc-mesh/pull/123/", "entire-vc", "evc-mesh", 123, true},
		{"files suffix", "https://github.com/entire-vc/evc-mesh/pull/123/files", "entire-vc", "evc-mesh", 123, true},
		{"http scheme", "http://github.com/o/r/pull/1", "o", "r", 1, true},
		{"not a PR url", "https://github.com/entire-vc/evc-mesh/commit/abc123", "", "", 0, false},
		{"not github", "https://gitlab.com/o/r/pull/1", "", "", 0, false},
		{"garbage", "not a url at all", "", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, num, ok := ParsePullRequestURL(tc.url)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantOwner, owner)
				assert.Equal(t, tc.wantRepo, repo)
				assert.Equal(t, tc.wantNum, num)
			}
		})
	}
}

func TestClient_GetPullRequestState_Merged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/entire-vc/evc-mesh/pulls/123", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "closed", "merged": true})
	}))
	defer srv.Close()

	c := NewClient("test-token")
	c.baseURL = srv.URL

	state, err := c.GetPullRequestState(context.Background(), "entire-vc", "evc-mesh", 123)
	require.NoError(t, err)
	assert.True(t, state.Merged)
	assert.Equal(t, "closed", state.State)
}

func TestClient_GetPullRequestState_StillOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "open", "merged": false})
	}))
	defer srv.Close()

	c := NewClient("")
	c.baseURL = srv.URL

	state, err := c.GetPullRequestState(context.Background(), "entire-vc", "evc-mesh", 99)
	require.NoError(t, err)
	assert.False(t, state.Merged)
	assert.Equal(t, "open", state.State)
}

func TestClient_GetPullRequestState_NoAuthHeaderWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"state": "open", "merged": false})
	}))
	defer srv.Close()

	c := NewClient("")
	c.baseURL = srv.URL
	_, err := c.GetPullRequestState(context.Background(), "o", "r", 1)
	require.NoError(t, err)
}

func TestClient_GetPullRequestState_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient("")
	c.baseURL = srv.URL
	_, err := c.GetPullRequestState(context.Background(), "o", "r", 1)
	require.Error(t, err)
}
