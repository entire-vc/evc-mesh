package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Team Relay search's base URL used to come from a raw environment-variable
// read inline in Search() — a typo in the variable's name would only ever
// surface on a live request, never at startup. These tests exercise the
// replacement: the team-relay extension provider, resolved fresh on every call per
// specsintegration-provider-contract §4 (workspace row wins
// wholly over env; neither present → a NAMED 503, not a silent/opaque one).

func trSearchRequest(e *echo.Echo, projID string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/?q=hello", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tr/search")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID)
	return c, rec
}

func TestTrSearchHandler_RejectsAMalformedProjectID(t *testing.T) {
	h := NewTrSearchHandler(trIntegration(t, "demo", true), trProjectService(t), trRelayResolver(""))
	c, rec := trSearchRequest(echo.New(), "not-a-uuid")

	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTrSearchHandler_DisabledIntegrationIsNotFound(t *testing.T) {
	h := NewTrSearchHandler(trIntegration(t, "demo", false), trProjectService(t), trRelayResolver("https://irrelevant.example"))
	c, rec := trSearchRequest(echo.New(), uuid.New().String())

	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// AC3: neither a workspace row nor env supplies a relay URL → a 503 whose
// body names the actual reason, not an opaque downstream failure the caller
// has to guess at from a stack trace.
func TestTrSearchHandler_RelayNotConfigured_RefusesWithNamedReason(t *testing.T) {
	h := NewTrSearchHandler(trIntegration(t, "demo", true), trProjectService(t), trRelayResolver(""))
	c, rec := trSearchRequest(echo.New(), uuid.New().String())

	require.NoError(t, h.Search(c))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "relay URL not configured")
	assert.Contains(t, body, "team_relay")
}

// AC2 (env branch): with no workspace row at all, the env-fallback tier
// still lets a search succeed — the resolver's "workspace row wins, then
// env, then refuse" order, exercised through the handler end to end.
func TestTrSearchHandler_EnvFallback_Succeeds(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/web/shares/demo", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"docs":[]}`))
	}))
	defer relay.Close()

	h := NewTrSearchHandler(trIntegration(t, "demo", true), trProjectService(t), trRelayResolver(relay.URL))
	c, rec := trSearchRequest(echo.New(), uuid.New().String())

	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// The relay URL comes from the team-relay extension: a workspace row governs
// and the search goes to that relay, with only the relay itself faked.
func TestTrSearchHandler_WorkspaceRowThroughExtension_Succeeds(t *testing.T) {
	var hit bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		assert.Equal(t, "/v1/web/shares/demo", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"docs":[]}`))
	}))
	defer relay.Close()

	h := NewTrSearchHandler(trIntegration(t, "demo", true), trProjectService(t), trExtensionResolver(t, relay.URL, true))
	c, rec := trSearchRequest(echo.New(), uuid.New().String())

	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, hit, "the extension-resolved relay must be the one called")
}

// An explicit is_active=false workspace row switches Team Relay off: refused
// with the named reason, no call goes out.
func TestTrSearchHandler_WorkspaceRowInactiveThroughExtension_Refuses(t *testing.T) {
	h := NewTrSearchHandler(trIntegration(t, "demo", true), trProjectService(t), trExtensionResolver(t, "https://unused.example", false))
	c, rec := trSearchRequest(echo.New(), uuid.New().String())

	require.NoError(t, h.Search(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
