package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Agent.URL is the deep-link to /w/<workspace slug>/team/agent/<agent slug> —
// the page that exists in the web app. Task, doc, project and comment already
// carry a url; agent did not.

func agentURLHandler(ws *MockWorkspaceService, svc *MockAgentService) *AgentHandler {
	h := NewAgentHandler(svc)
	if ws != nil {
		h.SetWorkspaceService(ws)
	}
	return h
}

func wsWithSlug(id uuid.UUID, slug string) *MockWorkspaceService {
	return &MockWorkspaceService{GetByIDFunc: func(_ context.Context, got uuid.UUID) (*domain.Workspace, error) {
		if got != id {
			return nil, errors.New("wrong workspace asked for")
		}
		return &domain.Workspace{ID: id, Slug: slug}, nil
	}}
}

func TestAgentHandler_GetByID_CarriesURL(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()
	svc := &MockAgentService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Agent, error) {
		return &domain.Agent{ID: agentID, WorkspaceID: wsID, Slug: "daedalus"}, nil
	}}
	h := agentURLHandler(wsWithSlug(wsID, "acme"), svc)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Host = "mesh.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("agent_id")
	c.SetParamValues(agentID.String())
	require.NoError(t, h.GetByID(c))

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "https://mesh.example/w/acme/team/agent/daedalus", out["url"])
}

func TestAgentHandler_List_CarriesURLUnderListedWorkspace(t *testing.T) {
	wsID := uuid.New()
	svc := &MockAgentService{ListFunc: func(context.Context, uuid.UUID, repository.AgentFilter, pagination.Params) (*pagination.Page[domain.Agent], error) {
		return &pagination.Page[domain.Agent]{Items: []domain.Agent{{Slug: "a"}, {Slug: "b"}}}, nil
	}}
	h := agentURLHandler(wsWithSlug(wsID, "acme"), svc)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Host = "mesh.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())
	require.NoError(t, h.List(c))

	var out struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Items, 2)
	assert.Equal(t, "https://mesh.example/w/acme/team/agent/a", out.Items[0]["url"])
	assert.Equal(t, "https://mesh.example/w/acme/team/agent/b", out.Items[1]["url"])
}

// One response, one workspace lookup: List must not resolve the same
// workspace once per agent on the page.
func TestAgentHandler_List_ResolvesWorkspaceOncePerResponse(t *testing.T) {
	wsID := uuid.New()
	svc := &MockAgentService{ListFunc: func(context.Context, uuid.UUID, repository.AgentFilter, pagination.Params) (*pagination.Page[domain.Agent], error) {
		return &pagination.Page[domain.Agent]{Items: []domain.Agent{{Slug: "a"}, {Slug: "b"}, {Slug: "c"}}}, nil
	}}
	lookups := 0
	ws := &MockWorkspaceService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Workspace, error) {
		lookups++
		return &domain.Workspace{ID: wsID, Slug: "acme"}, nil
	}}
	h := agentURLHandler(ws, svc)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Host = "mesh.example"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())
	require.NoError(t, h.List(c))

	assert.Equal(t, 1, lookups, "workspace must be resolved once for the whole page, not once per agent")
	var out struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Items, 3)
	assert.Equal(t, "http://mesh.example/w/acme/team/agent/c", out.Items[2]["url"])
}

// /agents/me links under the workspace the key authenticated into, not the
// agent's home row — the same distinction the workspace_id field already makes.
func TestAgentHandler_Me_URLUsesAuthWorkspace(t *testing.T) {
	agentID, homeWS, grantWS := uuid.New(), uuid.New(), uuid.New()
	svc := &MockAgentService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Agent, error) {
		return &domain.Agent{ID: agentID, WorkspaceID: homeWS, Slug: "guest"}, nil
	}}
	h := agentURLHandler(wsWithSlug(grantWS, "granted"), svc)

	rec := meRequest(t, h, http.MethodGet, agentID, grantWS, "", h.Me)
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Contains(t, out["url"], "/w/granted/team/agent/guest")
}

// Red control: no workspace service, or a failed lookup, means no url at all —
// a response is never failed or given a wrong link over a decoration.
func TestAgentHandler_URLAbsentWithoutWorkspaceSlug(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()
	svc := &MockAgentService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Agent, error) {
		return &domain.Agent{ID: agentID, WorkspaceID: wsID, Slug: "x"}, nil
	}}
	failing := &MockWorkspaceService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Workspace, error) {
		return nil, errors.New("db down")
	}}
	for name, h := range map[string]*AgentHandler{"no service": agentURLHandler(nil, svc), "lookup fails": agentURLHandler(failing, svc)} {
		rec := meRequest(t, h, http.MethodGet, agentID, wsID, "", h.Me)
		require.Equal(t, http.StatusOK, rec.Code, name)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		_, has := out["url"]
		assert.False(t, has, name)
	}
}
