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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func searchRequest(e *echo.Echo, projID string, wsID *uuid.UUID, rawQuery string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/?"+rawQuery, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/projects/:proj_id/documents/search")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID)
	return c, rec
}

func TestDocumentHandler_Search_PassesTheProjectFromThePathAndTheWorkspaceFromTheGuard(t *testing.T) {
	projID, wsID := uuid.New(), uuid.New()
	var gotProj, gotWS uuid.UUID
	var gotQuery string
	var gotLimit int

	h := NewDocumentHandler(&MockDocumentService{
		SearchFunc: func(_ context.Context, p, w uuid.UUID, q string, limit int) ([]domain.DocumentSearchHit, error) {
			gotProj, gotWS, gotQuery, gotLimit = p, w, q, limit
			return []domain.DocumentSearchHit{{ID: uuid.New(), Title: "Runbook", Snippet: "…", SnippetIsMatch: true}}, nil
		},
	})
	c, rec := searchRequest(echo.New(), projID.String(), &wsID, "q=rollback&limit=5")

	require.NoError(t, h.Search(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	// The tenant comes from the guard, the project from the path. Neither is read
	// from the query string — that is the whole reason the route is shaped this
	// way.
	assert.Equal(t, projID, gotProj)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, "rollback", gotQuery)
	assert.Equal(t, 5, gotLimit)

	var body struct {
		Items []domain.DocumentSearchHit `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	assert.True(t, body.Items[0].SnippetIsMatch, "the flag has to survive serialisation or the caller highlights blindly")
}

func TestDocumentHandler_Search_RefusesWithoutAWorkspace(t *testing.T) {
	h := NewDocumentHandler(&MockDocumentService{
		SearchFunc: func(context.Context, uuid.UUID, uuid.UUID, string, int) ([]domain.DocumentSearchHit, error) {
			t.Fatal("the service must not be reached without a workspace")
			return nil, nil
		},
	})
	c, rec := searchRequest(echo.New(), uuid.New().String(), nil, "q=x")

	require.NoError(t, h.Search(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentHandler_Search_RejectsAMalformedProjectID(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentHandler(&MockDocumentService{})
	c, rec := searchRequest(echo.New(), "not-a-uuid", &wsID, "q=x")

	require.NoError(t, h.Search(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Search_RejectsANonNumericLimit(t *testing.T) {
	// Silently ignoring it would answer a different question than the one asked.
	wsID := uuid.New()
	h := NewDocumentHandler(&MockDocumentService{})
	c, rec := searchRequest(echo.New(), uuid.New().String(), &wsID, "q=x&limit=lots")

	require.NoError(t, h.Search(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Search_SurfacesTheServiceRefusal(t *testing.T) {
	wsID := uuid.New()
	h := NewDocumentHandler(&MockDocumentService{
		SearchFunc: func(context.Context, uuid.UUID, uuid.UUID, string, int) ([]domain.DocumentSearchHit, error) {
			return nil, apierror.ValidationError(map[string]string{"q": "q is required"})
		},
	})
	c, rec := searchRequest(echo.New(), uuid.New().String(), &wsID, "")

	require.NoError(t, h.Search(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "q is required")
}
