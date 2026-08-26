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

// --- DescribeAgentKey (#218d5847 AC4, evc-team-relay#230/#bc11d499) --------

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
