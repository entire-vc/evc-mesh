package teamrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- DescribeAgentKey (an internal ticket AC4, evc-team-relay#230/an internal ticket) --------

func TestDescribeAgentKey_ReadsExpiryScopesAndHitsTheWebRouter(t *testing.T) {
	var gotPath string
	var gotKeyHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKeyHeader = r.Header.Get("X-Agent-Key")
		_, _ = w.Write([]byte(`{
			"id": "key-1",
			"label": "mesh sync",
			"share_id": "share-1",
			"scopes": ["read", "write"],
			"expires_at": "2026-11-24T00:00:00Z",
			"last_used_at": "2026-08-25T12:00:00Z"
		}`))
	}))
	defer srv.Close()

	desc, err := DescribeAgentKey(context.Background(), srv.URL, "share-1", "the-key")

	require.NoError(t, err)
	require.NotNil(t, desc)
	assert.Equal(t, "/v1/web/shares/share-1/agent-key", gotPath, "must hit the web router, not /v1/shares — that is where Team Relay actually implemented this")
	assert.Equal(t, "the-key", gotKeyHeader)
	assert.Equal(t, []string{"read", "write"}, desc.Scopes)
	require.NotNil(t, desc.ExpiresAt)
	assert.Equal(t, 2026, desc.ExpiresAt.Year())
	assert.Equal(t, time.November, desc.ExpiresAt.Month())
}

func TestDescribeAgentKey_NoExpiryIsNilNotZeroTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"key-1","label":null,"share_id":"share-1","scopes":["write"],"expires_at":null,"last_used_at":null}`))
	}))
	defer srv.Close()

	desc, err := DescribeAgentKey(context.Background(), srv.URL, "share-1", "the-key")

	require.NoError(t, err)
	assert.Nil(t, desc.ExpiresAt, "a key issued with no expiry must read as nil, not a zero-value time.Time that would compare as already-expired")
}

// Same 403-disambiguation contract as the sync-protocol routes — this route
// resolves the key through the same underlying check on the Team Relay side.
func TestDescribeAgentKey_ExpiredKeyIsErrKeyExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Agent key has expired","request_id":"req-3"}}`))
	}))
	defer srv.Close()

	_, err := DescribeAgentKey(context.Background(), srv.URL, "share-1", "the-key")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyExpired)
}

// --- ShareOfAgentKey ---------------------------------------------------------

func shareOfKeyRelay(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/web/agent-key" || r.Header.Get("X-Agent-Key") != "the-key" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestShareOfAgentKey_ReadsTheKeysShare(t *testing.T) {
	srv := shareOfKeyRelay(t, http.StatusOK, `{"id":"k1","share_id":"share-1",
		"share":{"id":"share-1","kind":"folder","path":"/Vault/Mesh Docs/","web_slug":"mesh-docs"}}`)

	share, err := ShareOfAgentKey(context.Background(), srv.URL+"/", "the-key")

	require.NoError(t, err)
	assert.Equal(t, "share-1", share.ID)
	assert.Equal(t, "mesh-docs", share.WebSlug)
	assert.Equal(t, "Vault/Mesh Docs", share.Name())
}

// Shares are commonly a "docs" folder inside a project folder, so the last
// path segment repeats across shares; the name keeps the whole path.
func TestKeyShareName_TellsSharesApart(t *testing.T) {
	mesh := KeyShare{Path: "Entire VC/Mesh/docs"}
	spark := KeyShare{Path: "/Entire VC/Spark/docs/"}
	assert.Equal(t, "Entire VC/Mesh/docs", mesh.Name())
	assert.Equal(t, "Entire VC/Spark/docs", spark.Name())
	assert.NotEqual(t, mesh.Name(), spark.Name())
}

// An older relay names only share_id; a share object without an id inherits it.
func TestShareOfAgentKey_ShareIDWithoutShareObject(t *testing.T) {
	share, err := ShareOfAgentKey(context.Background(), shareOfKeyRelay(t, http.StatusOK, `{"share_id":"share-1"}`).URL, "the-key")
	require.NoError(t, err)
	assert.Equal(t, "share-1", share.ID)
	assert.Equal(t, "", share.Name())

	share, err = ShareOfAgentKey(context.Background(), shareOfKeyRelay(t, http.StatusOK, `{"share_id":"share-1","share":{"path":"Notes"}}`).URL, "the-key")
	require.NoError(t, err)
	assert.Equal(t, "share-1", share.ID)
	assert.Equal(t, "Notes", share.Name())
}

func TestShareOfAgentKey_Errors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		is     error
	}{
		{"rejected key", http.StatusUnauthorized, ``, ErrKeyRejected},
		{"expired key", http.StatusForbidden, `{"error":{"code":403,"message":"Agent key has expired"}}`, ErrKeyExpired},
		{"relay without the route", http.StatusNotFound, ``, ErrNotFound},
		{"no share named", http.StatusOK, `{"id":"k1"}`, nil},
		{"unparseable body", http.StatusOK, `not json`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			share, err := ShareOfAgentKey(context.Background(), shareOfKeyRelay(t, tc.status, tc.body).URL, "the-key")
			require.Error(t, err)
			assert.Nil(t, share)
			if tc.is != nil {
				assert.ErrorIs(t, err, tc.is)
			}
		})
	}
}

func TestShareOfAgentKey_NoRelayOrUnreachable(t *testing.T) {
	_, err := ShareOfAgentKey(context.Background(), "", "the-key")
	require.Error(t, err)

	_, err = ShareOfAgentKey(context.Background(), "http://bad host", "the-key")
	require.Error(t, err)

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err = ShareOfAgentKey(context.Background(), url, "the-key")
	assert.ErrorIs(t, err, ErrUnreachable)
}
