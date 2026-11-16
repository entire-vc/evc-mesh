package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// An integration created for a share that was never web-published is saved
// with share_id and an empty share_slug (resolveShare copies the share's empty
// web slug). Search and document reads used to require the slug and answered
// 422 "missing share_slug" for such projects, while the artifact upload worked
// by share_id all along. These tests pin the share_id path and the slug
// fallback for older integrations.

const trTestShareID = "4f0c2a8e-6b1d-4c3e-9a57-0d2e8b7c1f36"

func trIntegrationSettings(t *testing.T, settings domain.TeamRelaySettings) *MockProjectIntegrationService {
	t.Helper()
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	return &MockProjectIntegrationService{
		GetTeamRelayFunc: func(context.Context, uuid.UUID) (*domain.ProjectIntegration, error) {
			return &domain.ProjectIntegration{Enabled: true, AgentKey: trTestKey, Settings: raw}, nil
		},
	}
}

// fakeRelay answers the four Team Relay routes these handlers use. idStatus
// is what the share-ID routes answer (200 serves content); the slug routes
// always serve the slug "legacy".
func fakeRelay(t *testing.T, idStatus int, hits *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.URL.Path)
		assert.Equal(t, trTestKey, r.Header.Get("X-Agent-Key"))
		switch r.URL.Path {
		case "/v1/web/shares/" + trTestShareID + "/files-index":
			if idStatus != http.StatusOK {
				w.WriteHeader(idStatus)
				_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Agent key does not have read scope"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"share_id":"` + trTestShareID + `","files":{
				"notes/b-alpha.md":{"type":"doc"},
				"notes":{"type":"folder"},
				"a-alpha.md":{"type":"doc"},
				"zeta.md":{"type":"doc"}}}`))
		case "/v1/shares/" + trTestShareID + "/download":
			if idStatus != http.StatusOK {
				w.WriteHeader(idStatus)
				_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Agent key does not have read scope"}}`))
				return
			}
			assert.Equal(t, "notes/b-alpha.md", r.URL.Query().Get("path"))
			_, _ = w.Write([]byte("---\ntitle: B\n---\nbody by id"))
		case "/v1/web/shares/legacy":
			_, _ = w.Write([]byte(`{"web_folder_items":[{"name":"old-alpha.md","path":"old-alpha.md","type":"doc"}]}`))
		case "/v1/web/shares/legacy/files":
			_, _ = w.Write([]byte(`{"path":"notes/b-alpha.md","name":"b-alpha.md","type":"doc","content":"body by slug"}`))
		default:
			t.Errorf("unexpected relay call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func trSearchQuery(t *testing.T, h *TrSearchHandler, q string) (code int, docs []map[string]string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/?q="+q, http.NoBody)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())
	require.NoError(t, h.Search(c))
	var body struct {
		Docs []map[string]string `json:"docs"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.NotNil(t, body.Docs, "docs must be a list, not null: %s", rec.Body.String())
	}
	return rec.Code, body.Docs
}

func TestTrSearch_ShareIDOnly_SearchesByID(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusOK, &hits)
	defer relay.Close()

	h := NewTrSearchHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID}), trProjectService(t), trRelayResolver(relay.URL))
	code, docs := trSearchQuery(t, h, "alpha")

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"/v1/web/shares/" + trTestShareID + "/files-index"}, hits)
	// Folders skipped, filtered by q, ordered by path, title = basename, link by share ID.
	require.Len(t, docs, 2)
	assert.Equal(t, "a-alpha.md", docs[0]["path"])
	assert.Equal(t, "relay://"+trTestShareID+"/a-alpha.md", docs[0]["relay_url"])
	assert.Equal(t, "notes/b-alpha.md", docs[1]["path"])
	assert.Equal(t, "b-alpha.md", docs[1]["title"])
	assert.Equal(t, "relay://"+trTestShareID+"/notes/b-alpha.md", docs[1]["relay_url"])
}

func TestTrSearch_ShareSlugOnly_StillSearchesBySlug(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusOK, &hits)
	defer relay.Close()

	h := NewTrSearchHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareSlug: "legacy"}), trProjectService(t), trRelayResolver(relay.URL))
	code, docs := trSearchQuery(t, h, "alpha")

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"/v1/web/shares/legacy"}, hits)
	require.Len(t, docs, 1)
	assert.Equal(t, "relay://legacy/old-alpha.md", docs[0]["relay_url"])
}

// Both set and the key reads by ID: links keep the slug, so links already
// pasted into tasks and the new ones look the same.
func TestTrSearch_BothSet_SearchesByIDWithSlugLinks(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusOK, &hits)
	defer relay.Close()

	h := NewTrSearchHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID, ShareSlug: "legacy"}), trProjectService(t), trRelayResolver(relay.URL))
	code, docs := trSearchQuery(t, h, "zeta")

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"/v1/web/shares/" + trTestShareID + "/files-index"}, hits)
	require.Len(t, docs, 1)
	assert.Equal(t, "relay://legacy/zeta.md", docs[0]["relay_url"])
}

// An older integration with a write-only key: the ID route refuses it (403,
// needs read scope), the slug route still answers.
func TestTrSearch_IDRouteRefused_FallsBackToSlug(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusForbidden, &hits)
	defer relay.Close()

	h := NewTrSearchHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID, ShareSlug: "legacy"}), trProjectService(t), trRelayResolver(relay.URL))
	code, docs := trSearchQuery(t, h, "alpha")

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"/v1/web/shares/" + trTestShareID + "/files-index", "/v1/web/shares/legacy"}, hits)
	require.Len(t, docs, 1)
	assert.Equal(t, "relay://legacy/old-alpha.md", docs[0]["relay_url"])
}

// No slug to fall back to: the refusal is the relay's, reported as 502.
func TestTrSearch_IDRouteRefused_NoSlug_IsBadGateway(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusForbidden, &hits)
	defer relay.Close()

	h := NewTrSearchHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID}), trProjectService(t), trRelayResolver(relay.URL))
	code, _ := trSearchQuery(t, h, "alpha")

	assert.Equal(t, http.StatusBadGateway, code)
	assert.Len(t, hits, 1)
}

func TestTrSearch_NeitherIDNorSlug_IsUnprocessable(t *testing.T) {
	h := NewTrSearchHandler(trIntegrationSettings(t, domain.TeamRelaySettings{}), trProjectService(t), trRelayResolver("https://unused.example"))
	code, _ := trSearchQuery(t, h, "alpha")

	assert.Equal(t, http.StatusUnprocessableEntity, code)
}

func trDocumentGet(t *testing.T, h *TrDocumentHandler, relayURL string) map[string]any {
	t.Helper()
	c, rec := trDocRequest(echo.New(), uuid.New().String(), relayURL)
	require.NoError(t, h.Get(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), trTestKey)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestTrDocument_ShareIDLink_ReadsByID(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusOK, &hits)
	defer relay.Close()

	h := NewTrDocumentHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID}), trProjectService(t), trRelayResolver(relay.URL))
	body := trDocumentGet(t, h, "relay://"+trTestShareID+"/notes/b-alpha.md")

	assert.Equal(t, true, body["available"])
	assert.Equal(t, "notes/b-alpha.md", body["path"])
	assert.Equal(t, "b-alpha.md", body["name"])
	assert.Equal(t, "body by id", body["content"], "front matter stripped")
	assert.Equal(t, []string{"/v1/shares/" + trTestShareID + "/download"}, hits)
}

// The share ID in a link is a UUID: case does not make it another share.
func TestTrDocument_ShareIDLink_IsCaseInsensitive(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusOK, &hits)
	defer relay.Close()

	h := NewTrDocumentHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID}), trProjectService(t), trRelayResolver(relay.URL))
	body := trDocumentGet(t, h, "relay://4F0C2A8E-6B1D-4C3E-9A57-0D2E8B7C1F36/notes/b-alpha.md")

	assert.Equal(t, true, body["available"])
}

func TestTrDocument_SlugLink_IDRouteRefused_FallsBackToSlug(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusForbidden, &hits)
	defer relay.Close()

	h := NewTrDocumentHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID, ShareSlug: "legacy"}), trProjectService(t), trRelayResolver(relay.URL))
	body := trDocumentGet(t, h, "relay://legacy/notes/b-alpha.md")

	assert.Equal(t, true, body["available"])
	assert.Equal(t, "body by slug", body["content"])
	assert.Equal(t, []string{"/v1/shares/" + trTestShareID + "/download", "/v1/web/shares/legacy/files"}, hits)
}

func TestTrDocument_ForeignShareIDLink_IsUnavailable(t *testing.T) {
	var hits []string
	relay := fakeRelay(t, http.StatusOK, &hits)
	defer relay.Close()

	h := NewTrDocumentHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID}), trProjectService(t), trRelayResolver(relay.URL))
	body := trDocumentGet(t, h, "relay://"+uuid.NewString()+"/notes/b-alpha.md")

	assert.Equal(t, false, body["available"])
	assert.Empty(t, hits, "no relay call with our key for a foreign share")
}

func TestTrDocument_NeitherIDNorSlug_IsUnavailable(t *testing.T) {
	h := NewTrDocumentHandler(trIntegrationSettings(t, domain.TeamRelaySettings{}), trProjectService(t), trRelayResolver("https://unused.example"))
	body := trDocumentGet(t, h, "relay:///notes/b-alpha.md")

	assert.Equal(t, false, body["available"])
}

// No slug to fall back to: a missing document and a refused key are both an
// ordinary "unavailable", not an error page.
func TestTrDocument_ShareIDOnly_RefusedOrMissingIsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		var hits []string
		relay := fakeRelay(t, status, &hits)

		h := NewTrDocumentHandler(trIntegrationSettings(t, domain.TeamRelaySettings{ShareID: trTestShareID}), trProjectService(t), trRelayResolver(relay.URL))
		body := trDocumentGet(t, h, "relay://"+trTestShareID+"/notes/b-alpha.md")
		relay.Close()

		assert.Equal(t, false, body["available"], "status %d", status)
		assert.Equal(t, []string{"/v1/shares/" + trTestShareID + "/download"}, hits)
	}
}

func TestTrDocument_CorruptSettingsIsUnavailable(t *testing.T) {
	pi := &MockProjectIntegrationService{
		GetTeamRelayFunc: func(context.Context, uuid.UUID) (*domain.ProjectIntegration, error) {
			return &domain.ProjectIntegration{Enabled: true, AgentKey: trTestKey, Settings: []byte(`"not an object"`)}, nil
		},
	}
	h := NewTrDocumentHandler(pi, trProjectService(t), trRelayResolver("https://unused.example"))
	body := trDocumentGet(t, h, "relay://"+trTestShareID+"/notes/b-alpha.md")

	assert.Equal(t, false, body["available"])
}
