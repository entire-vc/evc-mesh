package teamrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const searchTestShareID = "4f0c2a8e-6b1d-4c3e-9a57-0d2e8b7c1f36"

// ---------------------------------------------------------------------------
// SearchDocsByShareID — the route for shares that were never web-published
// ---------------------------------------------------------------------------

func TestSearchDocsByShareID_ListsDocsByPathSkippingFolders(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Agent-Key")
		_, _ = w.Write([]byte(`{"share_id":"` + searchTestShareID + `","files":{
			"notes/b.md":{"type":"doc"},
			"notes":{"type":"folder"},
			"a.md":{"type":"doc"},
			"board.canvas":{"type":"canvas"}}}`))
	}))
	defer srv.Close()

	docs, err := SearchDocsByShareID(context.Background(), srv.URL+"/", searchTestShareID, "pub", "tr_agent_secret", "", 0)

	require.NoError(t, err)
	assert.Equal(t, "/v1/web/shares/"+searchTestShareID+"/files-index", gotPath, "web UUID route, not the sync-only /v1/shares index")
	assert.Equal(t, "tr_agent_secret", gotKey)
	assert.Equal(t, []RelayDoc{
		{ID: "a.md", Title: "a.md", Path: "a.md", RelayURL: "relay://pub/a.md"},
		{ID: "board.canvas", Title: "board.canvas", Path: "board.canvas", RelayURL: "relay://pub/board.canvas"},
		{ID: "notes/b.md", Title: "b.md", Path: "notes/b.md", RelayURL: "relay://pub/notes/b.md"},
	}, docs)
}

func TestSearchDocsByShareID_FiltersByQueryAndLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"files":{"Plan-1.md":{"type":"doc"},"plan-2.md":{"type":"doc"},"other.md":{"type":"doc"}}}`))
	}))
	defer srv.Close()

	docs, err := SearchDocsByShareID(context.Background(), srv.URL, searchTestShareID, searchTestShareID, "k", "PLAN", 1)

	require.NoError(t, err)
	require.Len(t, docs, 1)
	assert.Equal(t, "Plan-1.md", docs[0].Path)
	assert.Equal(t, "relay://"+searchTestShareID+"/Plan-1.md", docs[0].RelayURL)
}

func TestSearchDocsByShareID_EmptyShareIsAnEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"files":{}}`))
	}))
	defer srv.Close()

	docs, err := SearchDocsByShareID(context.Background(), srv.URL, searchTestShareID, "x", "k", "q", 0)

	require.NoError(t, err)
	assert.NotNil(t, docs)
	assert.Empty(t, docs)
}

func TestSearchDocsByShareID_ClassifiesRefusals(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, `{}`, ErrKeyRejected},
		{http.StatusForbidden, `{"error":{"code":403,"message":"Agent key does not have read scope"}}`, ErrForeignShare},
		{http.StatusNotFound, `{}`, ErrNotFound},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := SearchDocsByShareID(context.Background(), srv.URL, searchTestShareID, "x", "k", "", 0)
		srv.Close()
		assert.ErrorIs(t, err, tc.want, "status %d", tc.status)
	}
}

func TestSearchDocsByShareID_BadJSONIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := SearchDocsByShareID(context.Background(), srv.URL, searchTestShareID, "x", "k", "", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse files-index")
}

func TestSearchDocsByShareID_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	_, err := SearchDocsByShareID(context.Background(), base, searchTestShareID, "x", "k", "", 0)

	assert.ErrorIs(t, err, ErrUnreachable)
}

func TestSearchDocsByShareID_NoRelayURL(t *testing.T) {
	_, err := SearchDocsByShareID(context.Background(), "", searchTestShareID, "x", "k", "", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "relay URL not configured")
}

func TestSearchDocsByShareID_BadRelayURLFailsToBuildRequest(t *testing.T) {
	_, err := SearchDocsByShareID(context.Background(), "http://bad host", searchTestShareID, "x", "k", "", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "build request")
}

// ---------------------------------------------------------------------------
// SearchDocs — the slug route, kept as the fallback for older integrations
// ---------------------------------------------------------------------------

func TestSearchDocs_ListsPublishedShareBySlug(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"web_folder_items":[
			{"name":"Notes","path":"Notes","type":"folder"},
			{"name":"Welcome.md","path":"Notes/Welcome.md","type":"doc"}]}`))
	}))
	defer srv.Close()

	docs, err := SearchDocs(context.Background(), srv.URL, "pub", "k", "welcome", 0)

	require.NoError(t, err)
	assert.Equal(t, "/v1/web/shares/pub", gotPath)
	assert.Equal(t, []RelayDoc{{ID: "Notes/Welcome.md", Title: "Welcome.md", Path: "Notes/Welcome.md", RelayURL: "relay://pub/Notes/Welcome.md"}}, docs)
}

func TestSearchDocs_RelayErrorAndBadJSON(t *testing.T) {
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer refused.Close()
	_, err := SearchDocs(context.Background(), refused.URL, "pub", "k", "", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 403")

	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer garbled.Close()
	_, err = SearchDocs(context.Background(), garbled.URL, "pub", "k", "", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}
