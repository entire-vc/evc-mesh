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

func TestGetCheckRunsSummary_Folds(t *testing.T) {
	body := `{"total_count":3,"check_runs":[{"id":4,"status":"completed","conclusion":"success"},{"id":9,"status":"completed","conclusion":"skipped"},{"id":7,"status":"completed","conclusion":"failure"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/commits/abc/check-runs", r.URL.Path)
		assert.Equal(t, "latest", r.URL.Query().Get("filter"), "only the newest run per check name may be folded")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewClient("")
	c.baseURL = srv.URL
	sum, err := c.GetCheckRunsSummary(context.Background(), "o", "r", "abc")
	require.NoError(t, err)
	assert.Equal(t, ChecksSummary{Total: 3, Status: "failed", RunID: 9}, sum)
}

func TestGetPullRequestHead_ReadsHeadAndAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/pulls/81", r.URL.Path)
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"merged":true,"head":{"sha":" ABC123 "}}`))
	}))
	defer srv.Close()
	c := NewClient("tok")
	c.baseURL = srv.URL
	got, err := c.GetPullRequestHead(context.Background(), "o", "r", 81)
	require.NoError(t, err)
	assert.Equal(t, PullRequestHead{Merged: true, HeadSHA: "abc123"}, got)
}

func TestEvidenceReads_FailureModes(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"non-200", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{"bad json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := NewClient("")
			c.baseURL = srv.URL
			_, err := c.GetPullRequestHead(context.Background(), "o", "r", 1)
			require.Error(t, err)
			_, err = c.GetCheckRunsSummary(context.Background(), "o", "r", "abc")
			require.Error(t, err)
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		c := NewClient("")
		c.baseURL = "http://127.0.0.1:1"
		_, err := c.GetPullRequestHead(context.Background(), "o", "r", 1)
		require.Error(t, err)
	})
}

func TestGetCheckRunsSummary_Statuses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ChecksSummary
	}{
		{"running", `{"total_count":2,"check_runs":[{"id":2,"status":"completed","conclusion":"success"},{"id":3,"status":"in_progress","conclusion":""}]}`, ChecksSummary{Total: 2, Status: "running", RunID: 3}},
		{"all green", `{"total_count":1,"check_runs":[{"id":4,"status":"completed","conclusion":"neutral"}]}`, ChecksSummary{Total: 1, Status: "success", RunID: 4}},
		{"none", `{"total_count":0,"check_runs":[]}`, ChecksSummary{Status: "success"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			c := NewClient("")
			c.baseURL = srv.URL
			got, err := c.GetCheckRunsSummary(context.Background(), "o", "r", "abc")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGetCheckRunsSummary_MoreThanOnePageIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":150,"check_runs":[{"id":1,"status":"completed","conclusion":"success"}]}`))
	}))
	defer srv.Close()
	c := NewClient("")
	c.baseURL = srv.URL
	_, err := c.GetCheckRunsSummary(context.Background(), "o", "r", "abc")
	require.Error(t, err)
}
