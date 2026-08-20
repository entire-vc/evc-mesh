package teamrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reading a document's source from a Team Relay share.
//
// The endpoint choice is the load-bearing decision here — /files accepts the
// write-scoped keys we already hold, /download demands a literal read scope our
// key may not have — so the test asserts WHICH path is called, not merely that
// something was.

func TestFetchDocument_CallsFilesAndSendsTheKeyAsAHeader(t *testing.T) {
	var gotPath, gotQuery, gotHeader, gotRawURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("path")
		gotHeader = r.Header.Get("X-Agent-Key")
		gotRawURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"Demo/Welcome.md","name":"Welcome","type":"doc","content":"# Hello"}`))
	}))
	defer srv.Close()

	doc, err := FetchDocument(context.Background(), srv.URL, "demo", "Demo/Welcome.md", "tr_agent_secret")

	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, "# Hello", doc.Content)
	// /files, not /download: the latter requires a literal "read" scope, and our
	// keys are issued write-only by default.
	assert.Equal(t, "/v1/web/shares/demo/files", gotPath)
	assert.Equal(t, "Demo/Welcome.md", gotQuery)
	// Header, never a query parameter — a credential in a URL reaches proxy logs,
	// referrers and browser history.
	assert.Equal(t, "tr_agent_secret", gotHeader)
	assert.NotContains(t, gotRawURL, "tr_agent_secret")
	assert.NotContains(t, gotRawURL, "agent_key")
}

func TestFetchDocument_MissingIsNilNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	doc, err := FetchDocument(context.Background(), srv.URL, "demo", "gone.md", "k")

	// nil, not an error: a document that is not there is not a failure of ours,
	// and the caller answers "unavailable" either way.
	require.NoError(t, err)
	assert.Nil(t, doc)
}

func TestFetchDocument_RejectedKeyIsAnError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		doc, err := FetchDocument(context.Background(), srv.URL, "demo", "x.md", "k")
		srv.Close()

		// Distinct from "not found": a rejected key is a configuration problem
		// somebody has to fix, and silently reporting it as a missing document
		// would hide it forever.
		require.Error(t, err)
		assert.Nil(t, doc)
	}
}

func TestFetchDocument_RefusesWithoutARelayURL(t *testing.T) {
	_, err := FetchDocument(context.Background(), "", "demo", "x.md", "k")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Front matter
// ---------------------------------------------------------------------------

func TestStripFrontMatter(t *testing.T) {
	t.Run("removes an Obsidian block", func(t *testing.T) {
		body, fm := StripFrontMatter("---\ntitle: \"Welcome\"\n---\n\n# Hello\n")
		assert.Equal(t, "\n# Hello\n", body)
		assert.Contains(t, fm, `title: "Welcome"`)
	})

	t.Run("leaves a document that has none", func(t *testing.T) {
		body, fm := StripFrontMatter("# Hello\n\ntext")
		assert.Equal(t, "# Hello\n\ntext", body)
		assert.Equal(t, "", fm)
	})

	t.Run("leaves a document that merely STARTS with a rule", func(t *testing.T) {
		// An unterminated block is not front matter. Guessing otherwise would eat
		// the entire document.
		source := "---\nthis is just text under a horizontal rule\n"
		body, fm := StripFrontMatter(source)
		assert.Equal(t, source, body)
		assert.Equal(t, "", fm)
	})

	t.Run("handles CRLF", func(t *testing.T) {
		body, _ := StripFrontMatter("---\r\ntitle: x\r\n---\r\n\r\n# Hi")
		assert.Contains(t, body, "# Hi")
		assert.NotContains(t, body, "title: x")
	})
}
