package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRESTClient_GetTaskComments_RequestsNewestPageAndRestoresChronologicalOrder
// pins D1 (task 4222c17d): get_task(include_comments=true) used to request
// the server's untouched default (oldest DefaultPageSize, ASC) with no way to
// reach the tail of a long thread. The fix requests sort_dir=desc (the newest
// page) and reverses it back to chronological order for display — this test
// asserts both halves: the outgoing request shape, and the returned order.
func TestRESTClient_GetTaskComments_RequestsNewestPageAndRestoresChronologicalOrder(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		// Server-side sort_dir=desc would return newest-first; the fixture
		// mirrors that so the test proves the client reverses it back.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "c3", "body": "newest", "created_at": "2026-08-05T00:00:00Z"},
				{"id": "c2", "body": "middle", "created_at": "2026-08-01T00:00:00Z"},
				{"id": "c1", "body": "oldest", "created_at": "2026-06-21T00:00:00Z"},
			},
			"total_count": 106,
			"has_more":    true,
			"page":        1,
			"page_size":   50,
		})
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "agk_test_key")
	result, err := c.GetTaskComments(t.Context(), "8a98ddd3-566c-4e81-b2d1-b92103d2ef03")
	require.NoError(t, err)

	assert.Contains(t, gotQuery, "sort_dir=desc", "must request the newest page, not the server default")
	assert.Contains(t, gotQuery, "include_internal=true")

	items, ok := result["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 3)

	idOf := func(v any) string {
		m, ok := v.(map[string]any)
		require.True(t, ok)
		id, _ := m["id"].(string)
		return id
	}
	assert.Equal(t, "c1", idOf(items[0]), "oldest of the returned page must come first")
	assert.Equal(t, "c2", idOf(items[1]))
	assert.Equal(t, "c3", idOf(items[2]), "newest of the returned page must be last — the envelope's own promise")

	// The truncation envelope must survive — this is what handleGetTask (D2)
	// depends on to surface total_count/has_more instead of discarding them.
	assert.InEpsilon(t, float64(106), result["total_count"], 0)
	assert.Equal(t, true, result["has_more"])
}

// TestRESTClient_GetTaskComments_EmptyThread guards the reverse-in-place loop
// against an empty or single-item items slice (off-by-one is the classic
// failure mode for a two-pointer swap).
func TestRESTClient_GetTaskComments_EmptyThread(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{}, "total_count": 0, "has_more": false, "page": 1, "page_size": 50,
		})
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "agk_test_key")
	result, err := c.GetTaskComments(t.Context(), "no-comments-task")
	require.NoError(t, err)

	items, ok := result["items"].([]any)
	require.True(t, ok)
	assert.Empty(t, items)
}

// TestRESTClient_GetTaskComments_SingleItem guards the same swap loop's other
// edge: one item must not panic or get dropped by the i<j loop condition.
func TestRESTClient_GetTaskComments_SingleItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":       []map[string]any{{"id": "only", "body": "hi"}},
			"total_count": 1, "has_more": false, "page": 1, "page_size": 50,
		})
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "agk_test_key")
	result, err := c.GetTaskComments(t.Context(), "one-comment-task")
	require.NoError(t, err)

	items, ok := result["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	m, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "only", m["id"])
}
