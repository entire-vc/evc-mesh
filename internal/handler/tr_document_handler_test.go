package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Reading a Team Relay document server-side, so that Docs can render it with our
// own editor instead of embedding Team Relay's rendered page in an iframe.
//
// The property that matters most here is a negative one: the integration key is
// long-lived and does not rotate, and it must not appear in ANY response this
// handler produces. That is asserted against the whole body rather than against
// a named field, because a key leaks through whichever field somebody adds next.

const trTestKey = "tr_agent_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

func trDocRequest(e *echo.Echo, projID, relayURL string) (echo.Context, *httptest.ResponseRecorder) {
	target := "/?relay_url=" + url.QueryEscape(relayURL)
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tr/document")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID)
	return c, rec
}

func trIntegration(t *testing.T, slug string, enabled bool) *MockProjectIntegrationService {
	t.Helper()
	settings, err := json.Marshal(domain.TeamRelaySettings{ShareSlug: slug})
	require.NoError(t, err)
	return &MockProjectIntegrationService{
		GetTeamRelayFunc: func(context.Context, uuid.UUID) (*domain.ProjectIntegration, error) {
			return &domain.ProjectIntegration{
				Enabled:  enabled,
				AgentKey: trTestKey,
				Settings: settings,
			}, nil
		},
	}
}

func TestTrDocumentHandler_FolderLinkIsUnavailableNotAnError(t *testing.T) {
	// A link to the share root has no document behind it. That is an ordinary
	// state of a link somebody pasted months ago, not a failure.
	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	c, rec := trDocRequest(echo.New(), uuid.New().String(), "relay://demo")

	require.NoError(t, h.Get(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"available":false`)
}

func TestTrDocumentHandler_ForeignShareIsRefused(t *testing.T) {
	// Reading it would mean using THIS project's credential to fetch content the
	// caller has no relationship with.
	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	c, rec := trDocRequest(echo.New(), uuid.New().String(), "relay://someone-else/Notes.md")

	require.NoError(t, h.Get(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"available":false`)
}

func TestTrDocumentHandler_DisabledIntegrationIsUnavailable(t *testing.T) {
	h := NewTrDocumentHandler(trIntegration(t, "demo", false))
	c, rec := trDocRequest(echo.New(), uuid.New().String(), "relay://demo/Notes.md")

	require.NoError(t, h.Get(c))

	assert.Contains(t, rec.Body.String(), `"available":false`)
}

func TestTrDocumentHandler_RefusesPathTraversal(t *testing.T) {
	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	c, rec := trDocRequest(echo.New(), uuid.New().String(), "relay://demo/../../etc/passwd")

	require.NoError(t, h.Get(c))

	// Refused here rather than forwarded: `..` handed to another service is a
	// traversal attempt whichever way that service resolves it.
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTrDocumentHandler_RejectsANonRelayURL(t *testing.T) {
	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	c, rec := trDocRequest(echo.New(), uuid.New().String(), "https://example.com/x.md")

	require.NoError(t, h.Get(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTrDocumentHandler_RejectsAMissingRelayURL(t *testing.T) {
	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Get(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTrDocumentHandler_RejectsAMalformedProjectID(t *testing.T) {
	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	c, rec := trDocRequest(echo.New(), "not-a-uuid", "relay://demo/Notes.md")

	require.NoError(t, h.Get(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// The one that matters. Every path this handler can take, checked for the key —
// INCLUDING the one that succeeds.
//
// The success case is why this test is shaped this way. An earlier version
// omitted it, and a mutation that added the key to the response body as an extra
// field passed cleanly: with no relay to answer, every case fell out at
// "unavailable" before a response was ever built, so the test was checking four
// early returns while wearing the name of a security property. A relay is
// stubbed here so the assertion runs against the body a reader would receive.
func TestTrDocumentHandler_NeverPutsTheIntegrationKeyInAResponse(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"Notes.md","name":"Notes","type":"doc","content":"# Notes"}`))
	}))
	defer relay.Close()
	t.Setenv("MESH_TEAMRELAY_RELAY_URL", relay.URL)

	cases := map[string]string{
		"folder link":   "relay://demo",
		"foreign share": "relay://someone-else/Notes.md",
		"traversal":     "relay://demo/../secrets.md",
		"non-relay url": "https://example.com/x.md",
		// The path that returns a document. Without it the test is four early
		// returns and cannot see a leak in the body at all.
		"document that is actually returned": "relay://demo/Notes.md",
	}

	for name, relayURL := range cases {
		t.Run(name, func(t *testing.T) {
			h := NewTrDocumentHandler(trIntegration(t, "demo", true))
			c, rec := trDocRequest(echo.New(), uuid.New().String(), relayURL)

			require.NoError(t, h.Get(c))

			body := rec.Body.String()
			// Not "the key is not in field X" — a key leaks through whichever
			// field somebody adds next, so the whole body is the assertion.
			assert.NotContains(t, body, trTestKey)
			assert.NotContains(t, body, "tr_agent_")
			assert.False(t, strings.Contains(body, "agent_key="),
				"a key in a query string reaches proxy logs, referrers and history")
		})
	}
}

// Proof that the success case above really does succeed — otherwise it would be
// a fifth early return, and the leak assertion would still be vacuous.
func TestTrDocumentHandler_ReturnsTheDocumentSource(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/web/shares/demo/files", r.URL.Path)
		assert.Equal(t, trTestKey, r.Header.Get("X-Agent-Key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"Notes.md","name":"Notes","type":"doc","content":"---\ntitle: x\n---\n\n# Notes"}`))
	}))
	defer relay.Close()
	t.Setenv("MESH_TEAMRELAY_RELAY_URL", relay.URL)

	h := NewTrDocumentHandler(trIntegration(t, "demo", true))
	c, rec := trDocRequest(echo.New(), uuid.New().String(), "relay://demo/Notes.md")

	require.NoError(t, h.Get(c))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Available bool   `json:"available"`
		Name      string `json:"name"`
		Content   string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Available)
	assert.Equal(t, "Notes", body.Name)
	assert.Contains(t, body.Content, "# Notes")
	// Obsidian front matter is stripped for display: left in, it renders as a
	// horizontal rule followed by raw `title:` lines — the first thing a reader
	// sees, and not the document.
	assert.NotContains(t, body.Content, "title: x")
}
