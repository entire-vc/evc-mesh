package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

const testAgentKey = "tr_agent_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

type stubProjectIntegrationService struct {
	service.ProjectIntegrationService
	pi *domain.ProjectIntegration
}

func (s *stubProjectIntegrationService) GetTeamRelay(_ context.Context, _ uuid.UUID) (*domain.ProjectIntegration, error) {
	return s.pi, nil
}

// The acceptance criterion's negative control: make fetchEmbedToken fail the way
// it fails in production — an unreachable/erroring web-publish — and show the
// response is { available: false } rather than a URL carrying the long-lived key.
func TestTrPreviewURL_EmbedTokenFailure_DoesNotLeakAgentKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"endpoint returns 500", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
		{"endpoint returns 404 (older web-publish, no embed-token route)", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}},
		{"endpoint rejects the key", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}},
		{"endpoint returns unparseable body", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}},
		{"endpoint returns 200 with an empty embed_url", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"embed_url":"","expires_at":"later"}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			t.Setenv("MESH_TEAMRELAY_WEB_BASE_URL", srv.URL)

			rec, body := callPreviewURL(t)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.NotContains(t, rec.Body.String(), "agent_key",
				"the response must not carry an agent_key parameter")
			assert.NotContains(t, rec.Body.String(), testAgentKey,
				"the long-lived key must not appear in the response in any form")
			assert.Equal(t, false, body["available"],
				"a failed embed token must report the preview as unavailable")
			assert.NotContains(t, body, "iframe_src",
				"no iframe_src should be offered when no short-lived token was obtained")
		})
	}
}

// Positive control: when the embed token IS obtained, the handler still works and
// returns the short-lived URL. Without this, the test above would also pass on a
// handler that had simply stopped working.
func TestTrPreviewURL_EmbedTokenSuccess_ReturnsShortLivedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The key travels in the Authorization header, never in a query string.
		assert.Equal(t, "Bearer "+testAgentKey, r.Header.Get("Authorization"))
		assert.Empty(t, r.URL.Query().Get("agent_key"))
		_, _ = w.Write([]byte(`{"embed_url":"https://relay.example.com/s/doc?embed_token=short","expires_at":"soon"}`))
	}))
	defer srv.Close()
	t.Setenv("MESH_TEAMRELAY_WEB_BASE_URL", srv.URL)

	rec, body := callPreviewURL(t)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, body["available"])
	assert.Equal(t, "https://relay.example.com/s/doc?embed_token=short", body["iframe_src"])
	assert.NotContains(t, rec.Body.String(), testAgentKey)
}

func callPreviewURL(t *testing.T) (rec *httptest.ResponseRecorder, body map[string]any) {
	t.Helper()

	settings, err := json.Marshal(domain.TeamRelaySettings{ShareSlug: "myshare"})
	require.NoError(t, err)

	h := NewTrPreviewURLHandler(&stubProjectIntegrationService{
		pi: &domain.ProjectIntegration{Enabled: true, AgentKey: testAgentKey, Settings: settings},
	})

	relay := "relay://myshare/Knowledge/notes.md"
	req := httptest.NewRequest(http.MethodGet, "/?relay_url="+url.QueryEscape(relay), http.NoBody)
	rec = httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tr/preview-url")
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Get(c))

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec, body
}
