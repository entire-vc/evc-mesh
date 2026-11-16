package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Project icons take the shared picker formats: one emoji,
// icon:<name> or empty. A new free-text value is a 422; a legacy text icon
// already stored keeps reading and may be sent back unchanged.

func createProjectWithIcon(t *testing.T, icon string, persisted *string) *httptest.ResponseRecorder {
	t.Helper()
	mockSvc := &MockProjectService{
		CreateFunc: func(ctx context.Context, p *domain.Project) error {
			*persisted = p.Icon
			return nil
		},
	}
	h, e := setupProjectTest(mockSvc)
	body, err := json.Marshal(map[string]string{"name": "P", "icon": icon})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/projects")
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())
	require.NoError(t, h.Create(c))
	return rec
}

func TestProjectHandler_Create_IconFormats(t *testing.T) {
	for _, icon := range []string{"🚀", "icon:rocket", ""} {
		t.Run("accept "+icon, func(t *testing.T) {
			persisted := "<unset>"
			rec := createProjectWithIcon(t, icon, &persisted)
			assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			assert.Equal(t, icon, persisted)
		})
	}
	for _, icon := range []string{"abc", "rocket", "icon:Rocket!", "<svg>", "🚀🚀"} {
		t.Run("refuse "+icon, func(t *testing.T) {
			persisted := "<unset>"
			rec := createProjectWithIcon(t, icon, &persisted)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
			assert.Equal(t, "<unset>", persisted, "a refused icon must not reach the service")
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			validation, _ := body["validation"].(map[string]any)
			assert.Contains(t, validation["icon"], "icon:rocket")
		})
	}
}

func patchProjectIcon(t *testing.T, storedIcon, body string) (*httptest.ResponseRecorder, *domain.Project) {
	t.Helper()
	projID := uuid.New()
	var saved *domain.Project
	mockSvc := &MockProjectService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
			return &domain.Project{ID: projID, Name: "Legacy", Icon: storedIcon}, nil
		},
		UpdateFunc: func(ctx context.Context, p *domain.Project) error {
			saved = p
			return nil
		},
	}
	h, e := setupProjectTest(mockSvc)
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())
	require.NoError(t, h.Update(c))
	return rec, saved
}

func TestProjectHandler_Update_IconFormats(t *testing.T) {
	t.Run("new valid values", func(t *testing.T) {
		for _, icon := range []string{"🚀", "icon:rocket", ""} {
			b, _ := json.Marshal(map[string]string{"icon": icon})
			rec, saved := patchProjectIcon(t, "OLD", string(b))
			assert.Equal(t, http.StatusOK, rec.Code, icon)
			require.NotNil(t, saved)
			assert.Equal(t, icon, saved.Icon)
		}
	})
	t.Run("new text value refused", func(t *testing.T) {
		rec, saved := patchProjectIcon(t, "", `{"icon":"abc"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Nil(t, saved)
	})
	t.Run("legacy text icon reads and survives other edits", func(t *testing.T) {
		rec, saved := patchProjectIcon(t, "OLD", `{"name":"Renamed"}`)
		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, saved)
		assert.Equal(t, "OLD", saved.Icon)
		var out domain.Project
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, "OLD", out.Icon)
	})
	t.Run("legacy text icon sent back unchanged", func(t *testing.T) {
		rec, saved := patchProjectIcon(t, "OLD", `{"icon":"OLD","name":"Renamed"}`)
		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, saved)
		assert.Equal(t, "OLD", saved.Icon)
	})
}
