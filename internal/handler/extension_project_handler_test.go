package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// stubExtensionLookup answers Get for the ids it holds; the providers only
// need a manifest.
type stubExtensionLookup map[string]extensions.Provider

func (s stubExtensionLookup) Get(id string) (extensions.Provider, bool) {
	p, ok := s[id]
	return p, ok
}

// projectManifestProvider implements only Manifest; the project routes call nothing else.
type projectManifestProvider struct {
	extensions.Provider
	man extensions.Manifest
}

func (p projectManifestProvider) Manifest() extensions.Manifest { return p.man }

func manifestOnlyProvider(project bool) extensions.Provider {
	return projectManifestProvider{man: extensions.Manifest{
		ID:     teamRelayExtensionID,
		Levels: extensions.Levels{Project: project},
	}}
}

func serveProjectExtension(h *ExtensionProjectHandler, method, path, body string) (code int, out map[string]any) {
	e := echo.New()
	const route = "/projects/:proj_id/extensions/:ext_id"
	e.GET(route, h.Get)
	e.PUT(route, h.Put)
	e.DELETE(route, h.Delete)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestExtensionProjectHandler_GetMasksAgentKey(t *testing.T) {
	projectID := uuid.New()
	svc := &MockProjectIntegrationService{
		GetTeamRelayFunc: func(_ context.Context, id uuid.UUID) (*domain.ProjectIntegration, error) {
			require.Equal(t, projectID, id)
			return sampleTeamRelayIntegration(projectID, "tr_live_secret"), nil
		},
	}
	h := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)}, svc)

	code, out := serveProjectExtension(h, http.MethodGet, "/projects/"+projectID.String()+"/extensions/team-relay", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, teamRelayExtensionID, out["extension_id"])
	settings := out["settings"].(map[string]any)
	assert.Equal(t, maskedSecret, settings["agent_key"], "a stored key reads back as ***")
	assert.Equal(t, "probe-share", settings["share_slug"])
}

func TestExtensionProjectHandler_GetServiceErrorPropagates(t *testing.T) {
	svc := &MockProjectIntegrationService{
		GetTeamRelayFunc: func(context.Context, uuid.UUID) (*domain.ProjectIntegration, error) {
			return nil, apierror.NotFound("ProjectIntegration")
		},
	}
	h := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)}, svc)

	code, _ := serveProjectExtension(h, http.MethodGet, "/projects/"+uuid.NewString()+"/extensions/team-relay", "")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestExtensionProjectHandler_TargetRefusals(t *testing.T) {
	svc := &MockProjectIntegrationService{}
	path := "/projects/" + uuid.NewString() + "/extensions/team-relay"

	notRegistered := NewExtensionProjectHandler(stubExtensionLookup{}, svc)
	code, _ := serveProjectExtension(notRegistered, http.MethodGet, path, "")
	assert.Equal(t, http.StatusNotFound, code, "team-relay not registered")

	noProjectLevel := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(false)}, svc)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		code, _ = serveProjectExtension(noProjectLevel, m, path, `{}`)
		assert.Equal(t, http.StatusNotFound, code, "%s without levels.project in the manifest", m)
	}

	ok := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)}, svc)
	code, _ = serveProjectExtension(ok, http.MethodGet, "/projects/not-a-uuid/extensions/team-relay", "")
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestExtensionProjectHandler_PutKeepsStoredKeyAndRefusesUnknownSettings(t *testing.T) {
	projectID := uuid.New()
	var got service.UpsertProjectIntegrationInput
	svc := &MockProjectIntegrationService{
		UpsertTeamRelayFunc: func(_ context.Context, _ uuid.UUID, in service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
			got = in
			return sampleTeamRelayIntegration(projectID, "tr_live_secret"), nil
		},
	}
	h := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)}, svc)
	path := "/projects/" + projectID.String() + "/extensions/team-relay"

	code, out := serveProjectExtension(h, http.MethodPut, path,
		`{"enabled":true,"settings":{"share_slug":"probe-share","agent_key":"***","docs_mount_path":"External/Notes"}}`)
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, got.AgentKey, "*** means keep the stored key")
	assert.True(t, got.Enabled)
	assert.Equal(t, "probe-share", got.ShareSlug)
	assert.Equal(t, maskedSecret, out["settings"].(map[string]any)["agent_key"])

	code, _ = serveProjectExtension(h, http.MethodPut, path, `{"enabled":true,"settings":{"agent_key":"tr_new"}}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "tr_new", got.AgentKey, "a real value replaces the key")

	code, _ = serveProjectExtension(h, http.MethodPut, path, `{"enabled":true,"settings":{"no_such_key":1}}`)
	assert.Equal(t, http.StatusBadRequest, code, "unknown settings keys are refused")
	code, _ = serveProjectExtension(h, http.MethodPut, path, `{`)
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestExtensionProjectHandler_PutServiceErrorPropagates(t *testing.T) {
	svc := &MockProjectIntegrationService{
		UpsertTeamRelayFunc: func(context.Context, uuid.UUID, service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
			return nil, apierror.ValidationError(map[string]string{"share_slug": "required"})
		},
	}
	h := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)}, svc)

	code, _ := serveProjectExtension(h, http.MethodPut, "/projects/"+uuid.NewString()+"/extensions/team-relay", `{"enabled":true}`)
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestExtensionProjectHandler_Delete(t *testing.T) {
	projectID := uuid.New()
	var deleted uuid.UUID
	svc := &MockProjectIntegrationService{
		DeleteTeamRelayFunc: func(_ context.Context, id uuid.UUID) error {
			deleted = id
			return nil
		},
	}
	h := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)}, svc)
	path := "/projects/" + projectID.String() + "/extensions/team-relay"

	code, _ := serveProjectExtension(h, http.MethodDelete, path, "")
	assert.Equal(t, http.StatusNoContent, code)
	assert.Equal(t, projectID, deleted)

	svc.DeleteTeamRelayFunc = func(context.Context, uuid.UUID) error { return errors.New("boom") }
	code, _ = serveProjectExtension(h, http.MethodDelete, path, "")
	assert.Equal(t, http.StatusInternalServerError, code)
}

func TestDeprecatedAlias_SetsHeadersAndLeavesBody(t *testing.T) {
	e := echo.New()
	e.GET("/old", func(c echo.Context) error { return c.String(http.StatusOK, "body") },
		DeprecatedAlias("/api/v1/new"))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/old", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "body", rec.Body.String())
	assert.Equal(t, "true", rec.Header().Get("Deprecation"))
	assert.Equal(t, `</api/v1/new>; rel="successor-version"`, rec.Header().Get("Link"))
}
